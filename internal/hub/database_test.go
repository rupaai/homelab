package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

const (
	postgresDoc = manifestDoc + "database: postgres\n"
	sqliteDoc   = manifestDoc + "database: sqlite\n"
)

// dbApp adds the movies app with doc and puts aaaaaaa1 on dev.
func (r *rig) dbApp(doc string) {
	r.t.Helper()
	ctx := context.Background()
	r.addPeople()
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(doc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		r.t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.pushWith("aaaaaaa1", doc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	if r.running(core.Dev) != "aaaaaaa1" {
		r.t.Fatal("dev isn't running")
	}
}

// promoteDB puts what is on dev onto prod.
func (r *rig) promoteDB() {
	r.t.Helper()
	ctx := context.Background()
	r.hub.CheckHealth(ctx)
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		r.t.Fatal(err)
	}
	r.settle()
}

func (r *rig) dataPath(env string, parts ...string) string {
	return filepath.Join(append([]string{r.dir, "apps", "movies", env, "data"}, parts...)...)
}

// copyDirs lists the copies of an environment made for label (a commit, or manual, ...).
func (r *rig) copyDirs(env, label string) []string {
	var out []string
	dirs, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", env, "*"))
	for _, d := range dirs {
		if strings.TrimPrefix(filepath.Base(d), filepath.Base(d)[:16]) == label {
			out = append(out, d)
		}
	}
	return out
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// callIndex is where a call first appears after position from, or -1.
func callIndex(calls []string, call string, from int) int {
	for i := from; i < len(calls); i++ {
		if calls[i] == call {
			return i
		}
	}
	return -1
}

func TestPostgresSpec(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	spec := r.dep.Specs["movies/dev"]
	if spec.Postgres == nil || spec.Postgres.Port != 17015 || spec.Postgres.DataDir != r.dataPath(core.Dev, "homelab-db") {
		t.Fatalf("postgres = %+v", spec.Postgres)
	}
	if st, err := os.Stat(spec.Postgres.DataDir); err != nil || !st.IsDir() {
		t.Fatalf("data folder: %v", err)
	}
	pw := spec.Secrets[core.DatabasePassword]
	if !regexp.MustCompile(`^[a-z0-9-]{20,}$`).MatchString(pw) {
		t.Fatalf("password %q", pw)
	}
	url := "postgres://app:" + pw + "@db:5432/app"
	if spec.Secrets[core.DatabaseURL] != url || spec.Vars[core.DatabaseURL] != url {
		t.Fatalf("url secret %q, setting %q", spec.Secrets[core.DatabaseURL], spec.Vars[core.DatabaseURL])
	}
	if r.sec.Values["movies/dev/DATABASE_PASSWORD"] != pw || r.sec.Values["movies/dev/DATABASE_URL"] != "" {
		t.Fatal("only the password is stored; the URL is made at each deploy")
	}
	if got := r.db.Registered["movies/dev"]; got != (core.DatabaseRegistration{Engine: "postgres", Port: 17015}) {
		t.Fatalf("registered %+v", got)
	}

	// A later deploy keeps the password.
	if err := r.hub.DeployDev(ctx, "movies", "aaaaaaa1", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.dep.Specs["movies/dev"].Secrets[core.DatabasePassword]; got != pw {
		t.Fatalf("the password changed on a second deploy")
	}

	// Prod has its own password and port.
	r.promoteDB()
	prod := r.dep.Specs["movies/prod"]
	if prod.Postgres == nil || prod.Postgres.Port != 17016 || prod.Secrets[core.DatabasePassword] == "" || prod.Secrets[core.DatabasePassword] == pw {
		t.Fatalf("prod = %+v", prod.Postgres)
	}
	if got := r.db.Registered["movies/prod"]; got.Port != 17016 {
		t.Fatalf("registered prod %+v", got)
	}

	// The password is shown with the other generated secrets, and can't be changed.
	envs, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range envs[0].Secrets {
		if s.Name == core.DatabasePassword {
			found = s.Generated && s.Set && s.Declared
		}
	}
	if !found {
		t.Fatalf("dev secrets: %+v", envs[0].Secrets)
	}
	if v, err := r.hub.RevealSecret(ctx, "movies", core.Dev, core.DatabasePassword, owner); err != nil || v != pw {
		t.Fatalf("reveal: %v", err)
	}
	for name, err := range map[string]error{
		"set":        r.hub.SetSecret(ctx, "movies", core.Dev, core.DatabasePassword, "other", owner),
		"regenerate": r.hub.RegenerateSecret(ctx, "movies", core.Dev, core.DatabasePassword, owner),
		"delete":     r.hub.DeleteSecret(ctx, "movies", core.Dev, core.DatabasePassword, owner),
		"restore":    r.hub.RestoreSecret(ctx, "movies", core.Dev, core.DatabasePassword, 1, owner),
	} {
		if !isUserErr(err) || !strings.Contains(err.Error(), "database's own password") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r.sec.Values["movies/dev/DATABASE_PASSWORD"] != pw {
		t.Fatal("the password was changed")
	}
}

func TestPostgresSecretsAsFiles(t *testing.T) {
	r := newRig(t)
	r.dbApp(postgresDoc + "secrets_as: files\n")
	spec := r.dep.Specs["movies/dev"]
	if _, ok := spec.Vars[core.DatabaseURL]; ok || spec.Secrets[core.DatabaseURL] == "" || !spec.SecretsAsFiles {
		t.Fatalf("with secrets_as: files the URL is only a file: vars %v", spec.Vars)
	}
}

func TestSQLiteSpec(t *testing.T) {
	r := newRig(t)
	r.dbApp(sqliteDoc)
	spec := r.dep.Specs["movies/dev"]
	if spec.Postgres != nil || spec.Secrets[core.DatabasePassword] != "" {
		t.Fatalf("sqlite got a postgres: %+v", spec.Postgres)
	}
	if spec.Vars["DATABASE_URL"] != "sqlite:////homelab/db/app.db" || spec.Vars["DATABASE_PATH"] != "/homelab/db/app.db" {
		t.Fatalf("vars %v", spec.Vars)
	}
	want := core.Mount{HostPath: r.dataPath(core.Dev, "homelab-db"), ContainerPath: "/homelab/db"}
	if !slices.Contains(spec.Mounts, want) {
		t.Fatalf("mounts %+v", spec.Mounts)
	}
	if got := r.db.Registered["movies/dev"]; got != (core.DatabaseRegistration{Engine: "sqlite", Path: r.dataPath(core.Dev, "homelab-db", "app.db")}) {
		t.Fatalf("registered %+v", got)
	}
}

func TestNoDatabaseNothingAdded(t *testing.T) {
	r := newRig(t)
	r.dbApp(manifestDoc)
	spec := r.dep.Specs["movies/dev"]
	if spec.Postgres != nil || spec.Vars["DATABASE_URL"] != "" || len(r.db.Calls) != 0 {
		t.Fatalf("spec %+v, calls %v", spec, r.db.Calls)
	}
}

// A failed registration never fails a deploy.
func TestRegistrationFailureIsOnlyLogged(t *testing.T) {
	r := newRig(t)
	r.db.RegisterErr = errors.New("services are down")
	r.dbApp(postgresDoc)
	if r.running(core.Dev) != "aaaaaaa1" || r.env(core.Dev).Health == core.Down {
		t.Fatal("dev should run without the registration")
	}
	if len(r.events("deploy.failed")) != 0 {
		t.Fatal("a deploy failed")
	}
}

func TestRemoveAppForgetsDatabases(t *testing.T) {
	r := newRig(t)
	r.dbApp(postgresDoc)
	if err := r.hub.RemoveApp(context.Background(), "movies", owner); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.db.Calls, "forget movies") || len(r.db.Registered) != 0 {
		t.Fatalf("calls %v", r.db.Calls)
	}
}

// A promote dumps prod's postgres while the app is stopped, and a promote that fails puts
// the dump back before the old version starts.
func TestPromoteDumpsAndRestoresPostgres(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.promoteDB() // prod has no database yet, so there is nothing to dump
	if callIndex(r.dep.Calls, "dump-db movies/prod", 0) >= 0 {
		t.Fatalf("dumped a database that doesn't exist yet: %v", r.dep.Calls)
	}
	r.dep.SetDB("movies", core.Prod, "prod rows")

	r.pushWith("bbbbbbb2", postgresDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.dep.Calls = nil
	r.hub.CheckHealth(ctx)
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	stop := callIndex(r.dep.Calls, "stop-app movies/prod", 0)
	dump := callIndex(r.dep.Calls, "dump-db movies/prod", 0)
	deploy := callIndex(r.dep.Calls, "deploy movies/prod sha256:bbbbbbb2", 0)
	if stop < 0 || dump < stop || deploy < dump {
		t.Fatalf("want stop, dump, deploy: %v", r.dep.Calls)
	}
	dirs := r.copyDirs(core.Prod, "bbbbbbb")
	if len(dirs) != 1 {
		t.Fatalf("copies %v", dirs)
	}
	if got := read(t, filepath.Join(dirs[0], "homelab-db.dump")); got != "prod rows" {
		t.Fatalf("dump holds %q", got)
	}
	// The first promote's copy has no database in it, so it isn't listed.
	if views := r.hub.dbCopies("movies", core.Prod); len(views) != 1 || views[0].Kind != "promote" || views[0].SizeBytes != 9 {
		t.Fatalf("listed %+v", views)
	}

	// The next promote fails: the new version changes the database and isn't healthy.
	r.pushWith("ccccccc3", postgresDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	r.health.SetFailWhen(func(url string) bool {
		if strings.HasSuffix(url, ":17011/healthz") && r.running(core.Prod) == "ccccccc3" {
			r.dep.SetDB("movies", core.Prod, "changed by ccccccc3")
			return true
		}
		return false
	})
	r.dep.Calls = nil
	r.dep.SetDB("movies", core.Prod, "rows before ccccccc3")
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "bbbbbbb2" {
		t.Fatalf("prod runs %q after the failed promote", got)
	}
	if got := r.dep.DBContent("movies", core.Prod); got != "rows before ccccccc3" {
		t.Fatalf("database after going back: %q", got)
	}
	restore := callIndex(r.dep.Calls, "restore-db movies/prod", 0)
	back := callIndex(r.dep.Calls, "deploy movies/prod sha256:bbbbbbb2", restore)
	if restore < 0 || back < restore {
		t.Fatalf("want the restore before the old version starts: %v", r.dep.Calls)
	}
	if evs := r.events("data.restored"); len(evs) != 2 || !strings.Contains(evs[0], "prod's database") {
		t.Fatalf("data.restored events: %v", evs)
	}
}

// If the dump can't be made, nothing changes and the old version starts again.
func TestPromoteStopsWhenTheDumpFails(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.promoteDB()
	r.pushWith("bbbbbbb2", postgresDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	r.dep.DumpErr = errors.New("no database")
	r.dep.Calls = nil
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if callIndex(r.dep.Calls, "deploy movies/prod sha256:bbbbbbb2", 0) >= 0 {
		t.Fatalf("the new version was deployed: %v", r.dep.Calls)
	}
	if evs := r.events("promote.failed"); len(evs) != 1 || !strings.Contains(evs[0], "dumped") {
		t.Fatalf("promote.failed: %v", evs)
	}
	if dirs := r.copyDirs(core.Prod, "bbbbbbb"); len(dirs) != 0 {
		t.Fatalf("a failed dump left %v", dirs)
	}
	r.hub.Reconcile(ctx) // the app that was stopped starts again
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q", got)
	}
}

// A sqlite file is copied with the app's other data, and put back by a failed promote.
func TestPromoteCopiesSQLite(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(sqliteDoc)
	r.promoteDB()
	file := r.dataPath(core.Prod, "homelab-db", "app.db")
	if err := os.WriteFile(file, []byte("prod rows"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.pushWith("bbbbbbb2", sqliteDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	r.health.SetFailWhen(func(url string) bool {
		if strings.HasSuffix(url, ":17011/healthz") && r.running(core.Prod) == "bbbbbbb2" {
			_ = os.WriteFile(file, []byte("changed by bbbbbbb2"), 0o644)
			return true
		}
		return false
	})
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	dirs := r.copyDirs(core.Prod, "bbbbbbb")
	if len(dirs) != 1 || read(t, filepath.Join(dirs[0], "homelab-db", "app.db")) != "prod rows" {
		t.Fatalf("copies %v", dirs)
	}
	if got := read(t, file); got != "prod rows" {
		t.Fatalf("file after going back: %q", got)
	}
}

func TestRestoreDataIgnoresTheDump(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	from := t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "homelab-db.dump"), []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept, err := r.hub.restoreData(ctx, "movies", core.Prod, from, "x")
	if err != nil || kept != "" {
		t.Fatalf("only a dump: kept %q, %v", kept, err)
	}
	if _, err := os.Stat(r.dataPath(core.Prod, "homelab-db.dump")); err == nil {
		t.Fatal("the dump was copied into the data folders")
	}
	if err := os.MkdirAll(filepath.Join(from, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "db", "a"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.dataPath(core.Prod, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.dataPath(core.Prod, "db", "a"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept, err = r.hub.restoreData(ctx, "movies", core.Prod, from, "x")
	if err != nil || read(t, r.dataPath(core.Prod, "db", "a")) != "old" || read(t, filepath.Join(kept, "db", "a")) != "new" {
		t.Fatalf("kept %q, %v", kept, err)
	}
	if _, err := os.Stat(r.dataPath(core.Prod, "homelab-db.dump")); err == nil {
		t.Fatal("the dump was copied into the data folders")
	}
}

func TestMakeDBCopyPostgres(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.dep.SetDB("movies", core.Dev, "rows v1")
	r.dep.Calls = nil
	c, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.Name, "-manual") || c.Kind != "manual" || c.SizeBytes != 7 || c.Made.IsZero() {
		t.Fatalf("copy %+v", c)
	}
	if slices.Contains(r.dep.Calls, "stop-app movies/dev") {
		t.Fatal("a copy now must not stop the app")
	}
	if got := read(t, filepath.Join(r.dir, "backups", "movies", "dev", c.Name, "homelab-db.dump")); got != "rows v1" {
		t.Fatalf("dump holds %q", got)
	}
	if evs := r.events("db.copied"); len(evs) != 1 || !strings.Contains(evs[0], c.Name) {
		t.Fatalf("events %v", evs)
	}
	v, err := r.hub.Database(ctx, "movies")
	if err != nil || v.Engine != "postgres" || len(v.Envs) != 2 {
		t.Fatalf("view %+v %v", v, err)
	}
	dev := v.Envs[0]
	if dev.Env != "dev" || dev.Port != 17015 || dev.User != "app" || dev.Database != "app" || len(dev.Copies) != 1 || dev.Copies[0].Name != c.Name {
		t.Fatalf("dev %+v", dev)
	}
	if prod := v.Envs[1]; prod.Port != 17016 || len(prod.Copies) != 0 || prod.Copies == nil {
		t.Fatalf("prod %+v", prod)
	}

	// A second copy in the same second is refused, and the first stays.
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); !isUserErr(err) {
		t.Fatalf("same second: %v", err)
	}
	if got := read(t, filepath.Join(r.dir, "backups", "movies", "dev", c.Name, "homelab-db.dump")); got != "rows v1" {
		t.Fatalf("the first copy was damaged: %q", got)
	}

	// Prod isn't deployed; a failed dump leaves nothing behind.
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Prod, owner); !isUserErr(err) || !strings.Contains(err.Error(), "Nothing is deployed") {
		t.Fatalf("prod: %v", err)
	}
	r.advance(2 * time.Second)
	r.dep.DumpErr = errors.New("the database is down")
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); !isUserErr(err) || !strings.Contains(err.Error(), "the database is down") {
		t.Fatalf("failed dump: %v", err)
	}
	if got := r.hub.dbCopies("movies", core.Dev); len(got) != 1 {
		t.Fatalf("copies %v", got)
	}
	if evs := r.events("db.failed"); len(evs) != 2 || !strings.Contains(evs[0], "the database is down") {
		t.Fatalf("db.failed events: %v", evs)
	}
}

func TestDatabaseOfAnAppWithoutOne(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(manifestDoc)
	v, err := r.hub.Database(ctx, "movies")
	if err != nil || v.Engine != "" || len(v.Envs) != 0 || v.Envs == nil {
		t.Fatalf("view %+v %v", v, err)
	}
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); !isUserErr(err) || !strings.Contains(err.Error(), "doesn't have a database") {
		t.Fatalf("copy: %v", err)
	}
	if _, err := r.hub.Database(ctx, "nope"); !isUserErr(err) {
		t.Fatalf("unknown app: %v", err)
	}
}

func TestRestoreDBCopyPostgres(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.dep.SetDB("movies", core.Dev, "v1")
	c, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner)
	if err != nil {
		t.Fatal(err)
	}
	r.dep.SetDB("movies", core.Dev, "v2")
	r.advance(2 * time.Second)
	r.dep.Calls = nil
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, c.Name, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.dep.DBContent("movies", core.Dev); got != "v1" {
		t.Fatalf("database holds %q", got)
	}
	stop := callIndex(r.dep.Calls, "stop-app movies/dev", 0)
	restore := callIndex(r.dep.Calls, "restore-db movies/dev", stop)
	start := callIndex(r.dep.Calls, "start-app movies/dev", restore)
	if stop < 0 || restore < 0 || start < 0 || callIndex(r.dep.Calls, "dump-db movies/dev", 0) > stop {
		t.Fatalf("want a safety dump, stop, restore, start: %v", r.dep.Calls)
	}
	if r.running(core.Dev) != "aaaaaaa1" {
		t.Fatal("dev isn't running after the restore")
	}
	var safety DBCopyView
	for _, x := range r.hub.dbCopies("movies", core.Dev) {
		if x.Kind == "before-restore" {
			safety = x
		}
	}
	if safety.Name == "" || read(t, filepath.Join(r.dir, "backups", "movies", "dev", safety.Name, "homelab-db.dump")) != "v2" {
		t.Fatalf("safety copy %+v", safety)
	}
	if evs := r.events("db.restored"); len(evs) != 1 {
		t.Fatalf("db.restored: %v", evs)
	}
	if e := r.env(core.Dev); e.Busy != "" {
		t.Fatalf("busy: %q", e.Busy)
	}

	// Names that aren't copies of this environment are refused.
	for _, name := range []string{"", "../prod/x", "20261023-120000-manual", c.Name + "/..", "20260101-000000-replaced-by-restore-x"} {
		if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, name, owner); !isUserErr(err) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Prod, c.Name, owner); !isUserErr(err) {
		t.Errorf("prod: %v", err)
	}
}

// If the restore fails, the database goes back to what it held and the app starts again.
func TestRestoreDBCopyFailurePutsTheDatabaseBack(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.dep.SetDB("movies", core.Dev, "v1")
	c, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner)
	if err != nil {
		t.Fatal(err)
	}
	r.dep.SetDB("movies", core.Dev, "v2")
	r.advance(2 * time.Second)
	r.dep.RestoreErrs = []error{errors.New("pg_restore failed")}
	r.dep.Calls = nil
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, c.Name, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.dep.DBContent("movies", core.Dev); got != "v2" {
		t.Fatalf("database holds %q, want what it held before", got)
	}
	if evs := r.events("db.failed"); len(evs) != 1 || !strings.Contains(evs[0], "put back") {
		t.Fatalf("db.failed: %v", evs)
	}
	if len(r.events("db.restored")) != 0 || r.running(core.Dev) != "aaaaaaa1" {
		t.Fatalf("restored events %v, running %q", r.events("db.restored"), r.running(core.Dev))
	}
}

// Making the safety copy must not delete the copy being restored.
func TestRestoreKeepsTheCopyItRestores(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	var oldest string
	for i := 0; i < r.hub.cfg.KeepBackups; i++ {
		r.advance(2 * time.Second)
		r.dep.SetDB("movies", core.Dev, "copy "+string(rune('a'+i)))
		c, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner)
		if err != nil {
			t.Fatal(err)
		}
		if oldest == "" {
			oldest = c.Name
		}
	}
	r.advance(2 * time.Second)
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, oldest, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.dep.DBContent("movies", core.Dev); got != "copy a" {
		t.Fatalf("database holds %q", got)
	}
}

func TestCopyProdToDevPostgres(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); !isUserErr(err) || !strings.Contains(err.Error(), "Nothing is deployed to prod") {
		t.Fatalf("prod not deployed: %v", err)
	}
	r.promoteDB()
	r.dep.SetDB("movies", core.Prod, "prod data")
	r.dep.SetDB("movies", core.Dev, "dev data")
	r.advance(2 * time.Second)
	r.dep.Calls = nil
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.dep.DBContent("movies", core.Dev) != "prod data" || r.dep.DBContent("movies", core.Prod) != "prod data" {
		t.Fatalf("dev %q, prod %q", r.dep.DBContent("movies", core.Dev), r.dep.DBContent("movies", core.Prod))
	}
	for _, c := range r.dep.Calls {
		if c == "stop-app movies/prod" || c == "stop movies/prod" {
			t.Fatalf("prod was stopped: %v", r.dep.Calls)
		}
	}
	if callIndex(r.dep.Calls, "stop-app movies/dev", 0) < 0 || callIndex(r.dep.Calls, "start-app movies/dev", 0) < 0 {
		t.Fatalf("dev's app should be stopped and started: %v", r.dep.Calls)
	}
	var safety DBCopyView
	for _, x := range r.hub.dbCopies("movies", core.Dev) {
		if x.Kind == "before-copy-from-prod" {
			safety = x
		}
	}
	if safety.Name == "" || read(t, filepath.Join(r.dir, "backups", "movies", "dev", safety.Name, "homelab-db.dump")) != "dev data" {
		t.Fatalf("safety copy %+v", safety)
	}
	if left, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", ".copy-*")); len(left) != 0 {
		t.Fatalf("left over: %v", left)
	}
	if evs := r.events("db.copied_to_dev"); len(evs) != 1 {
		t.Fatalf("events %v", evs)
	}

	// If prod can't be dumped nothing changes in dev.
	r.advance(2 * time.Second)
	r.dep.SetDB("movies", core.Dev, "dev data 2")
	r.dep.DumpErr = errors.New("prod database is down")
	r.dep.Calls = nil
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.dep.DBContent("movies", core.Dev) != "dev data 2" || callIndex(r.dep.Calls, "stop-app movies/dev", 0) >= 0 {
		t.Fatalf("dev changed: %q %v", r.dep.DBContent("movies", core.Dev), r.dep.Calls)
	}
	if evs := r.events("db.failed"); len(evs) != 1 || !strings.Contains(evs[0], "Nothing changed") {
		t.Fatalf("db.failed: %v", evs)
	}
}

func TestSQLiteCopies(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(sqliteDoc)
	devFile := r.dataPath(core.Dev, "homelab-db", "app.db")

	// Nothing to copy until the app has made its file.
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); !isUserErr(err) || !strings.Contains(err.Error(), "no data yet") {
		t.Fatalf("no file: %v", err)
	}
	if err := os.WriteFile(devFile, []byte("dev v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "manual" || c.SizeBytes != 6 || !slices.Contains(r.db.Calls, "snapshot movies/dev") {
		t.Fatalf("copy %+v, calls %v", c, r.db.Calls)
	}
	if got := read(t, filepath.Join(r.dir, "backups", "movies", "dev", c.Name, "homelab-db", "app.db")); got != "dev v1" {
		t.Fatalf("copy holds %q", got)
	}
	r.db.SnapshotErr = errors.New("busy")
	r.advance(2 * time.Second)
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); !isUserErr(err) {
		t.Fatalf("failed snapshot: %v", err)
	}
	if got := r.hub.dbCopies("movies", core.Dev); len(got) != 1 {
		t.Fatalf("a failed snapshot left %v", got)
	}
	r.db.SnapshotErr = nil

	// Restore puts the file back, with a copy of what was there.
	if err := os.WriteFile(devFile, []byte("dev v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.advance(2 * time.Second)
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, c.Name, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := read(t, devFile); got != "dev v1" {
		t.Fatalf("file holds %q", got)
	}
	var safety string
	for _, x := range r.hub.dbCopies("movies", core.Dev) {
		if x.Kind == "before-restore" {
			safety = read(t, filepath.Join(r.dir, "backups", "movies", "dev", x.Name, "homelab-db", "app.db"))
		}
	}
	if safety != "dev v2" {
		t.Fatalf("safety copy holds %q", safety)
	}

	// Copy prod to dev: only the file is replaced, and its journal files go.
	r.promoteDB()
	prodFile := r.dataPath(core.Prod, "homelab-db", "app.db")
	if err := os.WriteFile(prodFile, []byte("prod rows"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devFile+"-wal", []byte("old journal"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.advance(2 * time.Second)
	r.db.Calls = nil
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := read(t, devFile); got != "prod rows" {
		t.Fatalf("dev file holds %q", got)
	}
	if _, err := os.Stat(devFile + "-wal"); err == nil {
		t.Fatal("the old journal file is still there")
	}
	if got := read(t, prodFile); got != "prod rows" || !slices.Contains(r.db.Calls, "snapshot movies/prod") {
		t.Fatalf("prod file %q, calls %v", got, r.db.Calls)
	}
	var before string
	for _, x := range r.hub.dbCopies("movies", core.Dev) {
		if x.Kind == "before-copy-from-prod" {
			before = read(t, filepath.Join(r.dir, "backups", "movies", "dev", x.Name, "homelab-db", "app.db"))
		}
	}
	if before != "dev v1" {
		t.Fatalf("dev's copy holds %q", before)
	}
	if left, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", ".copy-*")); len(left) != 0 {
		t.Fatalf("left over: %v", left)
	}
	if r.running(core.Dev) != "aaaaaaa1" {
		t.Fatal("dev isn't running")
	}

	// Prod with no file yet has nothing to give.
	if err := os.Remove(prodFile); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := read(t, devFile); got != "prod rows" {
		t.Fatalf("dev changed: %q", got)
	}
	if evs := r.events("db.failed"); len(evs) != 2 || !strings.Contains(evs[0], "no data yet") {
		t.Fatalf("db.failed: %v", evs)
	}
}

func TestCopyKind(t *testing.T) {
	for name, want := range map[string]string{
		"20261023-120000-manual":                "manual",
		"20261023-120000-before-restore":        "before-restore",
		"20261023-120000-before-copy-from-prod": "before-copy-from-prod",
		"20261023-120000-bbbbbbb":               "promote",
	} {
		if got, ok := copyKind(name); !ok || got != want {
			t.Errorf("%s = %q %v", name, got, ok)
		}
	}
	for _, name := range []string{"", "x", ".copy-123", "20261023-120000-replaced-by-restore-abc1234", "20261023-120000"} {
		if _, ok := copyKind(name); ok {
			t.Errorf("%q is not a copy", name)
		}
	}
}

// Many changes from the SQL box keep their own copies; they never push out a copy made by hand.
func TestCopiesBeforeChangesDontPushOutOtherCopies(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.promoteDB()
	r.advance(2 * time.Second)
	manual, err := r.hub.MakeDBCopy(ctx, "movies", core.Prod, owner)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < r.hub.cfg.KeepBackups+3; i++ {
		r.advance(2 * time.Second)
		if _, err := r.hub.CopyBeforeChange(ctx, "movies", owner); err != nil {
			t.Fatal(err)
		}
	}
	db, err := r.hub.Database(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	changes, kept := 0, false
	for _, e := range db.Envs {
		if e.Env != core.Prod {
			continue
		}
		for _, c := range e.Copies {
			if c.Kind == "before-change" {
				changes++
			}
			kept = kept || c.Name == manual.Name
		}
	}
	if !kept || changes != r.hub.cfg.KeepBackups {
		t.Fatalf("manual copy kept: %v, copies before changes: %d", kept, changes)
	}
}
