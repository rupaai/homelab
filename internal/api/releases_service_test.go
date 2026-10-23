package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/hub"
)

func TestReleasesAPIIsForAnAppsOwnKeyOnly(t *testing.T) {
	r := newRigWith(t, func(a *hub.Adapters) { a.Versions = fake.NewVersions() })
	r.sec.PlatformValues["GITHUB_TOKEN"] = "ghp_secret_token_value"
	r.people(t)
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	h := New(r.h, opts)
	door := Door(r.h, opts)

	every, _ := r.adminKey(t, "users:read", "files:read", "files:write")
	appTok, ak := r.appKeyScopes(t, "movies", "dev", "users:read", "releases:read")

	for _, path := range []string{"/api/releases", "/api/releases?limit=5", "/api/releases/x"} {
		if rec := keyReq(h, "GET", path, every, "", &local); rec.Code != http.StatusForbidden {
			t.Errorf("admin key: %s = %d", path, rec.Code)
		}
		if rec := keyReq(door, "GET", path, every, "", nil); rec.Code != http.StatusForbidden {
			t.Errorf("admin key on the door: %s = %d", path, rec.Code)
		}
		if rec := keyReq(h, "GET", path, appTok, "", &local); rec.Code != http.StatusForbidden {
			t.Errorf("app key on the companion: %s = %d", path, rec.Code)
		}
		if rec := do(h, "GET", path, "", &local); rec.Code != http.StatusForbidden {
			t.Errorf("person: %s = %d", path, rec.Code)
		}
		if rec := do(h, "GET", path, "", nil); rec.Code == http.StatusTeapot {
			t.Errorf("nobody: %s reached the services", path)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("something was forwarded: %+v", f.calls)
	}

	rec := keyReq(door, "GET", "/api/releases?limit=5", appTok, "", nil)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("app key: %d %s", rec.Code, rec.Body)
	}
	got := f.calls[0]
	if got.Kind != "app" || got.App != "movies" || got.Env != "dev" || got.Key != ak.ID || strings.Join(got.Scopes, ",") != "users:read,releases:read" {
		t.Errorf("forwarded as %+v", got)
	}
	if f.paths[0] != "GET /api/releases?limit=5" {
		t.Errorf("forwarded %q", f.paths[0])
	}
	// Homelab adds the app's repo and the token; the app never has to know either.
	if x := f.extra[0]; x.Get("X-Homelab-Repo") != "a/movies" || x.Get("X-Homelab-Github-Token") != "ghp_secret_token_value" {
		t.Errorf("headers added: %v", x)
	}
	// What the app sends itself is dropped before the services see it.
	f.calls, f.paths, f.extra = nil, nil, nil
	keyReq(door, "GET", "/api/releases", appTok, "", nil, "X-Homelab-Repo", "evil/repo", "X-Homelab-Github-Token", "mine")
	if x := f.extra[0]; x.Get("X-Homelab-Repo") != "a/movies" || x.Get("X-Homelab-Github-Token") != "ghp_secret_token_value" {
		t.Errorf("the app's own headers got through: %v", x)
	}
}

func TestReleasesAPIWithoutAGitHubTokenIs503(t *testing.T) {
	r := newRigWith(t, func(a *hub.Adapters) { a.Versions = fake.NewVersions() })
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	door := Door(r.h, opts)
	appTok, _ := r.appKeyScopes(t, "movies", "dev", "users:read", "releases:read")
	rec := keyReq(door, "GET", "/api/releases", appTok, "", nil)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "ghp_") || len(f.calls) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
