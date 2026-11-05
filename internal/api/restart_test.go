package api

import (
	"context"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
	"github.com/rupaai/homelab/internal/state"
)

func TestRestartHomelabRoute(t *testing.T) {
	up := &fake.Updater{State: core.UpdateStatus{Managed: true, Current: "build-1"}}
	r := newRigWith(t, func(ad *hub.Adapters) { ad.Updater = up })
	if err := r.store.SavePerson(context.Background(), core.Person{ID: "m", Name: "M", Status: "signed_in", Roles: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	m := &core.Viewer{Local: true, PersonID: "m"}
	if rec := post(h, "/api/device/restart", "", m); rec.Code != 403 {
		t.Fatalf("member restart = %d", rec.Code)
	}
	// Without the header a cross-site page can't make this call.
	if rec := do(h, "POST", "/api/device/restart", "", &local); rec.Code != 403 {
		t.Fatalf("restart without the header = %d", rec.Code)
	}
	// Not under systemd: a plain refusal.
	if rec := post(h, "/api/device/restart", "", &local); rec.Code == 202 || !strings.Contains(rec.Body.String(), "can't restart") {
		t.Fatalf("restart without systemd = %d %s", rec.Code, rec.Body)
	}
	if up.Restarted != 0 {
		t.Fatal("restarted without systemd")
	}
	up.Restartable = true
	if rec := do(h, "GET", "/api/device", "", &local); !strings.Contains(rec.Body.String(), `"canRestart":true`) {
		t.Fatalf("device = %s", rec.Body)
	}
	if rec := post(h, "/api/device/restart", "", &local); rec.Code != 202 {
		t.Fatalf("restart = %d %s", rec.Code, rec.Body)
	}
	if up.Restarted != 1 {
		t.Fatalf("restarted %d times", up.Restarted)
	}
}

func TestRestartEnvRoute(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.AddApp(ctx, state.App{ID: "movies", Name: "Movies", Repo: "a/b", Manifest: []byte("id: movies"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SavePerson(ctx, core.Person{ID: "b", Name: "B", Status: "signed_in", Roles: []string{"builder"}, Grants: map[string]string{"movies": "build"}}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	b := &core.Viewer{Local: true, PersonID: "b"}
	path := "/api/apps/movies/envs/dev/restart"
	if rec := post(h, path, "", b); rec.Code != 403 {
		t.Fatalf("builder restart = %d", rec.Code)
	}
	if rec := do(h, "POST", path, "", &local); rec.Code != 403 {
		t.Fatalf("restart without the header = %d", rec.Code)
	}
	// Nothing runs on dev yet: a plain refusal, not a crash.
	rec := post(h, path, "", &local)
	if rec.Code == 202 || rec.Code >= 500 || !strings.Contains(rec.Body.String(), "Nothing is running") {
		t.Fatalf("restart with nothing running = %d %s", rec.Code, rec.Body)
	}
}

func TestEnvOffRoutes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.AddApp(ctx, state.App{ID: "movies", Name: "Movies", Repo: "a/b", Manifest: []byte("id: movies"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SavePerson(ctx, core.Person{ID: "b", Name: "B", Status: "signed_in", Roles: []string{"builder"}, Grants: map[string]string{"movies": "build"}}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	b := &core.Viewer{Local: true, PersonID: "b"}
	off, on := "/api/apps/movies/envs/dev/off", "/api/apps/movies/envs/dev/on"
	// Admins only.
	for _, path := range []string{off, on} {
		if rec := post(h, path, "", b); rec.Code != 403 {
			t.Fatalf("builder %s = %d", path, rec.Code)
		}
		if rec := do(h, "POST", path, "", &local); rec.Code != 403 {
			t.Fatalf("%s without the header = %d", path, rec.Code)
		}
	}
	if e, _ := r.store.Env(ctx, "movies", core.Dev); e.Off {
		t.Fatal("refused calls turned dev off")
	}
	// Only dev: prod is a plain 400, and so is an odd environment.
	for _, path := range []string{"/api/apps/movies/envs/prod/off", "/api/apps/movies/envs/staging/off"} {
		rec := post(h, path, "", &local)
		if rec.Code != 400 {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := post(h, "/api/apps/movies/envs/prod/off", "", &local); !strings.Contains(rec.Body.String(), "Only dev") {
		t.Fatalf("prod off says %s", rec.Body)
	}
	if rec := post(h, "/api/apps/nope/envs/dev/off", "", &local); rec.Code != 404 {
		t.Fatalf("unknown app = %d", rec.Code)
	}
	// Off, then the app shows it, then on.
	if rec := post(h, off, "", &local); rec.Code != 202 {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/apps/movies", "", &local); !strings.Contains(rec.Body.String(), `"off":true`) {
		t.Fatalf("app = %s", rec.Body)
	}
	if rec := post(h, "/api/apps/movies/envs/dev/restart", "", &local); rec.Code != 400 || !strings.Contains(rec.Body.String(), "Dev is off. Turn it on first.") {
		t.Fatalf("restart while off = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, on, "", &local); rec.Code != 202 {
		t.Fatalf("on = %d %s", rec.Code, rec.Body)
	}
	if e, _ := r.store.Env(ctx, "movies", core.Dev); e.Off {
		t.Fatal("dev is still off")
	}
}
