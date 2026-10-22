package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// The secrets API (/api/secrets) is served by homelab-services. These tests are about what
// Homelab lets through to it: only an app's own key, with that app's scopes.

// secretsAPIPaths is every kind of call the secrets API takes.
var secretsAPICalls = []struct{ method, path, body string }{
	{"GET", "/api/secrets", ""},
	{"GET", "/api/secrets/TMDB_API_KEY", ""},
	{"GET", "/api/secrets/TMDB_API_KEY?version=1", ""},
	{"PUT", "/api/secrets/google_token:u1", `{"value":"v"}`},
	{"DELETE", "/api/secrets/google_token:u1", ""},
	{"GET", "/api/secrets/TMDB_API_KEY/versions", ""},
	{"POST", "/api/secrets", "{}"},
	{"GET", "/api/secrets/a/b/c", ""},
}

func TestSecretsAPIIsForAnAppsOwnKeyOnly(t *testing.T) {
	r := newRig(t)
	r.people(t)
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	h := New(r.h, opts)
	door := Door(r.h, opts)

	every, _ := r.adminKey(t, "users:read", "files:read", "files:write")
	none, _ := r.adminKey(t, "users:read")
	appTok, ak := r.appKeyScopes(t, "movies", "dev", "users:read", "secrets:read", "secrets:write")

	for _, c := range secretsAPICalls {
		// Admin keys, on the companion's address: 403, and nothing is forwarded.
		for name, tok := range map[string]string{"admin key with every scope": every, "admin key": none} {
			rec := keyReq(h, c.method, c.path, tok, c.body, &local)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: %s %s = %d, want 403: %s", name, c.method, c.path, rec.Code, rec.Body)
			}
		}
		// Admin keys are not even for the app door.
		if rec := keyReq(door, c.method, c.path, every, c.body, nil); rec.Code != http.StatusForbidden {
			t.Errorf("admin key on the door: %s %s = %d", c.method, c.path, rec.Code)
		}
		// An app's key on the companion's address.
		if rec := keyReq(h, c.method, c.path, appTok, c.body, &local); rec.Code != http.StatusForbidden {
			t.Errorf("app key on the companion: %s %s = %d", c.method, c.path, rec.Code)
		}
		// A signed-in admin in the companion: the app's secrets API isn't theirs.
		if rec := do(h, c.method, c.path, c.body, &local, "X-Homelab-Request", "1", "Content-Type", "application/json"); rec.Code != http.StatusForbidden {
			t.Errorf("person: %s %s = %d", c.method, c.path, rec.Code)
		}
		// Nobody.
		if rec := do(h, c.method, c.path, c.body, nil); rec.Code == http.StatusTeapot {
			t.Errorf("nobody: %s %s reached the services", c.method, c.path)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("something was forwarded to the services: %+v", f.calls)
	}

	// The app's own key, on the app door, is forwarded as that app with its scopes.
	for _, c := range secretsAPICalls {
		f.calls, f.paths = nil, nil
		rec := keyReq(door, c.method, c.path, appTok, c.body, nil)
		if rec.Code != http.StatusTeapot {
			t.Fatalf("app key: %s %s = %d %s", c.method, c.path, rec.Code, rec.Body)
		}
		got := f.calls[0]
		if got.Kind != "app" || got.App != "movies" || got.Env != "dev" || got.Key != ak.ID || got.Person != "" ||
			strings.Join(got.Scopes, ",") != "users:read,secrets:read,secrets:write" {
			t.Errorf("%s %s forwarded as %+v", c.method, c.path, got)
		}
		if f.paths[0] != c.method+" "+c.path {
			t.Errorf("forwarded %q, want %q", f.paths[0], c.method+" "+c.path)
		}
	}
}

func TestAnAppKeyWithoutSecretsScopesIsForwardedWithoutThem(t *testing.T) {
	r := newRig(t)
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	door := Door(r.h, opts)
	appTok, _ := r.appKey(t, "movies", "dev") // users:read only, as a key from before the secrets API
	if rec := keyReq(door, "GET", "/api/secrets", appTok, "", nil); rec.Code != http.StatusTeapot {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Homelab forwards what the key has. homelab-services is what says no.
	if len(f.calls) != 1 || strings.Contains(strings.Join(f.calls[0].Scopes, ","), "secrets") {
		t.Fatalf("forwarded %+v", f.calls)
	}
}

func TestSecretsAPIWithoutServicesIs503(t *testing.T) {
	r := newRig(t)
	door := Door(r.h, Options{Version: "test", Static: testSite(), Store: r.store})
	appTok, _ := r.appKeyScopes(t, "movies", "dev", "users:read", "secrets:read")
	if rec := keyReq(door, "GET", "/api/secrets", appTok, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestSecretsAPIIsAServicePath(t *testing.T) {
	for _, p := range []string{"/api/secrets", "/api/secrets/x", "/api/secrets/x/versions"} {
		if !isServicePath(p) || !isSecretsAPI(p) {
			t.Errorf("%s is not a service path", p)
		}
	}
	for _, p := range []string{"/api/secretsx", "/api/settings/secrets", "/api/apps/movies/secrets"} {
		if isSecretsAPI(p) {
			t.Errorf("%s is the secrets API", p)
		}
	}
}

// ---- the companion's screen for secrets the app saved ----

func TestOwnerSeesRevealsAndDeletesSavedSecrets(t *testing.T) {
	r, h := secretsApp(t)
	r.sec.Saved["movies/prod/google_token:u1"] = fake.SavedSecret{Value: "tok-very-secret-1", Version: 3, UpdatedAt: time.Now()}
	owner := r.signIn(t, "owner")

	// Names show to every admin, without the values.
	rec := as(h, r.signIn(t, "admin"), "GET", "/api/apps/movies/secrets", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"saved":[{"name":"google_token:u1","version":3`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tok-very-secret-1") {
		t.Fatalf("the list holds a value: %s", rec.Body)
	}
	// Values: the owner only.
	if rec := as(h, r.signIn(t, "admin"), "POST", "/api/apps/movies/envs/prod/saved/google_token:u1/reveal", ""); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "tok-very") {
		t.Fatalf("admin reveal: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, r.signIn(t, "admin"), "DELETE", "/api/apps/movies/envs/prod/saved/google_token:u1", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("admin delete: %d %s", rec.Code, rec.Body)
	}
	rec = as(h, owner, "POST", "/api/apps/movies/envs/prod/saved/google_token:u1/reveal", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":"tok-very-secret-1"`) {
		t.Fatalf("owner reveal: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, r.signIn(t, "keeper"), "POST", "/api/apps/movies/envs/prod/saved/google_token:u1/reveal", ""); rec.Code != 200 {
		t.Fatalf("secrets role reveal: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, owner, "DELETE", "/api/apps/movies/envs/prod/saved/google_token:u1", ""); rec.Code != 204 {
		t.Fatalf("owner delete: %d %s", rec.Code, rec.Body)
	}
	if len(r.sec.Saved) != 0 {
		t.Fatalf("still saved: %v", r.sec.Saved)
	}
	if rec := as(h, owner, "POST", "/api/apps/movies/envs/prod/saved/google_token:u1/reveal", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("reveal after delete: %d", rec.Code)
	}
	// The activity log has the reveal, and no value.
	act := as(h, owner, "GET", "/api/activity?app=movies", "")
	if !strings.Contains(act.Body.String(), "saved by the app") || strings.Contains(act.Body.String(), "tok-very-secret-1") {
		t.Fatalf("activity: %s", act.Body)
	}
}

// ---- the secrets service is down ----

type downSecretsAPI struct{ core.Secrets }

func (downSecretsAPI) Set(string, string, string, string, string) error {
	return core.ErrSecretsUnavailable
}
func (downSecretsAPI) List(string, string) ([]core.SecretInfo, error) {
	return nil, core.ErrSecretsUnavailable
}
func (downSecretsAPI) ListSaved(string, string) ([]core.SecretInfo, error) {
	return nil, core.ErrSecretsUnavailable
}

func TestSecretScreensAnswer503WithAPlainMessageWhenTheServiceIsDown(t *testing.T) {
	r, _ := secretsApp(t)
	owner := r.signIn(t, "owner")
	downHub := hub.New(hub.Config{DataDir: t.TempDir()}, r.store, hub.Adapters{
		Source: r.src, Builder: fake.NewBuilder(), Registry: fake.NewRegistry(), Deployer: fake.NewDeployer(),
		Addresses: fake.NewAddresses(), Health: fake.NewHealth(), Secrets: downSecretsAPI{r.sec}, Identity: r.idp, Email: &fake.Email{},
		Notifier: fake.NewNotifier(), Resources: &fake.Resources{}, System: &fake.System{}, Updater: &fake.Updater{},
	})
	h := New(downHub, Options{Version: "test", Static: testSite(), Store: r.store})
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"x"}`},
		{"GET", "/api/apps/movies/secrets", ""},
	} {
		rec := as(h, owner, c.method, c.path, c.body)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Homelab's secrets service isn't running yet; try again in a minute.") {
			t.Errorf("%s %s = %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}
