package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
)

// adoptDoc keeps the sqlite file in the app's own data folder (db, seen as /data).
const adoptDoc = manifestDoc + "database: {engine: sqlite, path: /data/catalogue_full.db}\n"

func (r *rig) write(path, content string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func TestAdoptedSQLiteSpec(t *testing.T) {
	r := newRig(t)
	r.dbApp(adoptDoc)
	spec := r.dep.Specs["movies/dev"]
	if spec.Vars["DATABASE_URL"] != "sqlite:////data/catalogue_full.db" || spec.Vars["DATABASE_PATH"] != "/data/catalogue_full.db" {
		t.Fatalf("vars %v", spec.Vars)
	}
	// The app's own data folder is the only mount: no homelab-db.
	if want := []core.Mount{{HostPath: r.dataPath(core.Dev, "db"), ContainerPath: "/data"}}; !slices.Equal(spec.Mounts, want) {
		t.Fatalf("mounts %+v", spec.Mounts)
	}
	if _, err := os.Stat(r.dataPath(core.Dev, "homelab-db")); err == nil {
		t.Fatal("a homelab-db folder was made")
	}
	if got := r.db.Registered["movies/dev"]; got != (core.DatabaseRegistration{Engine: "sqlite", Path: r.dataPath(core.Dev, "db", "catalogue_full.db")}) {
		t.Fatalf("registered %+v", got)
	}
	// The app may set DATABASE_PATH to the same path itself.
	r2 := newRig(t)
	r2.dbApp(strings.Replace(adoptDoc, "secrets:", "env: {DATABASE_PATH: /data/catalogue_full.db}\nsecrets:", 1))
	if r2.dep.Specs["movies/dev"].Vars["DATABASE_PATH"] != "/data/catalogue_full.db" {
		t.Fatal("DATABASE_PATH")
	}
}

func TestAdoptedSQLiteCopies(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(adoptDoc)
	devFile := r.dataPath(core.Dev, "db", "catalogue_full.db")
	other := r.dataPath(core.Dev, "db", "poster.jpg")
	r.write(other, "poster")
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); !isUserErr(err) || !strings.Contains(err.Error(), "no data yet") {
		t.Fatalf("no file: %v", err)
	}
	r.write(devFile, "dev v1")
	c, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner)
	if err != nil {
		t.Fatal(err)
	}
	// The copy is named app.db whatever the file is called, and holds the file only.
	copyDir := filepath.Join(r.dir, "backups", "movies", "dev", c.Name)
	if c.SizeBytes != 6 || read(t, filepath.Join(copyDir, "homelab-db", "app.db")) != "dev v1" {
		t.Fatalf("copy %+v", c)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "homelab-db", "poster.jpg")); err == nil {
		t.Fatal("the copy holds the folder's other files")
	}

	// Restore puts the file back and leaves the rest of the folder, journal files aside.
	r.write(devFile, "dev v2")
	r.write(devFile+"-wal", "journal")
	r.advance(2 * time.Second)
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, c.Name, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if read(t, devFile) != "dev v1" || read(t, other) != "poster" {
		t.Fatalf("file %q, other %q", read(t, devFile), read(t, other))
	}
	if _, err := os.Stat(devFile + "-wal"); err == nil {
		t.Fatal("the old journal file is still there")
	}
	var safety string
	for _, x := range r.hub.dbCopies("movies", core.Dev) {
		if x.Kind == "before-restore" {
			safety = read(t, filepath.Join(r.dir, "backups", "movies", "dev", x.Name, "homelab-db", "app.db"))
		}
	}
	if safety != "dev v2" || r.running(core.Dev) != "aaaaaaa1" {
		t.Fatalf("safety copy %q, running %q", safety, r.running(core.Dev))
	}

	// Copy prod to dev.
	r.promoteDB()
	prodFile := r.dataPath(core.Prod, "db", "catalogue_full.db")
	r.write(prodFile, "prod rows")
	r.advance(2 * time.Second)
	r.db.Calls = nil
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if read(t, devFile) != "prod rows" || read(t, other) != "poster" || !slices.Contains(r.db.Calls, "snapshot movies/prod") {
		t.Fatalf("dev file %q, calls %v", read(t, devFile), r.db.Calls)
	}
	if evs := r.events("db.failed"); len(evs) != 0 {
		t.Fatalf("db.failed: %v", evs)
	}
}

// The file is in the data folder, so the copy made before a promote has it already.
func TestAdoptedSQLitePromoteCopyIsTheDataFolder(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(adoptDoc)
	r.promoteDB()
	r.write(r.dataPath(core.Prod, "db", "catalogue_full.db"), "prod rows")
	r.pushWith("bbbbbbb2", adoptDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	dirs := r.copyDirs(core.Prod, "bbbbbbb")
	if len(dirs) != 1 || read(t, filepath.Join(dirs[0], "db", "catalogue_full.db")) != "prod rows" {
		t.Fatalf("copies %v", dirs)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "homelab-db")); err == nil {
		t.Fatal("the promote copy has a homelab-db folder")
	}
}

// importFile sends content to an environment's import and waits for it to finish.
func (r *rig) importFile(env, content string) (string, error) {
	r.t.Helper()
	kind, err := r.hub.ImportDB(context.Background(), "movies", env, strings.NewReader(content), owner)
	r.settle()
	return kind, err
}

func TestImportSQLiteFile(t *testing.T) {
	sqlite := "SQLite format 3\x00 rows from outside"
	for name, c := range map[string]struct{ doc, file string }{
		"own folder": {sqliteDoc, "homelab-db/app.db"},
		"adopted":    {adoptDoc, "db/catalogue_full.db"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.dbApp(c.doc)
			file := filepath.Join(r.dir, "apps", "movies", "dev", "data", filepath.FromSlash(c.file))
			r.write(file, "old rows")
			r.write(file+"-wal", "old journal")
			r.write(filepath.Join(filepath.Dir(file), "keep.txt"), "kept")
			kind, err := r.importFile(core.Dev, sqlite)
			if err != nil || kind != "sqlite" {
				t.Fatalf("import: %q %v", kind, err)
			}
			if read(t, file) != sqlite || read(t, filepath.Join(filepath.Dir(file), "keep.txt")) != "kept" {
				t.Fatalf("file %q", read(t, file))
			}
			if _, err := os.Stat(file + "-wal"); err == nil {
				t.Fatal("the old journal file is still there")
			}
			// What was there is copied first, the upload is gone, and the app runs.
			var before string
			for _, x := range r.hub.dbCopies("movies", core.Dev) {
				if x.Kind == "before-import" {
					before = read(t, filepath.Join(r.dir, "backups", "movies", "dev", x.Name, "homelab-db", "app.db"))
				}
			}
			if before != "old rows" || r.running(core.Dev) != "aaaaaaa1" {
				t.Fatalf("safety copy %q, running %q", before, r.running(core.Dev))
			}
			if left, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", "dev", ".import-*")); len(left) != 0 {
				t.Fatalf("left over: %v", left)
			}
			if len(r.events("db.import_started")) != 1 || len(r.events("db.imported")) != 1 || len(r.events("db.failed")) != 0 {
				t.Fatalf("events: %v %v %v", r.events("db.import_started"), r.events("db.imported"), r.events("db.failed"))
			}
			// The app was stopped around the load.
			stop, start := callIndex(r.dep.Calls, "stop-app movies/dev", 0), callIndex(r.dep.Calls, "start-app movies/dev", 0)
			if stop < 0 || start < stop {
				t.Fatalf("calls %v", r.dep.Calls)
			}
		})
	}
}

func TestImportSQLiteFileWithNoFileYet(t *testing.T) {
	r := newRig(t)
	r.dbApp(adoptDoc)
	if _, err := r.importFile(core.Dev, "SQLite format 3\x00 first"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, r.dataPath(core.Dev, "db", "catalogue_full.db")); !strings.HasPrefix(got, "SQLite format 3") {
		t.Fatalf("file %q", got)
	}
	if len(r.hub.dbCopies("movies", core.Dev)) != 0 {
		t.Fatal("there was nothing to copy first")
	}
}

func TestImportPostgres(t *testing.T) {
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.dep.SetDB("movies", core.Dev, "old rows")

	// A pg_dump custom dump is restored like a copy.
	r.dep.Calls = nil
	kind, err := r.importFile(core.Dev, "PGDMP\x01custom dump")
	if err != nil || kind != "pg_dump" {
		t.Fatalf("custom: %q %v", kind, err)
	}
	if r.dep.DBContent("movies", core.Dev) != "PGDMP\x01custom dump" || !slices.Contains(r.dep.Calls, "restore-db movies/dev") || slices.Contains(r.dep.Calls, "import-sql movies/dev") {
		t.Fatalf("database %q, calls %v", r.dep.DBContent("movies", core.Dev), r.dep.Calls)
	}
	var before string
	for _, x := range r.hub.dbCopies("movies", core.Dev) {
		if x.Kind == "before-import" {
			before = read(t, filepath.Join(r.dir, "backups", "movies", "dev", x.Name, "homelab-db.dump"))
		}
	}
	if before != "old rows" {
		t.Fatalf("safety copy holds %q", before)
	}

	// Plain SQL is run with psql.
	r.advance(2 * time.Second)
	r.dep.Calls = nil
	sql := "CREATE TABLE notes (id int);\nINSERT INTO notes VALUES (1);\n"
	kind, err = r.importFile(core.Dev, sql)
	if err != nil || kind != "sql" {
		t.Fatalf("sql: %q %v", kind, err)
	}
	if r.dep.DBContent("movies", core.Dev) != sql || !slices.Contains(r.dep.Calls, "import-sql movies/dev") {
		t.Fatalf("database %q, calls %v", r.dep.DBContent("movies", core.Dev), r.dep.Calls)
	}
	if len(r.events("db.imported")) != 2 || r.running(core.Dev) != "aaaaaaa1" {
		t.Fatalf("imported %v, running %q", r.events("db.imported"), r.running(core.Dev))
	}
}

func TestImportRefusesTheWrongKind(t *testing.T) {
	cases := map[string]struct{ doc, content, want string }{
		"sqlite into postgres": {postgresDoc, "SQLite format 3\x00 x", "SQLite file, but Movie Night uses Postgres"},
		"binary into postgres": {postgresDoc, "\x00\x01\x02 not a dump", "doesn't know that kind of file"},
		"dump into sqlite":     {sqliteDoc, "PGDMP\x01x", "uses SQLite, so send a SQLite database file"},
		"sql into sqlite":      {adoptDoc, "SELECT 1;\n", "uses SQLite, so send a SQLite database file"},
		"empty":                {postgresDoc, "", "empty"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.dbApp(c.doc)
			r.dep.SetDB("movies", core.Dev, "old rows")
			r.dep.Calls = nil
			_, err := r.importFile(core.Dev, c.content)
			if !isUserErr(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			// Nothing was touched: no stop, no copy, no upload left behind.
			if slices.Contains(r.dep.Calls, "stop-app movies/dev") || len(r.hub.dbCopies("movies", core.Dev)) != 0 || len(r.events("db.import_started")) != 0 {
				t.Fatalf("calls %v", r.dep.Calls)
			}
			if left, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", "dev", ".import-*")); len(left) != 0 {
				t.Fatalf("left over: %v", left)
			}
			if r.dep.DBContent("movies", core.Dev) != "old rows" {
				t.Fatal("the database changed")
			}
		})
	}
}

func TestImportFailurePutsTheSafetyCopyBack(t *testing.T) {
	t.Run("plain sql", func(t *testing.T) {
		r := newRig(t)
		r.dbApp(postgresDoc)
		r.dep.SetDB("movies", core.Dev, "old rows")
		r.dep.ImportErr = errors.New("ERROR: syntax error at line 3")
		if _, err := r.importFile(core.Dev, "CREATE TABL oops;\n"); err != nil {
			t.Fatal(err)
		}
		if got := r.dep.DBContent("movies", core.Dev); got != "old rows" {
			t.Fatalf("database holds %q, want what it held before", got)
		}
		if evs := r.events("db.failed"); len(evs) != 1 || !strings.Contains(evs[0], "syntax error") || !strings.Contains(evs[0], "put back as it was") {
			t.Fatalf("db.failed: %v", evs)
		}
		if len(r.events("db.imported")) != 0 || r.running(core.Dev) != "aaaaaaa1" {
			t.Fatalf("imported %v, running %q", r.events("db.imported"), r.running(core.Dev))
		}
	})
	t.Run("custom dump", func(t *testing.T) {
		r := newRig(t)
		r.dbApp(postgresDoc)
		r.dep.SetDB("movies", core.Dev, "old rows")
		r.dep.RestoreErrs = []error{errors.New("pg_restore failed")}
		if _, err := r.importFile(core.Dev, "PGDMP\x01broken"); err != nil {
			t.Fatal(err)
		}
		if got := r.dep.DBContent("movies", core.Dev); got != "old rows" {
			t.Fatalf("database holds %q", got)
		}
		if evs := r.events("db.failed"); len(evs) != 1 || !strings.Contains(evs[0], "pg_restore failed") {
			t.Fatalf("db.failed: %v", evs)
		}
	})
	for name, doc := range map[string]string{"sqlite own folder": sqliteDoc, "sqlite adopted": adoptDoc} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.dbApp(doc)
			_, folder, rel, _ := mustManifest(t, r, doc).SQLiteFile()
			file := r.dataPath(core.Dev, folder, rel)
			r.write(file, "old rows")
			r.dep.CopyErr = func(from, _ string) error {
				if strings.Contains(from, ".import-") {
					return errors.New("disk full")
				}
				return nil
			}
			if _, err := r.importFile(core.Dev, "SQLite format 3\x00 new"); err != nil {
				t.Fatal(err)
			}
			if read(t, file) != "old rows" {
				t.Fatalf("file holds %q", read(t, file))
			}
			if evs := r.events("db.failed"); len(evs) != 1 || !strings.Contains(evs[0], "disk full") {
				t.Fatalf("db.failed: %v", evs)
			}
			if r.running(core.Dev) != "aaaaaaa1" {
				t.Fatal("the app didn't start again")
			}
		})
	}
	t.Run("sqlite with no file before", func(t *testing.T) {
		r := newRig(t)
		r.dbApp(adoptDoc)
		r.dep.CopyErr = func(from, to string) error {
			if strings.Contains(from, ".import-") {
				r.write(to, "half a file")
				return errors.New("disk full")
			}
			return nil
		}
		if _, err := r.importFile(core.Dev, "SQLite format 3\x00 new"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(r.dataPath(core.Dev, "db", "catalogue_full.db")); err == nil {
			t.Fatal("half a file was left")
		}
		if evs := r.events("db.failed"); len(evs) != 1 || !strings.Contains(evs[0], "put back as it was") {
			t.Fatalf("db.failed: %v", evs)
		}
	})
}

func TestImportNeedsADeployedDatabaseAndASmallEnoughFile(t *testing.T) {
	r := newRig(t)
	r.dbApp(postgresDoc)
	// Prod isn't deployed yet.
	if _, err := r.importFile(core.Prod, "SELECT 1;"); !isUserErr(err) || !strings.Contains(err.Error(), "Nothing is deployed") {
		t.Fatalf("prod: %v", err)
	}
	old := importLimit
	importLimit = 10
	defer func() { importLimit = old }()
	if _, err := r.importFile(core.Dev, "SELECT 1; SELECT 2;"); !isUserErr(err) || !strings.Contains(err.Error(), "bigger than") {
		t.Fatalf("too big: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", "dev", ".import-*")); len(left) != 0 {
		t.Fatalf("left over: %v", left)
	}
	// A busy app is refused, and an upload that breaks changes nothing.
	importLimit = old
	l := r.hub.appLock("movies")
	l.Lock()
	if _, err := r.hub.ImportDB(context.Background(), "movies", core.Dev, strings.NewReader("SELECT 1;"), owner); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	l.Unlock()
	broken := &breaks{strings.NewReader("SELECT 1;")}
	if _, err := r.hub.ImportDB(context.Background(), "movies", core.Dev, broken, owner); !isUserErr(err) || !strings.Contains(err.Error(), "didn't finish") {
		t.Fatalf("broken upload: %v", err)
	}
	if len(r.events("db.import_started")) != 0 || bytes.Contains([]byte(r.dep.DBContent("movies", core.Dev)), []byte("SELECT")) {
		t.Fatal("a refused import changed something")
	}
}

// breaks is an upload that ends with an error.
type breaks struct{ r io.Reader }

func (b *breaks) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		err = errors.New("connection reset")
	}
	return n, err
}

func mustManifest(t *testing.T, r *rig, doc string) *manifest.Manifest {
	t.Helper()
	m, err := r.hub.parse([]byte(doc), repo)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
