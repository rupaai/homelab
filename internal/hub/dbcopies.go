package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/sharedir"
	"github.com/rupaai/homelab/internal/state"
)

// Databases (database: in homelab.yaml). Homelab runs the postgres container and makes the
// copies; homelab-services is the one that looks inside a database.
//
// A copy lives with the data copies made before a promote, in
// <Homelab folder>/backups/<app>/<env>/<time>-<label>/. For postgres it holds the file
// homelab-db.dump; for sqlite the folder homelab-db with app.db in it, whatever the file
// is called in the app (an app can keep its sqlite file in a data folder of its own, see
// database: {engine: sqlite, path: ...}).

// What a copy was made for. Copies made before a promote are named after the commit.
const (
	copyManual       = "manual"
	copyPromote      = "promote"
	copyBeforeRest   = "before-restore"
	copyBeforeProd   = "before-copy-from-prod"
	copyBeforeImport = "before-import"
	copyBeforeChange = "before-change" // before a write made from the companion's SQL box
)

// DBCopyView is one copy of an environment's database.
type DBCopyView struct {
	Name      string    `json:"name"`
	Made      time.Time `json:"made"`
	SizeBytes int64     `json:"size_bytes"`
	Kind      string    `json:"kind"` // manual | promote | before-restore | before-copy-from-prod | before-import | before-change
}

// DBEnvView is one environment's database and its copies. Port, User and Database are what
// someone needs to connect from another computer (postgres only).
type DBEnvView struct {
	Env      string       `json:"env"`
	Port     int          `json:"port,omitempty"`
	User     string       `json:"user,omitempty"`
	Database string       `json:"database,omitempty"`
	Copies   []DBCopyView `json:"copies"`
}

// DBView is an app's database: its kind, and each environment's copies.
type DBView struct {
	Engine string      `json:"engine"` // postgres | sqlite; empty when the app has no database
	Envs   []DBEnvView `json:"envs"`
}

var copyNameRe = regexp.MustCompile(`^(\d{8}-\d{6})-([a-z0-9][a-z0-9-]*)$`)

// sqlitePath is the sqlite file of an environment, on this computer.
func (h *Hub) sqlitePath(app, env string, m *manifest.Manifest) string {
	_, folder, rel, _ := m.SQLiteFile()
	return filepath.Join(h.dataDir(app, env), folder, filepath.FromSlash(rel))
}

// register tells homelab-services where the environment's database is.
func (h *Hub) register(a state.App, env string, m *manifest.Manifest) error {
	if h.ad.Databases == nil {
		return errors.New("Homelab's services aren't set up here")
	}
	reg := core.DatabaseRegistration{Engine: m.Database, Port: h.dbPort(a.Slot, env)}
	if m.Database == core.DatabaseSQLite {
		reg = core.DatabaseRegistration{Engine: m.Database, Path: h.sqlitePath(a.ID, env, m)}
	}
	return h.ad.Databases.Register(a.ID, env, reg)
}

// hasPostgres says whether a release of the app has a postgres database.
func (h *Hub) hasPostgres(rel core.Release, a state.App) bool {
	m, err := h.parse(rel.Manifest, a.Repo)
	return err == nil && m.Database == core.DatabasePostgres
}

// copyKind says what a copy named <time>-<label> was made for. ok is false for folders
// that aren't copies of a database's data.
func copyKind(name string) (kind string, ok bool) {
	g := copyNameRe.FindStringSubmatch(name)
	if g == nil || strings.HasPrefix(g[2], "replaced-by-restore-") {
		return "", false
	}
	switch g[2] {
	case copyManual, copyBeforeRest, copyBeforeProd, copyBeforeImport, copyBeforeChange:
		return g[2], true
	}
	return copyPromote, true
}

// dbCopies lists the copies of an environment's database, newest first.
func (h *Hub) dbCopies(app, env string) []DBCopyView {
	out := []DBCopyView{}
	root := filepath.Join(h.cfg.DataDir, "backups", app, env)
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		kind, ok := copyKind(e.Name())
		if !e.IsDir() || !ok {
			continue
		}
		size, has := copySize(filepath.Join(root, e.Name()))
		if !has {
			continue // a copy of the app's other data
		}
		made, _ := time.Parse("20060102-150405", e.Name()[:15])
		out = append(out, DBCopyView{Name: e.Name(), Made: made, SizeBytes: size, Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// copySize is the size of the database in a copy, and whether the copy has one.
func copySize(dir string) (int64, bool) {
	if st, err := os.Stat(filepath.Join(dir, dbDumpFile)); err == nil {
		return st.Size(), true
	}
	folder := filepath.Join(dir, core.DatabaseFolder)
	if _, err := os.Stat(folder); err != nil {
		return 0, false
	}
	var size int64
	_ = filepath.WalkDir(folder, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size, true
}

// Database describes an app's database and its copies.
func (h *Hub) Database(ctx context.Context, appID string) (DBView, error) {
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return DBView{}, userErr("There's no app %q.", appID)
	}
	m, err := h.parse(a.Manifest, a.Repo)
	if err != nil {
		return DBView{}, &UserError{Msg: err.Error()}
	}
	v := DBView{Engine: m.Database, Envs: []DBEnvView{}}
	if m.Database == "" {
		return v, nil
	}
	for _, env := range []string{core.Dev, core.Prod} {
		e := DBEnvView{Env: env, Copies: h.dbCopies(appID, env)}
		if m.Database == core.DatabasePostgres {
			e.Port, e.User, e.Database = h.dbPort(a.Slot, env), core.DatabaseUser, core.DatabaseName
		}
		v.Envs = append(v.Envs, e)
	}
	return v, nil
}

// dbTarget finds an app's database in one deployed environment.
func (h *Hub) dbTarget(ctx context.Context, appID, env string) (state.App, *manifest.Manifest, error) {
	if err := h.checkAppEnv(ctx, appID, env); err != nil {
		return state.App{}, nil, err
	}
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return a, nil, err
	}
	e, err := h.store.Env(ctx, appID, env)
	if err != nil {
		return a, nil, err
	}
	if e.Want == nil {
		return a, nil, userErr("Nothing is deployed to %s yet, so it has no database.", env)
	}
	m, err := h.parse(e.Want.Manifest, a.Repo)
	if err != nil {
		return a, nil, &UserError{Msg: err.Error()}
	}
	if m.Database == "" {
		return a, nil, userErr("%s doesn't have a database.", a.Name)
	}
	return a, m, nil
}

// copyFailed turns a deploy adapter's error into one for people.
func copyFailed(what string, err error) error {
	var ue *UserError
	if errors.As(err, &ue) {
		return err
	}
	msg := firstLine(strings.TrimSpace(err.Error()))
	return userErr("%s: %s", what, msg)
}

// copyNow copies an environment's database while its app runs (label says why) and returns
// the copy's folder. spare is a copy that must not be pruned. sqlite with no file yet has
// nothing to copy: the folder is "" then.
func (h *Hub) copyNow(ctx context.Context, a state.App, m *manifest.Manifest, env, label, spare string) (string, error) {
	file := h.sqlitePath(a.ID, env, m)
	if m.Database == core.DatabaseSQLite {
		if _, err := os.Stat(file); err != nil {
			return "", nil
		}
		if h.ad.Databases == nil {
			return "", errors.New("Homelab's services aren't set up here")
		}
		if err := h.register(a, env, m); err != nil {
			return "", err
		}
	}
	dst, root, err := h.newCopyDir(a.ID, env, label)
	if err != nil {
		return "", err
	}
	if m.Database == core.DatabasePostgres {
		err = h.ad.Deployer.DumpDatabase(ctx, a.ID, env, filepath.Join(dst, dbDumpFile))
	} else if err = os.Mkdir(filepath.Join(dst, core.DatabaseFolder), 0o700); err == nil {
		err = h.ad.Databases.Snapshot(a.ID, env, filepath.Join(dst, core.DatabaseFolder, core.DatabaseSQLiteFile))
	}
	if err != nil {
		h.ad.Deployer.Remove(ctx, dst)
		return "", err
	}
	h.pruneCopies(ctx, root, spare)
	return dst, nil
}

// MakeDBCopy copies an environment's database now. The app keeps running.
func (h *Hub) MakeDBCopy(ctx context.Context, appID, env string, by core.Viewer) (DBCopyView, error) {
	return h.makeDBCopy(ctx, appID, env, copyManual, "Copied the database in %s to %s", by)
}

// CopyBeforeChange copies prod's database before the companion writes to it. It returns
// the copy's name, which is "" when a sqlite database has no file yet, so there is nothing
// to lose.
func (h *Hub) CopyBeforeChange(ctx context.Context, appID string, by core.Viewer) (string, error) {
	c, err := h.makeDBCopy(ctx, appID, core.Prod, copyBeforeChange, "Copied the database in %s to %s before a change", by)
	var ue *UserError
	if errors.As(err, &ue) && strings.HasSuffix(ue.Msg, "so there is nothing to copy.") {
		return "", nil
	}
	return c.Name, err
}

func (h *Hub) makeDBCopy(ctx context.Context, appID, env, label, done string, by core.Viewer) (DBCopyView, error) {
	a, m, err := h.dbTarget(ctx, appID, env)
	if err != nil {
		return DBCopyView{}, err
	}
	lock := h.appLock(appID)
	if !lock.TryLock() {
		return DBCopyView{}, ErrBusy
	}
	defer lock.Unlock()
	dst, err := h.copyNow(ctx, a, m, env, label, "")
	if err == nil && dst == "" {
		return DBCopyView{}, userErr("The database in %s has no data yet, so there is nothing to copy.", env)
	}
	if err != nil {
		h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.failed", Detail: "Couldn't copy the database: " + firstLine(err.Error())})
		return DBCopyView{}, copyFailed("Couldn't copy the database", err)
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.copied", Detail: fmt.Sprintf(done, env, filepath.Base(dst))})
	for _, c := range h.dbCopies(appID, env) {
		if c.Name == filepath.Base(dst) {
			return c, nil
		}
	}
	return DBCopyView{Name: filepath.Base(dst), Kind: label}, nil
}

// RestoreDBCopy puts a copy back as the environment's database. It makes a copy of what
// is there first, stops the app, restores, and starts the app again. It returns once that
// has started; progress shows in the activity log.
func (h *Hub) RestoreDBCopy(ctx context.Context, appID, env, name string, by core.Viewer) error {
	a, m, err := h.dbTarget(ctx, appID, env)
	if err != nil {
		return err
	}
	if _, ok := copyKind(name); !ok {
		return userErr("There's no copy %q.", name)
	}
	from := filepath.Join(h.cfg.DataDir, "backups", appID, env, name)
	if _, has := copySize(from); !has {
		return userErr("There's no copy %q.", name)
	}
	lock := h.appLock(appID)
	if !lock.TryLock() {
		return ErrBusy
	}
	h.setBusy(appID, env, "Restoring the database")
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.restore_started",
		Detail: fmt.Sprintf("Restoring the database in %s from %s", env, name)})
	go func() {
		defer h.Kick()
		defer lock.Unlock()
		defer h.setBusy(appID, env, "")
		ctx := context.WithoutCancel(ctx)
		if err := h.restoreDBCopy(ctx, a, m, env, from); err != nil {
			h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.failed", Detail: err.Error()})
			return
		}
		h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.restored",
			Detail: fmt.Sprintf("Restored the database in %s from %s. What was there before is copied too", env, name)})
	}()
	return nil
}

func (h *Hub) restoreDBCopy(ctx context.Context, a state.App, m *manifest.Manifest, env, from string) error {
	return h.replaceDatabase(ctx, a, m, env, "restore", copyBeforeRest, filepath.Base(from), func() error {
		return h.putCopy(ctx, a, m, env, from)
	})
}

// replaceDatabase changes an environment's database with load while its app is stopped.
// It copies what is there first (label), and if load fails puts that copy back. The app
// starts again whatever happens. what ("restore" or "import") is for the messages, and
// spare is a copy that must not be pruned.
func (h *Hub) replaceDatabase(ctx context.Context, a state.App, m *manifest.Manifest, env, what, label, spare string, load func() error) error {
	if err := h.ad.Deployer.CheckFiles(ctx); err != nil && m.Database == core.DatabaseSQLite {
		return fmt.Errorf("Nothing changed, the database can't be copied: %w", err)
	}
	safety, err := h.copyNow(ctx, a, m, env, label, spare)
	if err != nil {
		return fmt.Errorf("Nothing changed, the database couldn't be copied first: %w", err)
	}
	if err := h.ad.Deployer.StopApp(ctx, a.ID, env); err != nil {
		return fmt.Errorf("Couldn't stop the app to %s its database: %w", what, err)
	}
	// The app starts again whatever happens, with the database as it is then.
	defer func() {
		if err := h.ad.Deployer.StartApp(ctx, a.ID, env); err != nil {
			h.log.Error("database: start the app again", "app", a.ID, "env", env, "err", err)
		}
	}()
	err = load()
	if err == nil {
		return nil
	}
	switch {
	case safety != "":
		if back := h.putCopy(ctx, a, m, env, safety); back != nil {
			return fmt.Errorf("The %s failed (%w), and putting the database back from %s failed too (%v)", what, err, filepath.Base(safety), back)
		}
	case m.Database == core.DatabaseSQLite:
		// There was no file before, so none is the way it was.
		if back := h.removeSQLite(ctx, a, m, env); back != nil {
			return fmt.Errorf("The %s failed (%w), and removing the half-made database failed too (%v)", what, err, back)
		}
	default:
		return fmt.Errorf("The %s failed: %w", what, err)
	}
	return fmt.Errorf("The %s failed (%w), so the database was put back as it was", what, err)
}

// putCopy makes the environment's database what a copy holds. The app must be stopped.
func (h *Hub) putCopy(ctx context.Context, a state.App, m *manifest.Manifest, env, from string) error {
	if m.Database == core.DatabasePostgres {
		return h.ad.Deployer.RestoreDatabase(ctx, a.ID, env, filepath.Join(from, dbDumpFile))
	}
	if !m.OwnsDatabaseFolder() {
		// The file sits in the app's own data folder with its other data, so only the file
		// is put back.
		return h.putSQLite(ctx, a, m, env, filepath.Join(from, core.DatabaseFolder, core.DatabaseSQLiteFile))
	}
	dir := filepath.Join(h.dataDir(a.ID, env), core.DatabaseFolder)
	if err := h.ad.Deployer.Remove(ctx, dir); err != nil {
		return err
	}
	if err := h.ad.Deployer.Copy(ctx, filepath.Join(from, core.DatabaseFolder), dir); err != nil {
		return err
	}
	return sharedir.Open(dir)
}

// removeSQLite removes an environment's sqlite file and its journal files, which belong to
// the file. Whatever else is in the folder stays.
func (h *Hub) removeSQLite(ctx context.Context, a state.App, m *manifest.Manifest, env string) error {
	file := h.sqlitePath(a.ID, env, m)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := h.ad.Deployer.Remove(ctx, file+suffix); err != nil {
			return err
		}
	}
	return nil
}

// putSQLite makes the environment's sqlite file a copy of the file src. The app must be
// stopped.
func (h *Hub) putSQLite(ctx context.Context, a state.App, m *manifest.Manifest, env, src string) error {
	if err := h.removeSQLite(ctx, a, m, env); err != nil {
		return err
	}
	file := h.sqlitePath(a.ID, env, m)
	// Let containers write what Homelab made: the folder when it is small and ours, the file
	// alone when the app's data folder may hold a lot.
	open, dir := file, filepath.Dir(file)
	if _, err := os.Stat(dir); err != nil || m.OwnsDatabaseFolder() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		open = dir
	}
	if err := h.ad.Deployer.Copy(ctx, src, file); err != nil {
		return err
	}
	return sharedir.Open(open)
}

// CopyProdToDev replaces dev's database with a copy of prod's. Prod keeps running. Dev gets
// a copy of its own database first, and its app is stopped while the database is replaced.
// It returns once that has started; progress shows in the activity log.
func (h *Hub) CopyProdToDev(ctx context.Context, appID string, by core.Viewer) error {
	a, m, err := h.dbTarget(ctx, appID, core.Prod)
	if err != nil {
		return err
	}
	if _, _, err := h.dbTarget(ctx, appID, core.Dev); err != nil {
		return err
	}
	lock := h.appLock(appID)
	if !lock.TryLock() {
		return ErrBusy
	}
	h.setBusy(appID, core.Dev, "Copying prod's database")
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: core.Dev, Action: "db.copy_to_dev_started", Detail: "Copying prod's database to dev"})
	go func() {
		defer h.Kick()
		defer lock.Unlock()
		defer h.setBusy(appID, core.Dev, "")
		ctx := context.WithoutCancel(ctx)
		if err := h.copyProdToDev(ctx, a, m); err != nil {
			h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: core.Dev, Action: "db.failed", Detail: err.Error()})
			return
		}
		h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: core.Dev, Action: "db.copied_to_dev",
			Detail: "Copied prod's database to dev. What was in dev before is copied too"})
	}()
	return nil
}

func (h *Hub) copyProdToDev(ctx context.Context, a state.App, m *manifest.Manifest) error {
	if err := h.ad.Deployer.CheckFiles(ctx); err != nil && m.Database == core.DatabaseSQLite {
		return fmt.Errorf("Nothing changed, the database can't be copied: %w", err)
	}
	// Prod's database goes into a folder of its own until dev has it. It isn't kept.
	parent := filepath.Join(h.cfg.DataDir, "backups", a.ID)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if m.Database == core.DatabasePostgres {
		err = h.ad.Deployer.DumpDatabase(ctx, a.ID, core.Prod, filepath.Join(tmp, dbDumpFile))
	} else {
		if _, serr := os.Stat(h.sqlitePath(a.ID, core.Prod, m)); serr != nil {
			return errors.New("Nothing changed, prod's database has no data yet")
		}
		if err = h.register(a, core.Prod, m); err == nil {
			err = os.Mkdir(filepath.Join(tmp, core.DatabaseFolder), 0o700)
		}
		if err == nil {
			err = h.ad.Databases.Snapshot(a.ID, core.Prod, filepath.Join(tmp, core.DatabaseFolder, core.DatabaseSQLiteFile))
		}
	}
	if err != nil {
		return fmt.Errorf("Nothing changed, prod's database couldn't be copied: %w", err)
	}
	safety, err := h.copyNow(ctx, a, m, core.Dev, copyBeforeProd, "")
	if err != nil {
		return fmt.Errorf("Nothing changed, dev's database couldn't be copied first: %w", err)
	}
	if err := h.ad.Deployer.StopApp(ctx, a.ID, core.Dev); err != nil {
		return fmt.Errorf("Couldn't stop dev's app to replace its database: %w", err)
	}
	defer func() {
		if err := h.ad.Deployer.StartApp(ctx, a.ID, core.Dev); err != nil {
			h.log.Error("database: start the app again", "app", a.ID, "env", core.Dev, "err", err)
		}
	}()
	err = h.putProd(ctx, a, m, tmp)
	if err == nil {
		return nil
	}
	if safety != "" {
		if back := h.putCopy(ctx, a, m, core.Dev, safety); back != nil {
			return fmt.Errorf("Copying prod's database failed (%w), and putting dev's back from %s failed too (%v)", err, filepath.Base(safety), back)
		}
		return fmt.Errorf("Copying prod's database failed (%w), so dev's database was put back as it was", err)
	}
	return fmt.Errorf("Copying prod's database failed: %w", err)
}

// putProd makes dev's database what prod's was, from the folder it was copied to.
func (h *Hub) putProd(ctx context.Context, a state.App, m *manifest.Manifest, from string) error {
	if m.Database == core.DatabasePostgres {
		return h.ad.Deployer.RestoreDatabase(ctx, a.ID, core.Dev, filepath.Join(from, dbDumpFile))
	}
	// Only the file is replaced; whatever else the folder holds stays. The old file's
	// journal files go too, because they belong to the old file.
	return h.putSQLite(ctx, a, m, core.Dev, filepath.Join(from, core.DatabaseFolder, core.DatabaseSQLiteFile))
}
