package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// signIn gives a person a real companion session and returns its Bearer token.
func (r *rig) signIn(t *testing.T, personID string) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if err := r.store.PutSession(context.Background(), digest[:], personID, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return sessionPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// secretsApp adds the movies app and people with each kind of access.
func secretsApp(t *testing.T) (*rig, http.Handler) {
	t.Helper()
	r := newRig(t)
	for _, p := range []core.Person{
		{ID: "admin", Name: "Ada", Email: "ada@example.test", Status: "signed_in", Roles: []string{"admin"}},
		{ID: "keeper", Name: "Kay", Email: "kay@example.test", Status: "signed_in", Roles: []string{"admin", "secrets"}},
		{ID: "member", Name: "Mo", Email: "mo@example.test", Status: "signed_in", Roles: []string{"member"}},
	} {
		if err := r.store.SavePerson(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	r.src.Push("rupaai/movies", "main", "aaaaaaa1", "first", []byte(manifestDoc))
	h := r.handler()
	if rec := post(h, "/api/apps", `{"repo":"rupaai/movies"}`, &local); rec.Code != 201 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	return r, h
}

// as sends a request signed in as a person.
func as(h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	return do(h, method, path, body, &core.Viewer{Local: true}, "Authorization", "Bearer "+token,
		"X-Homelab-Request", "1", "Content-Type", "application/json")
}

// secretValueCalls is every endpoint that shows, sets, replaces, regenerates, deletes or
// restores a secret value, or lists its versions.
var secretValueCalls = []struct{ method, path, body string }{
	{"POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal", ""},
	{"PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"changed-value"}`},
	{"DELETE", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", ""},
	{"POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/regenerate", ""},
	{"GET", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/versions", ""},
	{"POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/versions/1/restore", ""},
	{"POST", "/api/settings/secrets/GITHUB_TOKEN/reveal", ""},
	{"PUT", "/api/settings/secrets/GITHUB_TOKEN", `{"value":"changed-value"}`},
	{"DELETE", "/api/settings/secrets/GITHUB_TOKEN", ""},
	{"POST", "/api/apps/movies/envs/prod/saved/google_token:u1/reveal", ""},
	{"DELETE", "/api/apps/movies/envs/prod/saved/google_token:u1", ""},
	{"GET", "/api/settings/secrets/GITHUB_TOKEN/versions", ""},
	{"POST", "/api/settings/secrets/GITHUB_TOKEN/versions/1/restore", ""},
}

func TestAPIKeysNeverReachSecretValues(t *testing.T) {
	r, h := secretsApp(t)
	owner := r.signIn(t, "owner")
	if rec := as(h, owner, "PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"tmdb-value-123"}`); rec.Code != 204 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, owner, "PUT", "/api/settings/secrets/GITHUB_TOKEN", `{"value":"ghp_value_123"}`); rec.Code != 204 {
		t.Fatalf("set setting: %d %s", rec.Code, rec.Body)
	}
	every, _ := r.adminKey(t, hub.ScopeUsersRead)
	appTok, _ := r.appKey(t, "movies", "prod")
	door := Door(r.h, Options{Version: "test", Static: testSite(), Store: r.store})

	for name, token := range map[string]string{"admin key with every scope": every, "app key": appTok} {
		for _, c := range secretValueCalls {
			// On the companion's address, and for app keys on the app door as well.
			handlers := map[string]http.Handler{"companion": h}
			if token == appTok {
				handlers["door"] = door
			}
			for where, handler := range handlers {
				rec := do(handler, c.method, c.path, c.body, &core.Viewer{Local: true}, "Authorization", "Bearer "+token,
					"X-Homelab-Request", "1", "Content-Type", "application/json")
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s on %s: %s %s = %d, want 403: %s", name, where, c.method, c.path, rec.Code, rec.Body)
				}
				if strings.Contains(rec.Body.String(), "tmdb-value-123") || strings.Contains(rec.Body.String(), "ghp_value_123") {
					t.Errorf("%s: %s leaked a value", name, c.path)
				}
			}
		}
	}
	// A key holder who is also a person with a session cookie or headers gains nothing.
	rec := do(h, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal", "", &core.Viewer{Local: true, PersonID: "owner"},
		"Authorization", "Bearer "+every, "X-Homelab-Request", "1", "Content-Type", "application/json")
	if rec.Code != http.StatusForbidden {
		t.Errorf("key with a viewer already set: %d %s", rec.Code, rec.Body)
	}
	// Nothing changed.
	if rec := as(h, owner, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "tmdb-value-123") {
		t.Fatalf("value after the key attempts: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, owner, "POST", "/api/settings/secrets/GITHUB_TOKEN/reveal", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ghp_value_123") {
		t.Fatalf("setting after the key attempts: %d %s", rec.Code, rec.Body)
	}
}

func TestSecretValueCallsNeedASession(t *testing.T) {
	_, h := secretsApp(t)
	for _, c := range secretValueCalls {
		// A viewer that names a person but has no sign-in behind it, and nobody at all.
		for _, v := range []*core.Viewer{{Local: true, PersonID: "owner"}, {Local: true}, nil} {
			rec := do(h, c.method, c.path, c.body, v, "X-Homelab-Request", "1", "Content-Type", "application/json")
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with viewer %+v = %d, want 403", c.method, c.path, v, rec.Code)
			}
		}
	}
}

func TestOwnerUsesSecretValuesThroughTheAPI(t *testing.T) {
	r, h := secretsApp(t)
	owner := r.signIn(t, "owner")
	for _, v := range []string{"first-value", "second-value"} {
		if rec := as(h, owner, "PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"`+v+`"}`); rec.Code != 204 {
			t.Fatalf("set: %d %s", rec.Code, rec.Body)
		}
	}
	reveal := func(path string) string {
		t.Helper()
		rec := as(h, owner, "POST", path, "")
		var got struct{ Value string }
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("reveal %s: %d %s", path, rec.Code, rec.Body)
		}
		return got.Value
	}
	if v := reveal("/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal"); v != "second-value" {
		t.Fatalf("reveal = %q", v)
	}

	rec := as(h, owner, "GET", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/versions", "")
	var vs struct {
		Versions []struct {
			Version int
			SetBy   string
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &vs) != nil || len(vs.Versions) != 2 || vs.Versions[0].Version != 2 || vs.Versions[1].SetBy == "" {
		t.Fatalf("versions: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "-value") {
		t.Fatalf("versions hold values: %s", rec.Body)
	}
	if rec := as(h, owner, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/versions/1/restore", ""); rec.Code != 204 {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if v := reveal("/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal"); v != "first-value" {
		t.Fatalf("after restore = %q", v)
	}
	if rec := as(h, owner, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/versions/x/restore", ""); rec.Code != 400 {
		t.Fatalf("bad version: %d", rec.Code)
	}
	if rec := as(h, owner, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/versions/9/restore", ""); rec.Code != 400 {
		t.Fatalf("missing version: %d", rec.Code)
	}

	// Platform credentials.
	if rec := as(h, owner, "PUT", "/api/settings/secrets/GITHUB_TOKEN", `{"value":"ghp_value_123"}`); rec.Code != 204 {
		t.Fatalf("set setting: %d %s", rec.Code, rec.Body)
	}
	if v := reveal("/api/settings/secrets/GITHUB_TOKEN/reveal"); v != "ghp_value_123" {
		t.Fatalf("setting reveal = %q", v)
	}
	if rec := as(h, owner, "GET", "/api/settings/secrets/GITHUB_TOKEN/versions", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("setting versions: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, owner, "POST", "/api/settings/secrets/GITHUB_TOKEN/versions/1/restore", ""); rec.Code != 204 {
		t.Fatalf("setting restore: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, owner, "DELETE", "/api/settings/secrets/GITHUB_TOKEN", ""); rec.Code != 204 {
		t.Fatalf("delete setting: %d", rec.Code)
	}
	if rec := as(h, owner, "DELETE", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}

	// The list never has values, and says whether this person may see them.
	as(h, owner, "PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"listed-value-9"}`)
	rec = as(h, owner, "GET", "/api/apps/movies/secrets", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "listed-value-9") || strings.Contains(rec.Body.String(), `"value"`) ||
		!strings.Contains(rec.Body.String(), `"canSeeValues":true`) || !strings.Contains(rec.Body.String(), `"set":true`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, owner, "GET", "/api/me", ""); !strings.Contains(rec.Body.String(), `"canSeeSecrets":true`) {
		t.Fatalf("me: %s", rec.Body)
	}
}

func TestAdminWhoIsNotOwnerIsRefusedSecretValues(t *testing.T) {
	r, h := secretsApp(t)
	owner := r.signIn(t, "owner")
	as(h, owner, "PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"tmdb-value-123"}`)
	as(h, owner, "PUT", "/api/settings/secrets/GITHUB_TOKEN", `{"value":"ghp_value_123"}`)
	for _, who := range []string{"admin", "member"} {
		tok := r.signIn(t, who)
		for _, c := range secretValueCalls {
			rec := as(h, tok, c.method, c.path, c.body)
			if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "-value") {
				t.Errorf("%s: %s %s = %d: %s", who, c.method, c.path, rec.Code, rec.Body)
			}
		}
	}
	// An admin still sees the names and whether each is set, with no values.
	rec := as(h, r.signIn(t, "admin"), "GET", "/api/apps/movies/secrets", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "tmdb-value-123") || !strings.Contains(rec.Body.String(), `"canSeeValues":false`) ||
		!strings.Contains(rec.Body.String(), `"set":true`) {
		t.Fatalf("admin list: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, r.signIn(t, "admin"), "GET", "/api/settings/secrets", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "ghp_") {
		t.Fatalf("admin settings: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, r.signIn(t, "admin"), "GET", "/api/me", ""); !strings.Contains(rec.Body.String(), `"canSeeSecrets":false`) {
		t.Fatalf("me: %s", rec.Body)
	}
	// Nothing changed.
	if rec := as(h, owner, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal", ""); !strings.Contains(rec.Body.String(), "tmdb-value-123") {
		t.Fatalf("value changed: %s", rec.Body)
	}
}

func TestSecretsRolePersonUsesSecretValues(t *testing.T) {
	r, h := secretsApp(t)
	tok := r.signIn(t, "keeper")
	if rec := as(h, tok, "PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"keeper-value-1"}`); rec.Code != 204 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, tok, "POST", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "keeper-value-1") {
		t.Fatalf("reveal: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, tok, "GET", "/api/apps/movies/secrets", ""); !strings.Contains(rec.Body.String(), `"canSeeValues":true`) {
		t.Fatalf("list: %s", rec.Body)
	}
	if rec := as(h, tok, "POST", "/api/settings/secrets/GITHUB_TOKEN/reveal", ""); rec.Code != 400 {
		t.Fatalf("reveal of an unset setting: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, tok, "DELETE", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
}

func TestOnlyOwnerGivesTheSecretsRoleThroughTheAPI(t *testing.T) {
	r, h := secretsApp(t)
	body := `{"name":"Mo","email":"mo@example.test","roles":["member","secrets"],"grants":{}}`
	if rec := as(h, r.signIn(t, "admin"), "PUT", "/api/people/member", body); rec.Code != 400 || !strings.Contains(rec.Body.String(), "Only the owner") {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
	invite := `{"name":"New","email":"new@example.test","roles":["member","secrets"],"grants":{}}`
	if rec := as(h, r.signIn(t, "admin"), "POST", "/api/people", invite); rec.Code != 400 {
		t.Fatalf("admin invite: %d %s", rec.Code, rec.Body)
	}
	// "keeper" has the role but isn't the owner, so can't pass it on.
	if rec := as(h, r.signIn(t, "keeper"), "PUT", "/api/people/member", body); rec.Code != 400 {
		t.Fatalf("keeper: %d %s", rec.Code, rec.Body)
	}
	if rec := as(h, r.signIn(t, "owner"), "PUT", "/api/people/member", body); rec.Code != 200 {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
	p, _ := r.store.Person(context.Background(), "member")
	if !hasRole(p, "secrets") {
		t.Fatalf("role not stored: %+v", p)
	}
	if rec := as(h, r.signIn(t, "member"), "GET", "/api/me", ""); !strings.Contains(rec.Body.String(), `"canSeeSecrets":true`) {
		t.Fatalf("me: %s", rec.Body)
	}
}
