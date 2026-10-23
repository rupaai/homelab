package hub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// Importing a file into an environment's database (the companion uploads it). The file is
// kept in the environment's backups folder while it loads, never in memory.

// MaxImport is the biggest file an import takes.
const MaxImport = 50 << 30

// importLimit is MaxImport; tests lower it.
var importLimit int64 = MaxImport

// What an imported file is, from its first bytes.
const (
	ImportSQLite = "sqlite"  // a sqlite database file
	ImportPGDump = "pg_dump" // a postgres dump made with pg_dump -Fc
	ImportSQL    = "sql"     // a plain SQL script
)

const sqliteMagic = "SQLite format 3\x00"

// importKind says what a file is, from its first bytes. ok is false for anything else
// (binary data that is neither).
func importKind(head []byte) (kind string, ok bool) {
	switch {
	case bytes.HasPrefix(head, []byte(sqliteMagic)):
		return ImportSQLite, true
	case bytes.HasPrefix(head, []byte("PGDMP")):
		return ImportPGDump, true
	case bytes.IndexByte(head, 0) >= 0:
		return "", false
	}
	return ImportSQL, true
}

// checkImport says why a file of this kind can't go into the app's database, or nil.
func checkImport(app, engine string, head []byte) (string, error) {
	kind, ok := importKind(head)
	switch {
	case len(head) == 0:
		return "", userErr("That file is empty.")
	case engine == core.DatabaseSQLite && kind == ImportSQLite:
		return kind, nil
	case engine == core.DatabaseSQLite:
		return "", userErr("%s uses SQLite, so send a SQLite database file.", app)
	case !ok:
		return "", userErr("Homelab doesn't know that kind of file. Send a Postgres dump (pg_dump -Fc) or a plain .sql file.")
	case kind == ImportSQLite:
		return "", userErr("That is a SQLite file, but %s uses Postgres. Send a Postgres dump (pg_dump -Fc) or a plain .sql file.", app)
	}
	return kind, nil
}

// readFailed notes an error from the uploaded body, as opposed to one from writing the file.
type readFailed struct {
	r   io.Reader
	err error
}

func (f *readFailed) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && err != io.EOF {
		f.err = err
	}
	return n, err
}

// ImportDB loads the uploaded file into an environment's database, replacing what is
// there. It keeps the upload in a file, checks that it is the right kind for the app's
// database, and returns that kind once the work has started: the app is stopped, the
// database is copied and replaced, and the app starts again. If the load fails the copy
// goes back. Progress shows in the activity log.
func (h *Hub) ImportDB(ctx context.Context, appID, env string, body io.Reader, by core.Viewer) (string, error) {
	a, m, err := h.dbTarget(ctx, appID, env)
	if err != nil {
		return "", err
	}
	lock := h.appLock(appID)
	if !lock.TryLock() {
		return "", ErrBusy
	}
	started := false
	defer func() {
		if !started {
			lock.Unlock()
		}
	}()
	root := filepath.Join(h.cfg.DataDir, "backups", appID, env)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(root, ".import-")
	if err != nil {
		return "", err
	}
	file := f.Name()
	defer func() {
		if !started {
			os.Remove(file)
		}
	}()
	in := &readFailed{r: io.LimitReader(body, importLimit+1)}
	size, err := io.Copy(f, in)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case in.err != nil:
		return "", userErr("The upload didn't finish, so nothing was changed.")
	case err != nil:
		return "", err
	case size > importLimit:
		return "", userErr("That file is bigger than the %d GB Homelab takes.", importLimit>>30)
	}
	head, err := firstBytes(file, 8<<10)
	if err != nil {
		return "", err
	}
	kind, err := checkImport(a.Name, m.Database, head)
	if err != nil {
		return "", err
	}

	started = true
	h.setBusy(appID, env, "Importing into the database")
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.import_started",
		Detail: fmt.Sprintf("Importing a %s file (%d bytes) into the database in %s", kind, size, env)})
	go func() {
		defer h.Kick()
		defer lock.Unlock()
		defer h.setBusy(appID, env, "")
		defer os.Remove(file)
		ctx := context.WithoutCancel(ctx)
		if err := h.replaceDatabase(ctx, a, m, env, "import", copyBeforeImport, "", func() error {
			return h.loadImport(ctx, a, m, env, kind, file)
		}); err != nil {
			h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.failed", Detail: err.Error()})
			return
		}
		h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Env: env, Action: "db.imported",
			Detail: fmt.Sprintf("Imported the file into the database in %s. What was there before is copied too", env)})
	}()
	return kind, nil
}

// firstBytes reads up to n bytes from the start of a file.
func firstBytes(file string, n int) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	got, err := io.ReadFull(f, b)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return b[:got], err
}

// loadImport makes the environment's database what the file holds. The app must be stopped.
func (h *Hub) loadImport(ctx context.Context, a state.App, m *manifest.Manifest, env, kind, file string) error {
	switch kind {
	case ImportSQLite:
		return h.putSQLite(ctx, a, m, env, file)
	case ImportPGDump:
		return h.ad.Deployer.RestoreDatabase(ctx, a.ID, env, file)
	}
	return h.ad.Deployer.ImportSQL(ctx, a.ID, env, file)
}
