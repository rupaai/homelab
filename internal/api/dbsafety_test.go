package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// bodyServices records what Homelab forwards, body included.
type bodyServices struct {
	fakeServices
	bodies []string
}

func (f *bodyServices) Forward(w http.ResponseWriter, r *http.Request, c core.ServiceCaller) {
	b, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, string(b))
	f.fakeServices.Forward(w, r, c)
}

// prodApp deploys the postgres movies app to dev and prod.
func (r *rig) prodApp(t *testing.T) {
	t.Helper()
	r.dbApp(t)
	if rec := do(r.handler(), "PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"k"}`, &local, "X-Homelab-Request", "1", "Content-Type", "application/json"); rec.Code != 204 {
		t.Fatalf("secret: %d %s", rec.Code, rec.Body)
	}
	r.h.CheckHealth(context.Background())
	if rec := post(r.handler(), "/api/apps/movies/promote", ``, &local); rec.Code != http.StatusAccepted {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if v, err := r.h.App(context.Background(), "movies"); err == nil && v.Envs[1].Version != nil && v.Envs[1].Busy == "" {
			return
		}
	}
	t.Fatal("prod wasn't deployed")
}

// dbCopies lists an environment's database copies.
func (r *rig) dbCopies(t *testing.T, env string) []hub.DBCopyView {
	t.Helper()
	v, err := r.h.Database(context.Background(), "movies")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range v.Envs {
		if e.Env == env {
			return e.Copies
		}
	}
	return nil
}

func TestProdWriteMakesACopyFirst(t *testing.T) {
	r := newRig(t)
	r.prodApp(t)
	f := &bodyServices{}
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, Services: f})
	r.dep.SetDB("movies", core.Prod, "prod rows")
	send := func(path, body string) {
		t.Helper()
		if rec := do(h, "POST", path, body, &local, "X-Homelab-Request", "1", "Content-Type", "application/json"); rec.Code != http.StatusTeapot {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	copies := func() int { return len(r.dbCopies(t, core.Prod)) }

	// A prod write: copied first, then forwarded with copy= and the body as it was.
	body := `{"sql":"update notes set title='x' where id=1","write":true,"confirm":"prod"}`
	send("/api/db/movies/prod/query", body)
	if copies() != 1 {
		t.Fatalf("copies: %d", copies())
	}
	name := r.dbCopies(t, core.Prod)[0].Name
	if !strings.HasSuffix(name, "-before-change") {
		t.Fatalf("copy %q", name)
	}
	if got := f.paths[0]; got != "POST /api/db/movies/prod/query?copy="+name {
		t.Fatalf("forwarded %q", got)
	}
	if f.bodies[0] != body {
		t.Fatalf("body %q", f.bodies[0])
	}
	if r.dep.DBContent("movies", core.Prod) != "prod rows" {
		t.Fatal("the database changed")
	}

	// A forged copy= is dropped, on a write and everywhere else in /api/db.
	time.Sleep(1100 * time.Millisecond) // copies are named after the second
	send("/api/db/movies/prod/query?copy=made-up&x=1", `{"sql":"delete from notes where id=1","write":true}`)
	if got := f.paths[1]; strings.Contains(got, "made-up") || !strings.Contains(got, "copy=") || !strings.Contains(got, "x=1") {
		t.Fatalf("forwarded %q", got)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/db/movies/prod/query?copy=made-up"},
		{"POST", "/api/db/movies/dev/query?copy=made-up"},
		{"GET", "/api/db/movies/prod/changes?copy=made-up&limit=5"},
		{"GET", "/api/db?copy=made-up"},
	} {
		n := len(f.paths)
		body := `{"sql":"select 1","write":false}`
		if c.method == "GET" {
			body = ""
		}
		if rec := do(h, c.method, c.path, body, &local, "X-Homelab-Request", "1", "Content-Type", "application/json"); rec.Code != http.StatusTeapot {
			t.Fatalf("%s: %d", c.path, rec.Code)
		}
		if got := f.paths[n]; strings.Contains(got, "copy") || strings.Contains(got, "made-up") {
			t.Errorf("%s %s forwarded as %q", c.method, c.path, got)
		}
	}
	if copies() != 2 {
		t.Fatalf("copies after reads: %d", copies())
	}

	// Dev writes get no copy, and reads none.
	n := len(f.paths)
	send("/api/db/movies/dev/query", `{"sql":"delete from notes","write":true}`)
	send("/api/db/movies/prod/query", `{"sql":"select 1"}`)
	if f.paths[n] != "POST /api/db/movies/dev/query" || f.paths[n+1] != "POST /api/db/movies/prod/query" {
		t.Fatalf("forwarded %v", f.paths[n:])
	}
	if copies() != 2 || len(r.dbCopies(t, core.Dev)) != 0 {
		t.Fatal("a dev write or a read made a copy")
	}
}

func TestProdWriteWithoutACopyIsRefused(t *testing.T) {
	r := newRig(t)
	r.prodApp(t)
	f := &bodyServices{}
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, Services: f})
	r.dep.DumpErr = io.ErrClosedPipe
	rec := do(h, "POST", "/api/db/movies/prod/query", `{"sql":"delete from notes where id=1","write":true}`, &local, "X-Homelab-Request", "1", "Content-Type", "application/json")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Couldn't make a copy first, so nothing was changed.") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(f.paths) != 0 {
		t.Fatalf("forwarded %v", f.paths)
	}
}

func TestDatabaseImportEndpoint(t *testing.T) {
	r := newRig(t)
	r.dbApp(t)
	ctx := context.Background()
	for _, p := range []core.Person{
		{ID: "member", Name: "Mo", Email: "mo@example.test", Status: "signed_in", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}},
		{ID: "builder", Name: "Bo", Email: "bo@example.test", Status: "signed_in", Roles: []string{"builder"}, Grants: map[string]string{"movies": "build"}},
		{ID: "admin", Name: "Ada", Email: "ada@example.test", Status: "signed_in", Roles: []string{"admin"}},
	} {
		if err := r.store.SavePerson(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	h := r.handler()
	const path = "/api/apps/movies/envs/dev/db/import"
	raw := []string{"X-Homelab-Request", "1", "Content-Type", "application/octet-stream"}
	sql := strings.Repeat("-- a long comment line\n", 10000) + "SELECT 1;\n" // bigger than the JSON limit

	// Admins only.
	for _, who := range []*core.Viewer{nil, {PersonID: "member"}, {PersonID: "builder"}, {PersonID: "ghost"}} {
		if rec := do(h, "POST", path, sql, who, raw...); rec.Code != http.StatusForbidden {
			t.Errorf("%v = %d, want 403", who, rec.Code)
		}
	}
	tok, _ := r.adminKey(t, "users:read", "db:read")
	if rec := keyReq(h, "POST", path, tok, "", &local); rec.Code != http.StatusNotFound {
		t.Errorf("key = %d, want 404", rec.Code)
	}
	if len(r.dep.Calls) > 0 && callCount(r.dep.Calls, "import-sql") != 0 {
		t.Fatal("a refused import ran")
	}

	// The companion's header is needed.
	if rec := do(h, "POST", path, sql, &local, "Content-Type", "application/octet-stream"); rec.Code != http.StatusForbidden {
		t.Errorf("without the header: %d", rec.Code)
	}
	// JSON isn't how a file is sent.
	if rec := do(h, "POST", path, sql, &local, "X-Homelab-Request", "1", "Content-Type", "application/json"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("json: %d", rec.Code)
	}
	// octet-stream is for the import only.
	for _, p := range []string{"/api/apps/movies/envs/dev/db/copies", "/api/apps/movies/deploy", "/api/apps/movies/envs/dev/db/import/x"} {
		if rec := do(h, "POST", p, "x", &local, raw...); rec.Code != http.StatusUnsupportedMediaType && rec.Code != http.StatusNotFound {
			t.Errorf("octet-stream to %s: %d", p, rec.Code)
		}
	}
	// A file of the wrong kind is refused in words.
	if rec := do(h, "POST", path, "SQLite format 3\x00 x", &local, raw...); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "uses Postgres") {
		t.Errorf("wrong kind: %d %s", rec.Code, rec.Body)
	}

	// An admin's plain SQL file, over the JSON size limit, is accepted and loaded.
	if rec := do(h, "POST", path, sql, &core.Viewer{PersonID: "admin"}, raw...); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"kind":"sql"`) {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	r.idle(t)
	if r.dep.DBContent("movies", core.Dev) != sql {
		t.Fatal("the database doesn't hold the file")
	}
	rec := do(h, "GET", "/api/activity?app=movies&limit=50", "", &local)
	for _, action := range []string{"db.import_started", "db.imported"} {
		if !strings.Contains(rec.Body.String(), action) {
			t.Errorf("no %s in the activity log", action)
		}
	}
}

func callCount(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
