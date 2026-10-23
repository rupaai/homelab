package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// dbApp adds the movies app with a postgres database and waits until dev runs.
func (r *rig) dbApp(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	r.src.Push("rupaai/movies", "main", "aaaaaaa1", "first", []byte(manifestDoc+"database: postgres\n"))
	if rec := post(r.handler(), "/api/apps", `{"repo":"rupaai/movies"}`, &local); rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	r.reg.Put("ghcr.io/rupaai/movies", "sha-aaaaaaa1", "sha256:aaaaaaa1")
	r.build.Set("rupaai/movies", core.Build{Commit: "aaaaaaa1", Status: core.BuildPassed})
	r.h.CheckBuilds(ctx)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if v, err := r.h.App(ctx, "movies"); err == nil && v.Envs[0].Version != nil && v.Envs[0].Busy == "" {
			return
		}
	}
	t.Fatal("dev wasn't deployed")
}

// idle waits until dev has no action running.
func (r *rig) idle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if v, err := r.h.App(context.Background(), "movies"); err == nil && v.Envs[0].Busy == "" {
			return
		}
	}
	t.Fatal("dev stayed busy")
}

func TestDatabaseEndpoints(t *testing.T) {
	r := newRig(t)
	r.dbApp(t)
	h := r.handler()
	r.dep.SetDB("movies", core.Dev, "rows v1")

	rec := do(h, "GET", "/api/apps/movies/db", "", &local)
	var v struct {
		Engine string
		Envs   []struct {
			Env      string
			Port     int
			User     string
			Database string
			Copies   []struct {
				Name      string
				Made      time.Time
				SizeBytes int64 `json:"size_bytes"`
				Kind      string
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); rec.Code != http.StatusOK || err != nil || v.Engine != "postgres" || len(v.Envs) != 2 {
		t.Fatalf("db: %d %s", rec.Code, rec.Body)
	}
	if dev := v.Envs[0]; dev.Env != "dev" || dev.Port != 17015 || dev.User != "app" || dev.Database != "app" || len(dev.Copies) != 0 {
		t.Fatalf("dev: %+v", dev)
	}
	if rec := do(h, "GET", "/api/apps/nope/db", "", &local); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown app: %d", rec.Code)
	}

	// Make a copy now.
	rec = post(h, "/api/apps/movies/envs/dev/db/copies", ``, &local)
	var c struct {
		Name      string
		SizeBytes int64 `json:"size_bytes"`
		Kind      string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); rec.Code != http.StatusCreated || err != nil || c.Kind != "manual" || c.SizeBytes != 7 || !strings.HasSuffix(c.Name, "-manual") {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/apps/movies/db", "", &local)
	if !strings.Contains(rec.Body.String(), c.Name) {
		t.Fatalf("the copy isn't listed: %s", rec.Body)
	}
	if rec := post(h, "/api/apps/movies/envs/prod/db/copies", ``, &local); rec.Code != http.StatusBadRequest {
		t.Fatalf("copy of undeployed prod: %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/apps/movies/envs/staging/db/copies", ``, &local); rec.Code != http.StatusBadRequest {
		t.Fatalf("copy of unknown env: %d %s", rec.Code, rec.Body)
	}

	// Restore it.
	r.dep.SetDB("movies", core.Dev, "rows v2")
	time.Sleep(1100 * time.Millisecond) // the safety copy is named after the second
	if rec := post(h, "/api/apps/movies/envs/dev/db/copies/"+c.Name+"/restore", ``, &local); rec.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	r.idle(t)
	if got := r.dep.DBContent("movies", core.Dev); got != "rows v1" {
		t.Fatalf("database holds %q", got)
	}
	if rec := post(h, "/api/apps/movies/envs/dev/db/copies/nope/restore", ``, &local); rec.Code != http.StatusBadRequest {
		t.Fatalf("restore of a copy that isn't there: %d %s", rec.Code, rec.Body)
	}

	// Prod isn't deployed, so there is nothing to copy to dev.
	if rec := post(h, "/api/apps/movies/db/copy-prod-to-dev", ``, &local); rec.Code != http.StatusBadRequest {
		t.Fatalf("copy prod to dev: %d %s", rec.Code, rec.Body)
	}

	// Every action is in the activity log.
	rec = do(h, "GET", "/api/activity?app=movies&limit=50", "", &local)
	for _, action := range []string{"db.copied", "db.restore_started", "db.restored"} {
		if !strings.Contains(rec.Body.String(), action) {
			t.Errorf("no %s in the activity log: %s", action, rec.Body)
		}
	}
}

func TestDatabaseEndpointsAreForAdmins(t *testing.T) {
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
	calls := []struct{ method, path string }{
		{"GET", "/api/apps/movies/db"},
		{"POST", "/api/apps/movies/envs/dev/db/copies"},
		{"POST", "/api/apps/movies/envs/dev/db/copies/20261023-120000-manual/restore"},
		{"POST", "/api/apps/movies/db/copy-prod-to-dev"},
	}
	for _, who := range []*core.Viewer{nil, {PersonID: "member"}, {PersonID: "builder"}, {PersonID: "ghost"}} {
		for _, c := range calls {
			rec := do(h, c.method, c.path, "", who, "X-Homelab-Request", "1", "Content-Type", "application/json")
			if rec.Code != http.StatusForbidden {
				t.Errorf("%v %s %s = %d, want 403", who, c.method, c.path, rec.Code)
			}
		}
	}
	// No copies were made, and an admin may.
	if rec := do(h, "GET", "/api/apps/movies/db", "", &core.Viewer{PersonID: "admin"}); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "-manual") {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/apps/movies/envs/dev/db/copies", ``, &core.Viewer{PersonID: "admin"}); rec.Code != http.StatusCreated {
		t.Fatalf("admin copy: %d %s", rec.Code, rec.Body)
	}
	// Changes want the companion's header.
	if rec := do(h, "POST", "/api/apps/movies/envs/dev/db/copies", "", &local); rec.Code != http.StatusForbidden {
		t.Fatalf("without the header: %d", rec.Code)
	}
	// API keys have no way to these.
	tok, _ := r.adminKey(t, "users:read", "db:read")
	for _, c := range calls {
		if rec := keyReq(h, c.method, c.path, tok, "", &local); rec.Code != http.StatusNotFound {
			t.Errorf("key %s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

// An admin key with db:read is forwarded to homelab-services with that scope.
func TestDBReadScopeReachesTheServices(t *testing.T) {
	r := newRig(t)
	f := &fakeServices{}
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, Services: f})
	tok, k := r.adminKey(t, "db:read")
	if rec := keyReq(h, "GET", "/api/db/movies/dev/tables", tok, "", &local); rec.Code != http.StatusTeapot {
		t.Fatalf("db:read key: %d %s", rec.Code, rec.Body)
	}
	if len(f.calls) != 1 || f.calls[0].Kind != "admin" || f.calls[0].Key != k.ID || len(f.calls[0].Scopes) != 1 || f.calls[0].Scopes[0] != "db:read" {
		t.Fatalf("forwarded %+v", f.calls)
	}
	if f.paths[0] != "GET /api/db/movies/dev/tables" {
		t.Fatalf("paths %v", f.paths)
	}
}
