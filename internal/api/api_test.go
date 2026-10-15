package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
	"github.com/rupaai/homelab/internal/state"
)

const manifestDoc = `
id: movies
name: Movie Night
image: ghcr.io/rupaai/movies
tag: "sha-{commit}"
port: 8000
health: /healthz
data: [{name: db, path: /data}]
secrets: [TMDB_API_KEY]
`

const indexHTML = `<!doctype html><html><body><script>window.boot=1</script></body></html>`

type rig struct {
	h     *hub.Hub
	src   *fake.Source
	fail  *atomic.Bool
	store *state.Store
	idp   *fake.Identity
}

// flakySource fails every read while fail is set.
type flakySource struct {
	*fake.Source
	fail *atomic.Bool
}

func (f flakySource) ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error) {
	if f.fail.Load() {
		return nil, errors.New("deadline exceeded talking to github")
	}
	return f.Source.ReadFile(ctx, repo, ref, path)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "homelab.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SavePerson(context.Background(), core.Person{ID: "owner", Name: "Owner", Email: "owner@example.test", Status: "signed_in", Roles: []string{"owner", "admin"}}); err != nil {
		t.Fatal(err)
	}
	src := fake.NewSource()
	fail := &atomic.Bool{}
	idp := fake.NewIdentity()
	h := hub.New(hub.Config{DataDir: dir}, st, hub.Adapters{
		Source: flakySource{src, fail}, Builder: fake.NewBuilder(), Registry: fake.NewRegistry(), Deployer: fake.NewDeployer(),
		Addresses: fake.NewAddresses(), Health: fake.NewHealth(), Secrets: fake.NewSecrets(), Identity: idp, Email: fake.Email{},
	})
	return &rig{h: h, src: src, fail: fail, store: st, idp: idp}
}

func TestBootstrapCreatesOwnerAndOneTimeRecoveryCode(t *testing.T) {
	r := newRig(t)
	if err := r.store.DeletePerson(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	if rec := do(h, "GET", "/api/bootstrap", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needsSetup":true`) {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}
	rec := post(h, "/api/setup", `{"name":"Labiba","email":"owner@example.test"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		LoginURL     string `json:"loginUrl"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.LoginURL == "" || got.RecoveryCode == "" {
		t.Fatalf("setup response omitted credentials: %+v", got)
	}
	exists, err := r.store.OwnerExists(context.Background())
	if err != nil || !exists {
		t.Fatalf("owner exists=%v err=%v", exists, err)
	}
	p, err := r.store.PersonByEmail(context.Background(), "OWNER@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "invited" || !hasRole(p, "owner") || !hasRole(p, "admin") {
		t.Fatalf("owner=%+v", p)
	}
	if u := r.idp.Users[p.ID]; u.ID != p.ID {
		t.Fatalf("Pocket ID user=%+v", u)
	}
	hash, used, err := r.store.RecoveryHash(context.Background(), p.ID)
	if err != nil || used || hash == got.RecoveryCode {
		t.Fatalf("recovery hash=%q used=%v err=%v", hash, used, err)
	}
	if rec = post(h, "/api/setup", `{"name":"Other","email":"other@example.test"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("second setup: %d %s", rec.Code, rec.Body)
	}
}

func TestRecoveryCodeIsLoopbackOnlyRateLimitedAndSingleUse(t *testing.T) {
	r := newRig(t)
	code, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetRecovery(context.Background(), "owner", string(hash)); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	body, _ := json.Marshal(map[string]string{"code": code})
	rec := post(h, "/api/recover", string(body), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("recover: %d %s", rec.Code, rec.Body)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "homelab_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("recovery didn't create a session")
	}
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Host = "localhost:7080"
	req.AddCookie(cookie)
	got := httptest.NewRecorder()
	h.ServeHTTP(got, req)
	if got.Code != http.StatusOK {
		t.Fatalf("recovered session: %d %s", got.Code, got.Body)
	}
	if rec = post(h, "/api/recover", string(body), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body)
	}
	invalidCode, err := randomURLToken(20)
	if err != nil {
		t.Fatal(err)
	}
	invalidBody, _ := json.Marshal(map[string]string{"code": invalidCode})
	req = httptest.NewRequest("POST", "/api/recover", strings.NewReader(string(invalidBody)))
	req.Host = "example.com"
	req.Header.Set("Content-Type", "application/json")
	got = httptest.NewRecorder()
	h.ServeHTTP(got, req)
	if got.Code != http.StatusForbidden {
		t.Fatalf("remote recovery: %d %s", got.Code, got.Body)
	}
}

func (r *rig) handler(owners ...string) http.Handler {
	return New(r.h, Options{Owners: owners, Version: "test", Static: testSite(), Logger: nil, Store: r.store})
}

func testSite() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                  {Data: []byte(indexHTML)},
		"favicon.png":                 {Data: []byte("png")},
		"_app/immutable/entry/app.js": {Data: []byte("x")},
	}
}

var local = core.Viewer{Local: true, PersonID: "owner"}

func do(h http.Handler, method, path, body string, v *core.Viewer, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:7080"
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

func post(h http.Handler, path, body string, v *core.Viewer) *httptest.ResponseRecorder {
	return do(h, "POST", path, body, v, "X-Homelab-Request", "1", "Content-Type", "application/json")
}

func TestAuth(t *testing.T) {
	r := newRig(t)
	owner := &core.Viewer{Login: "Labiba@Example.com", PersonID: "owner"}
	stranger := &core.Viewer{Login: "eve@example.com"}
	tagged := &core.Viewer{Login: "tagged-devices", Tagged: true}
	anon := &core.Viewer{}
	cases := []struct {
		name   string
		owners []string
		v      *core.Viewer
		want   int
	}{
		{"local", []string{"labiba@example.com"}, &local, 200},
		{"loopback alone", []string{"labiba@example.com"}, &core.Viewer{Local: true}, 403},
		{"owner session", []string{"labiba@example.com"}, owner, 200},
		{"stranger", []string{"labiba@example.com"}, stranger, 403},
		{"tagged node", []string{"labiba@example.com"}, tagged, 403},
		{"no viewer", []string{"labiba@example.com"}, nil, 403},
		{"empty owners, signed-in person", nil, &core.Viewer{PersonID: "owner"}, 200},
		{"empty owners, tagged", nil, tagged, 403},
		{"empty owners, anonymous", nil, anon, 403},
	}
	for _, c := range cases {
		rec := do(r.handler(c.owners...), "GET", "/api/apps", "", c.v)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		if c.want == 403 && !strings.Contains(rec.Body.String(), "Sign in") && !strings.Contains(rec.Body.String(), "don't have access") {
			t.Errorf("%s: message %s", c.name, rec.Body)
		}
	}
	// healthz is open, even with no viewer.
	if rec := do(r.handler("a@b.c"), "GET", "/healthz", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"test"`) {
		t.Errorf("healthz: %d %s", rec.Code, rec.Body)
	}
	// So is the companion itself (it shows its own sign-in message from the API).
	if rec := do(r.handler("a@b.c"), "GET", "/", "", nil); rec.Code != 200 {
		t.Errorf("static: %d", rec.Code)
	}
}

func TestMemberAPIIsScoped(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.AddApp(ctx, state.App{ID: "movies", Name: "Movies", Repo: "a/b", Manifest: []byte("id: movies"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SavePerson(ctx, core.Person{ID: "sam", Name: "Sam", Status: "signed_in", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}}); err != nil {
		t.Fatal(err)
	}
	v := &core.Viewer{PersonID: "sam"}
	h := r.handler()
	rec := do(h, "GET", "/api/apps", "", v)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"env":"prod"`) || strings.Contains(rec.Body.String(), `"env":"dev"`) {
		t.Fatalf("member app list: %d %s", rec.Code, rec.Body)
	}
	if rec = do(h, "GET", "/api/apps/movies/envs/dev/logs", "", v); rec.Code != 403 {
		t.Fatalf("member logs status=%d body=%s", rec.Code, rec.Body)
	}
	if rec = do(h, "GET", "/api/apps/movies", "", v); rec.Code != 200 || strings.Contains(rec.Body.String(), `"env":"dev"`) {
		t.Fatalf("member app detail leaked dev: %d %s", rec.Code, rec.Body)
	}
	if rec = do(h, "GET", "/api/projects", "", v); rec.Code != 403 {
		t.Fatalf("member projects status=%d body=%s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/api/people", "/api/settings/secrets", "/api/activity", "/api/apps/movies/envs/dev/secrets/TMDB_API_KEY"} {
		if rec = do(h, "GET", path, "", v); rec.Code != 403 {
			t.Errorf("member %s status=%d body=%s", path, rec.Code, rec.Body)
		}
	}
	if rec = do(h, "GET", "/api/settings/secrets", "", v); rec.Code != 403 {
		t.Fatalf("member settings status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestBuilderProjectsIncludeOnlyBuildGrants(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.AddApp(ctx, state.App{ID: "movies", Name: "Movies", Repo: "a/b", Manifest: []byte("id: movies"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AddApp(ctx, state.App{ID: "recipes", Name: "Recipes", Repo: "a/c", Manifest: []byte("id: recipes"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SavePerson(ctx, core.Person{ID: "builder", Name: "Builder", Status: "signed_in", Roles: []string{"builder"}, Grants: map[string]string{"movies": "build", "recipes": "use"}}); err != nil {
		t.Fatal(err)
	}
	rec := do(r.handler(), "GET", "/api/projects", "", &core.Viewer{PersonID: "builder"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"movies"`) || strings.Contains(rec.Body.String(), `"id":"recipes"`) {
		t.Fatalf("builder projects: %d %s", rec.Code, rec.Body)
	}
}

func TestRecoveryLinkCreatesOneOwnerSession(t *testing.T) {
	r := newRig(t)
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	digest := sha256.Sum256([]byte(token))
	if err := r.store.PutOneTimeCode(context.Background(), digest[:], "owner", "recovery-link", time.Now().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	req := httptest.NewRequest("GET", "/recover/"+token, nil)
	req.Host = "localhost:7080"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("recovery status=%d body=%s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()
	if len(cookie) == 0 || cookie[0].Name != "homelab_session" || !cookie[0].HttpOnly {
		t.Fatalf("session cookie=%v", cookie)
	}
	req = httptest.NewRequest("GET", "/api/me", nil)
	req.Host = "localhost:7080"
	req.AddCookie(cookie[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovered API status=%d body=%s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest("GET", "/recover/"+token, nil)
	req.Host = "localhost:7080"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("reused recovery link status=%d", rec.Code)
	}
}

func TestMe(t *testing.T) {
	r := newRig(t)
	rec := do(r.handler(), "GET", "/api/me", "", &core.Viewer{PersonID: "owner", Login: "a@b.c", Name: "A"})
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["login"] != "owner@example.test" || got["name"] != "Owner" || got["local"] != false {
		t.Fatalf("%v", got)
	}
}

func TestCSRFAndBody(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	body := `{"repo":"rupaai/movies"}`
	if rec := do(h, "POST", "/api/apps", body, &local, "Content-Type", "application/json"); rec.Code != 403 {
		t.Errorf("no header: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/apps", body, &local, "X-Homelab-Request", "1", "Content-Type", "text/plain"); rec.Code != 415 {
		t.Errorf("wrong type: %d", rec.Code)
	}
	if rec := post(h, "/api/apps", `{"repo":"a/b","extra":1}`, &local); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	if rec := post(h, "/api/apps", `{"repo":"`+strings.Repeat("a", 70000)+`"}`, &local); rec.Code != 413 {
		t.Errorf("too big: %d", rec.Code)
	}
	if rec := post(h, "/api/apps", ``, &local); rec.Code != 400 {
		t.Errorf("empty: %d", rec.Code)
	}
	// Stranger with the header is still refused.
	if rec := post(h, "/api/apps", body, &core.Viewer{Tagged: true}); rec.Code != 403 {
		t.Errorf("tagged: %d", rec.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	// Unknown app: 404.
	rec := do(h, "GET", "/api/apps/nope", "", &local)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "no app") {
		t.Errorf("unknown app: %d %s", rec.Code, rec.Body)
	}
	// Bad repo: 400 with the hub's message.
	rec = post(h, "/api/apps", `{"repo":"nonsense"}`, &local)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "owner/name") {
		t.Errorf("bad repo: %d %s", rec.Code, rec.Body)
	}
	// Unexpected failure: 500, generic message (the source reports an internal error).
	r.fail.Store(true)
	rec = post(h, "/api/apps", `{"repo":"rupaai/movies"}`, &local)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "deadline") || !strings.Contains(rec.Body.String(), "details are in its log") {
		t.Errorf("internal: %d %s", rec.Code, rec.Body)
	}
	// Busy: 409.
	rec2 := httptest.NewRecorder()
	(&server{log: slog.Default()}).fail(rec2, httptest.NewRequest("GET", "/", nil), hub.ErrBusy)
	if rec2.Code != 409 {
		t.Errorf("busy: %d", rec2.Code)
	}
	// Unknown API path: JSON 404, not the SPA.
	if rec := do(h, "GET", "/api/zzz", "", &local); rec.Code != 404 || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("unknown api: %d %s", rec.Code, rec.Header())
	}
}

func TestAppsFlow(t *testing.T) {
	r := newRig(t)
	h := r.handler()

	rec := do(h, "GET", "/api/apps", "", &local)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list: %d %q", rec.Code, rec.Body)
	}

	r.src.Push("rupaai/movies", "main", "aaaaaaa1", "first", []byte(manifestDoc))
	rec = post(h, "/api/apps", `{"repo":"https://github.com/rupaai/movies.git"}`, &local)
	if rec.Code != 201 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var added map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || added["id"] != "movies" {
		t.Fatalf("added view: %v %v", err, added)
	}
	if rec := post(h, "/api/apps", `{"repo":"rupaai/movies"}`, &local); rec.Code != 400 || !strings.Contains(rec.Body.String(), "already") {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", "/api/apps", "", &local)
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0]["id"] != "movies" || list[0]["name"] != "Movie Night" {
		t.Fatalf("list: %v %s", err, rec.Body)
	}
	if rec := do(h, "GET", "/api/apps/movies", "", &local); rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}

	// The add shows up in the activity log, newest first.
	rec = do(h, "GET", "/api/activity?app=movies&limit=5", "", &local)
	var ev []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil || len(ev) == 0 {
		t.Fatalf("activity: %v %s", err, rec.Body)
	}
	if rec := do(h, "GET", "/api/apps/movies/envs/dev/logs?lines=10", "", &local); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lines"`) {
		t.Fatalf("logs: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/apps/nope/promote", "", &local); rec.Code != 404 {
		t.Fatalf("promote preview of unknown app: %d", rec.Code)
	}
	if rec := post(h, "/api/apps/nope/deploy", ``, &local); rec.Code != 404 {
		t.Fatalf("deploy of unknown app: %d %s", rec.Code, rec.Body)
	}
}

func TestStatic(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	rec := do(h, "GET", "/apps/movies", "", &core.Viewer{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "window.boot") {
		t.Fatalf("spa fallback: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("index cache: %q", rec.Header().Get("Cache-Control"))
	}
	if rec := do(h, "GET", "/favicon.png", "", nil); rec.Body.String() != "png" {
		t.Errorf("file: %q", rec.Body)
	}
	if rec := do(h, "GET", "/_app/immutable/entry/app.js", "", nil); !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("immutable: %q", rec.Header().Get("Cache-Control"))
	}
	hd := rec.Header()
	for _, k := range []string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Content-Security-Policy"} {
		if hd.Get(k) == "" {
			t.Errorf("missing %s", k)
		}
	}
	csp := hd.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'sha256-") || strings.Contains(strings.Split(csp, "style-src")[0], "unsafe-inline") {
		t.Errorf("csp: %s", csp)
	}
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:7080": true, "localhost:7080": true, "[::1]:7080": true, "127.0.0.1": true,
		"evil.example:7080": false, "homelab.tail1.ts.net": false, "192.168.1.5:7080": false,
	} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestLocalNeedsLoopbackHost(t *testing.T) {
	r := newRig(t)
	req := httptest.NewRequest("GET", "/api/apps", nil)
	req.Host = "rebound.example:7080"
	req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Local: true}))
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestMilestone2Endpoints(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	send := func(method, path, body string) *httptest.ResponseRecorder {
		return do(h, method, path, body, &local, "X-Homelab-Request", "1", "Content-Type", "application/json")
	}
	r.src.Push("rupaai/movies", "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if rec := post(h, "/api/apps", `{"repo":"rupaai/movies"}`, &local); rec.Code != 201 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}

	// Changes other than POST need the header too.
	if rec := do(h, "PUT", "/api/apps/movies/promotion", `{"mode":"auto"}`, &local, "Content-Type", "application/json"); rec.Code != 403 {
		t.Fatalf("PUT without header: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/apps/movies", "", &local); rec.Code != 403 {
		t.Fatalf("DELETE without header: %d", rec.Code)
	}

	rec := send("PUT", "/api/apps/movies/promotion", `{"mode":"auto"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"promotion":"auto"`) {
		t.Fatalf("promotion: %d %s", rec.Code, rec.Body)
	}
	if rec := send("PUT", "/api/apps/movies/promotion", `{"mode":"often"}`); rec.Code != 400 {
		t.Fatalf("bad mode: %d", rec.Code)
	}

	if rec := send("PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"secret-value"}`); rec.Code != 204 {
		t.Fatalf("set secret: %d %s", rec.Code, rec.Body)
	}
	if rec := send("PUT", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", `{"value":"a\nb"}`); rec.Code != 400 {
		t.Fatalf("value with a line break: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/apps/movies/secrets", "", &local)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret-value") || !strings.Contains(rec.Body.String(), `"set":true`) {
		t.Fatalf("secrets: %d %s", rec.Code, rec.Body)
	}
	if rec := send("DELETE", "/api/apps/movies/envs/prod/secrets/TMDB_API_KEY", ""); rec.Code != 204 {
		t.Fatalf("delete secret: %d", rec.Code)
	}

	if rec := send("PUT", "/api/settings/secrets/GITHUB_TOKEN", `{"value":"ghp_x"}`); rec.Code != 204 {
		t.Fatalf("set setting: %d %s", rec.Code, rec.Body)
	}
	if rec := send("PUT", "/api/settings/secrets/NOPE", `{"value":"x"}`); rec.Code != 400 {
		t.Fatalf("unknown setting: %d", rec.Code)
	}
	rec = do(h, "GET", "/api/settings/secrets", "", &local)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "ghp_x") || !strings.Contains(rec.Body.String(), `"appliesAfterRestart":true`) {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}

	if rec := send("DELETE", "/api/apps/movies", ""); rec.Code != 204 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/apps/movies", "", &local); rec.Code != 404 {
		t.Fatalf("after remove: %d", rec.Code)
	}
}
