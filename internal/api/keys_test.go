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
	"github.com/rupaai/homelab/internal/state"
)

// keyReq makes a request that carries (or doesn't) a bearer token, from a given address.
func keyReq(h http.Handler, method, path, token, remote string, v *core.Viewer, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.Host = "127.0.0.1:7080"
	if remote != "" {
		req.RemoteAddr = remote
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if v != nil {
		req = req.WithContext(core.WithViewer(req.Context(), *v))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// adminKey makes an admin key for the rig's owner.
func (r *rig) adminKey(t *testing.T, scopes ...string) (string, state.APIKey) {
	t.Helper()
	tok, k, err := r.h.CreateAdminKey(context.Background(), "svc", scopes, nil, core.Viewer{PersonID: "owner", Login: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return tok, k
}

// appKey stores an app key for an app environment and returns its value. It builds the
// key the way the hub does: "hlk_" + 32 random bytes, only the SHA-256 kept.
func (r *rig) appKey(t *testing.T, app, env string) (string, state.APIKey) {
	t.Helper()
	ctx := context.Background()
	r.ensureApp(t, app)
	b := make([]byte, 32)
	rand.Read(b)
	tok := "hlk_" + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(tok))
	k := state.APIKey{ID: app + "-" + env, Hash: sum[:], Prefix: tok[:8], Name: app + " " + env, Kind: hub.KeyKindApp, App: app, Env: env,
		Scopes: []string{"users:read"}, CreatedAt: time.Now()}
	if err := r.store.PutAPIKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	return tok, k
}

func (r *rig) ensureApp(t *testing.T, app string) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.store.App(ctx, app); err != nil {
		if _, err := r.store.AddApp(ctx, state.App{ID: app, Name: app, Repo: "a/" + app, Manifest: []byte("id: " + app), Promotion: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
}

// people adds the apps their grants name first, so the grants can point at them.
func (r *rig) people(t *testing.T) {
	t.Helper()
	r.ensureApp(t, "movies")
	r.ensureApp(t, "music")
	for _, p := range []core.Person{
		{ID: "member", Name: "Mo", Email: "mo@example.test", Status: "signed_in", Roles: []string{"member"}},
		{ID: "movie-fan", Name: "Fan", Email: "fan@example.test", Status: "signed_in", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}},
		{ID: "music-fan", Name: "Tune", Email: "tune@example.test", Status: "signed_in", Roles: []string{"member"}, Grants: map[string]string{"music": "use"}},
	} {
		if err := r.store.SavePerson(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
}

func ids(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var list []struct{ ID, Access string }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	m := map[string]string{}
	for _, u := range list {
		m[u.ID] = u.Access
	}
	return m
}

func TestAdminKeyReadsUsersWithoutCSRFHeader(t *testing.T) {
	r := newRig(t)
	r.people(t)
	h := r.handler()
	tok, _ := r.adminKey(t, "users:read")
	tailnet := &core.Viewer{Login: "owner@example.test", PersonID: "owner"}
	for _, v := range []*core.Viewer{&local, tailnet} {
		rec := keyReq(h, "GET", "/api/users", tok, "", v)
		if rec.Code != 200 || len(ids(t, rec)) != 4 {
			t.Fatalf("users: %d %s", rec.Code, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("cache-control=%q", rec.Header().Get("Cache-Control"))
		}
		rec = keyReq(h, "GET", "/api/users/me", tok, "", v)
		var me struct {
			Kind   string
			Key    string
			Person struct{ ID string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &me); rec.Code != 200 || err != nil || me.Kind != "key" || me.Key != "svc" || me.Person.ID != "owner" {
			t.Fatalf("me: %d %s", rec.Code, rec.Body)
		}
	}
	// ?app= narrows to an app's people.
	r.appKey(t, "movies", "dev")
	rec := keyReq(h, "GET", "/api/users?app=movies", tok, "", &local)
	if got := ids(t, rec); rec.Code != 200 || len(got) != 2 || got["movie-fan"] != "use" || got["owner"] != "admin" {
		t.Fatalf("movies users: %d %v", rec.Code, got)
	}
	if rec := keyReq(h, "GET", "/api/users?app=ghost", tok, "", &local); rec.Code != 404 && rec.Code != 400 {
		t.Fatalf("unknown app: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminKeyWithoutViewerOrWithWrongKindIsRefused(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	adminTok, _ := r.adminKey(t, "users:read")
	appTok, _ := r.appKey(t, "movies", "dev")
	if rec := keyReq(h, "GET", "/api/users", adminTok, "", nil); rec.Code != 403 {
		t.Fatalf("admin key with no viewer: %d %s", rec.Code, rec.Body)
	}
	for _, v := range []*core.Viewer{nil, &local} {
		if rec := keyReq(h, "GET", "/api/users", appTok, "", v); rec.Code != 403 {
			t.Fatalf("app key on the main handler: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestKeyOnlyReachesKeyRoutes(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	tok, _ := r.adminKey(t, "users:read")
	for _, c := range [][2]string{
		{"GET", "/api/apps"}, {"GET", "/api/keys"}, {"POST", "/api/keys"}, {"DELETE", "/api/keys/x"},
		{"GET", "/api/people"}, {"GET", "/api/activity"}, {"POST", "/api/users"}, {"GET", "/api/users/me/extra"},
	} {
		rec := keyReq(h, c[0], c[1], tok, "", &local)
		if rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s %s with a key: %d %s", c[0], c[1], rec.Code, rec.Body)
		}
	}
	if rec := keyReq(h, "GET", "/api/apps", tok, "", &local); rec.Code != 404 {
		t.Fatalf("GET /api/apps: %d", rec.Code)
	}
	if rec := keyReq(h, "GET", "/api/keys", tok, "", &local); rec.Code != 404 {
		t.Fatalf("GET /api/keys: %d", rec.Code)
	}
}

func TestKeyScopeIsRequiredForUsers(t *testing.T) {
	r := newRig(t)
	door := Door(r.h, Options{Version: "test", Static: testSite(), Store: r.store})
	r.ensureApp(t, "movies")
	tok := "hlk_" + strings.Repeat("S", 43)
	sum := sha256.Sum256([]byte(tok))
	k := state.APIKey{ID: "noscope", Hash: sum[:], Prefix: tok[:8], Name: "movies dev", Kind: hub.KeyKindApp, App: "movies", Env: "dev", CreatedAt: time.Now()}
	if err := r.store.PutAPIKey(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if rec := keyReq(door, "GET", "/api/users", tok, "", nil); rec.Code != 403 {
		t.Fatalf("users without scope: %d %s", rec.Code, rec.Body)
	}
	// Asking who it is needs no scope.
	if rec := keyReq(door, "GET", "/api/users/me", tok, "", nil); rec.Code != 200 {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
}

func TestBadKeyIs401WithChallenge(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	adminTok, k := r.adminKey(t, "users:read")
	for name, tok := range map[string]string{"junk": "nope", "unknown": "hlk_" + strings.Repeat("A", 43), "wrong prefix": "hlx_" + adminTok[4:]} {
		rec := keyReq(h, "GET", "/api/users", tok, "", &local)
		if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: %d %v", name, rec.Code, rec.Header())
		}
		if strings.Contains(rec.Body.String(), tok) {
			t.Errorf("%s: the key was echoed", name)
		}
	}
	// A revoked key reads exactly like an unknown one.
	if err := r.h.RevokeKey(context.Background(), k.ID, local); err != nil {
		t.Fatal(err)
	}
	rec := keyReq(h, "GET", "/api/users", adminTok, "", &local)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("revoked: %d", rec.Code)
	}
}

func TestCookiesAndHomelabHeadersAreIgnoredOnKeyRequests(t *testing.T) {
	r := newRig(t)
	r.people(t)
	tok, _ := r.adminKey(t, "users:read")
	// Swap the key routes for one that records what reaches it.
	s := newServer(r.h, Options{Version: "test", Static: testSite(), Store: r.store})
	var got http.Header
	var caller hub.Caller
	var viewer core.Viewer
	s.keyMux = http.NewServeMux()
	s.keyMux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		caller, _ = callerFrom(req.Context())
		viewer, _ = core.ViewerFrom(req.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	h := s.handler()

	member := core.Viewer{Login: "mo@example.test", PersonID: "member"}
	rec := keyReq(h, "GET", "/api/users/me", tok, "", &member,
		"Cookie", "homelab_session=abc", "X-Homelab-Request", "1", "X-Homelab-Person", "member", "x-homelab-role", "owner", "Accept", "application/json")
	if rec.Code != http.StatusNoContent || got == nil {
		t.Fatalf("the key routes weren't reached: %d %s", rec.Code, rec.Body)
	}
	for name := range got {
		if l := strings.ToLower(name); l == "cookie" || l == "authorization" || strings.HasPrefix(l, "x-homelab-") {
			t.Errorf("header %s reached the key routes", name)
		}
	}
	if got.Get("Accept") != "application/json" {
		t.Errorf("other headers were dropped too: %v", got)
	}
	if caller.PersonID != "owner" || viewer.PersonID != "" || viewer.Login != "" {
		t.Errorf("caller=%+v viewer=%+v: the request kept the listener's person", caller, viewer)
	}

	// A bad key is refused even with headers that look like a person.
	got = nil
	rec = keyReq(h, "GET", "/api/users/me", "hlk_"+strings.Repeat("C", 43), "", &local, "Cookie", "homelab_session=abc", "X-Homelab-Person", "owner")
	if rec.Code != 401 || got != nil {
		t.Fatalf("bad key: %d reached=%v", rec.Code, got != nil)
	}
}

func TestDoor(t *testing.T) {
	r := newRig(t)
	r.people(t)
	door := Door(r.h, Options{Version: "test", Static: testSite(), Store: r.store})
	moviesTok, _ := r.appKey(t, "movies", "dev")
	r.appKey(t, "music", "prod")
	adminTok, _ := r.adminKey(t, "users:read")

	rec := keyReq(door, "GET", "/api/users", "", "", nil)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no bearer: %d %v", rec.Code, rec.Header())
	}
	if rec := keyReq(door, "GET", "/", "", "", nil); rec.Code != 401 {
		t.Fatalf("door root without a key: %d", rec.Code)
	}
	if rec := keyReq(door, "GET", "/api/users", "hlk_"+strings.Repeat("D", 43), "", nil); rec.Code != 401 {
		t.Fatalf("bad key: %d", rec.Code)
	}
	if rec := keyReq(door, "GET", "/api/users", adminTok, "", nil); rec.Code != 403 {
		t.Fatalf("admin key on the door: %d %s", rec.Code, rec.Body)
	}
	if rec := keyReq(door, "GET", "/api/users", adminTok, "", &local); rec.Code != 403 {
		t.Fatalf("admin key on the door with a viewer: %d %s", rec.Code, rec.Body)
	}

	rec = keyReq(door, "GET", "/api/users", moviesTok, "", nil)
	if got := ids(t, rec); rec.Code != 200 || len(got) != 2 || got["movie-fan"] != "use" || got["owner"] != "admin" {
		t.Fatalf("app users: %d %s", rec.Code, rec.Body)
	}
	if rec := keyReq(door, "GET", "/api/users?app=movies", moviesTok, "", nil); rec.Code != 200 {
		t.Fatalf("own app by name: %d", rec.Code)
	}
	if rec := keyReq(door, "GET", "/api/users?app=music", moviesTok, "", nil); rec.Code != 403 {
		t.Fatalf("other app: %d %s", rec.Code, rec.Body)
	}
	rec = keyReq(door, "GET", "/api/users/me", moviesTok, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"app"`) || !strings.Contains(rec.Body.String(), `"app":"movies"`) {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	// The door serves nothing else, with or without a viewer.
	for _, p := range []string{"/api/apps", "/api/keys", "/", "/api/bootstrap"} {
		if rec := keyReq(door, "GET", p, moviesTok, "", &local); rec.Code != 404 {
			t.Errorf("door %s: %d", p, rec.Code)
		}
	}
}

func TestManageKeys(t *testing.T) {
	r := newRig(t)
	r.people(t)
	h := r.handler()
	member := &core.Viewer{Login: "mo@example.test", PersonID: "member"}

	body := `{"name":"photo service","scopes":["users:read"," users:read "]}`
	rec := post(h, "/api/keys", body, &local)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		Key  string
		Info struct {
			ID     string
			Prefix string
			Name   string
			Scopes []string
			Kind   string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil || !strings.HasPrefix(made.Key, "hlk_") || made.Info.ID == "" ||
		made.Info.Name != "photo service" || made.Info.Kind != "admin" || strings.Join(made.Info.Scopes, ",") != "users:read" {
		t.Fatalf("created: %v %s", err, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("create response shows a hash: %s", rec.Body)
	}

	// The list has the prefix, never the key or its hash.
	rec = do(h, "GET", "/api/keys", "", &local)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), made.Info.Prefix) || !strings.Contains(rec.Body.String(), made.Info.ID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	for _, leak := range []string{made.Key, made.Key[4:], "hash", `"key"`} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("list leaks %q: %s", leak, rec.Body)
		}
	}
	var list map[string][]struct{ ID, CreatedByName string }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list["admin"]) != 1 || list["app"] == nil || list["admin"][0].CreatedByName != "Owner" {
		t.Fatalf("list shape: %v %s", err, rec.Body)
	}

	// The key works...
	if rec := keyReq(h, "GET", "/api/users/me", made.Key, "", &local); rec.Code != 200 {
		t.Fatalf("new key: %d %s", rec.Code, rec.Body)
	}

	// Members get nothing, and can't revoke it.
	if rec := post(h, "/api/keys", body, member); rec.Code != 403 {
		t.Fatalf("member create: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/keys", "", member); rec.Code != 403 {
		t.Fatalf("member list: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/keys/"+made.Info.ID, "", member, "X-Homelab-Request", "1"); rec.Code != 403 {
		t.Fatalf("member revoke: %d", rec.Code)
	}
	if rec := keyReq(h, "GET", "/api/users/me", made.Key, "", &local); rec.Code != 200 {
		t.Fatalf("key after refused revoke: %d", rec.Code)
	}

	// Changes need the companion header.
	if rec := do(h, "DELETE", "/api/keys/"+made.Info.ID, "", &local); rec.Code != 403 {
		t.Fatalf("revoke without header: %d", rec.Code)
	}
	// ...revoke it, and it stops.
	if rec := do(h, "DELETE", "/api/keys/"+made.Info.ID, "", &local, "X-Homelab-Request", "1"); rec.Code != 204 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := keyReq(h, "GET", "/api/users/me", made.Key, "", &local); rec.Code != 401 {
		t.Fatalf("revoked key: %d", rec.Code)
	}
	rec = do(h, "GET", "/api/keys", "", &local)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list["admin"]) != 0 {
		t.Fatalf("list after revoke: %s", rec.Body)
	}
	if rec := do(h, "DELETE", "/api/keys/"+made.Info.ID, "", &local, "X-Homelab-Request", "1"); rec.Code != 400 {
		t.Fatalf("revoke twice: %d %s", rec.Code, rec.Body)
	}

	// An app's own key can be revoked too (it gets a new one on its next deploy).
	_, ak := r.appKey(t, "movies", "dev")
	if rec := do(h, "DELETE", "/api/keys/"+ak.ID, "", &local, "X-Homelab-Request", "1"); rec.Code != 204 {
		t.Fatalf("revoke app key: %d %s", rec.Code, rec.Body)
	}
	if k, _ := r.store.APIKeyByID(context.Background(), ak.ID); k.Revoked == nil {
		t.Fatal("app key wasn't revoked")
	}
	// Bad requests are 400s with a message.
	for _, b := range []string{`{"name":"","scopes":["users:read"]}`, `{"name":"x","scopes":[]}`, `{"name":"x","scopes":["nope"]}`, `{"name":"x","scopes":["files:photos:read"]}`, `{"name":"x","scopes":["users:read"],"expires":"2001-01-01T00:00:00Z"}`} {
		if rec := post(h, "/api/keys", b, &local); rec.Code != 400 {
			t.Errorf("%s: %d %s", b, rec.Code, rec.Body)
		}
	}
}
