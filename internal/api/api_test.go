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
		Notifier: fake.NewNotifier(), Resources: &fake.Resources{Usage: core.DeviceUsage{DiskUsed: 1, DiskTotal: 2}},
		System: &fake.System{Report: core.SystemInfo{OS: "Ubuntu 24.04.4 LTS", UpdatesPending: -1, SecurityPending: -1}},
		Updater: &fake.Updater{State: core.UpdateStatus{Managed: true, Current: "build-1", Latest: "build-2", Available: true},
			TestList: []core.TestVersion{{Version: "pr-7-abc1234", PR: 7, Title: "Better sign-in"}}},
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

func TestInitialOwnerCanRequestFreshPasskeyLinkUntilSignIn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.store.SavePerson(ctx, core.Person{ID: "owner", Name: "Owner", Email: "owner@example.test", IdentityID: "pocket-owner", Status: "invited", Roles: []string{"owner", "admin"}}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	path := "/api/setup/passkey-link"
	if got := post(h, path, `{}`, &core.Viewer{Local: true}); got.Code != http.StatusCreated || !strings.Contains(got.Body.String(), `"url":"http://localhost:1411/login/pocket-owner"`) {
		t.Fatalf("fresh initial-owner link: %d %s", got.Code, got.Body)
	}
	if r.idp.LoginLinkTTL != time.Hour {
		t.Fatalf("initial owner link TTL = %s, want %s", r.idp.LoginLinkTTL, time.Hour)
	}
	if err := r.store.RecordSignIn(ctx, "owner", "pocket-owner"); err != nil {
		t.Fatal(err)
	}
	if got := post(h, path, `{}`, &core.Viewer{Local: true}); got.Code != http.StatusBadRequest {
		t.Fatalf("signed-in owner could request setup-only link: %d %s", got.Code, got.Body)
	}
}

func TestConcurrentSetupCannotCreateTwoOwners(t *testing.T) {
	r := newRig(t)
	if err := r.store.DeletePerson(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	start := make(chan struct{})
	codes := make(chan int, 2)
	for _, body := range []string{`{"name":"Owner One","email":"one@example.test"}`, `{"name":"Owner Two","email":"two@example.test"}`} {
		go func(body string) {
			<-start
			codes <- post(h, "/api/setup", body, nil).Code
		}(body)
	}
	close(start)
	created := 0
	for range 2 {
		if <-codes == http.StatusCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("concurrent setup created %d owners", created)
	}
	people, err := r.store.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, person := range people {
		if hasRole(person, "owner") {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("stored %d owners", owners)
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
	if rec.Code != http.StatusOK {
		t.Fatalf("recover: %d %s", rec.Code, rec.Body)
	}
	noSessionCookie(t, rec)
	token := bodyToken(t, rec)
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Host = "localhost:7080"
	req.Header.Set("Authorization", "Bearer "+token)
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

func TestSignedInPersonCanRequestFreshPasskeyLink(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.store.RecordIdentityID(ctx, "owner", "provider-owner-id"); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SavePerson(ctx, core.Person{ID: "roleless", Name: "Roleless", Email: "roleless@example.test", IdentityID: "provider-roleless-id", Status: "signed_in"}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	got := post(h, "/api/me/passkey-link", `{}`, &core.Viewer{PersonID: "roleless"})
	if got.Code != http.StatusCreated || !strings.Contains(got.Body.String(), `"url":"http://localhost:1411/login/provider-roleless-id"`) {
		t.Fatalf("fresh passkey link: %d %s", got.Code, got.Body)
	}

	if got = post(h, "/api/me/passkey-link", `{}`, nil); got.Code != http.StatusForbidden {
		t.Fatalf("anonymous passkey link: %d %s", got.Code, got.Body)
	}
	r.idp.Fail = true
	if got = post(h, "/api/me/passkey-link", `{}`, &core.Viewer{PersonID: "owner"}); got.Code != http.StatusInternalServerError {
		t.Fatalf("unavailable identity provider: %d %s", got.Code, got.Body)
	}
}

func TestHealthAndRecoveryStayAvailableWhenIdentityProviderFails(t *testing.T) {
	r := newRig(t)
	r.idp.Fail = true
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
	if got := do(h, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("health with unavailable identity provider: %d %s", got.Code, got.Body)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	if got := post(h, "/api/recover", string(body), nil); got.Code != http.StatusOK {
		t.Fatalf("recovery with unavailable identity provider: %d %s", got.Code, got.Body)
	}
	token, err := randomURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	if err := r.store.PutOneTimeCode(context.Background(), digest[:], "owner", "recovery-link", time.Now().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/recover/"+token, nil)
	req.Host = "localhost:7080"
	req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Local: true}))
	link := httptest.NewRecorder()
	h.ServeHTTP(link, req)
	if link.Code != http.StatusSeeOther {
		t.Fatalf("recovery link with unavailable identity provider: %d %s", link.Code, link.Body)
	}
}

func (r *rig) handler() http.Handler {
	return asLocal(New(r.h, Options{Version: "test", Static: testSite(), Logger: nil, Store: r.store}))
}

// asLocal marks requests that carry no viewer as arriving over the loopback connection, as
// homelabd does for every request on its local listener.
func asLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := core.ViewerFrom(r.Context()); !ok {
			r = r.WithContext(core.WithViewer(r.Context(), core.Viewer{Local: true}))
		}
		next.ServeHTTP(w, r)
	})
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

// noSessionCookie fails when a response sets the old companion cookie to a value.
func noSessionCookie(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "homelab_session" && c.Value != "" {
			t.Fatalf("response sets a homelab_session cookie: %v", c)
		}
	}
}

// bodyToken reads the session token from a sign-in response.
func bodyToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !strings.HasPrefix(out.Token, "hls_") {
		t.Fatalf("sign-in body=%s", rec.Body)
	}
	return out.Token
}

func post(h http.Handler, path, body string, v *core.Viewer) *httptest.ResponseRecorder {
	return do(h, "POST", path, body, v, "X-Homelab-Request", "1", "Content-Type", "application/json")
}

func TestAuth(t *testing.T) {
	r := newRig(t)
	owner := &core.Viewer{Login: "Labiba@Example.com", PersonID: "owner"}
	stranger := &core.Viewer{Login: "eve@example.com"}
	anon := &core.Viewer{}
	cases := []struct {
		name string
		v    *core.Viewer
		want int
	}{
		{"local", &local, 200},
		{"loopback alone", &core.Viewer{Local: true}, 403},
		{"signed-in person", owner, 200},
		{"stranger", stranger, 403},
		{"no viewer", nil, 403},
		{"anonymous", anon, 403},
	}
	for _, c := range cases {
		rec := do(r.handler(), "GET", "/api/apps", "", c.v)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		if c.want == 403 && !strings.Contains(rec.Body.String(), "Sign in") && !strings.Contains(rec.Body.String(), "don't have access") {
			t.Errorf("%s: message %s", c.name, rec.Body)
		}
	}
	// healthz is open, even with no viewer.
	if rec := do(r.handler(), "GET", "/healthz", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"test"`) {
		t.Errorf("healthz: %d %s", rec.Code, rec.Body)
	}
	// So is the companion itself (it shows its own sign-in message from the API).
	if rec := do(r.handler(), "GET", "/", "", nil); rec.Code != 200 {
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
	for _, path := range []string{"/api/people", "/api/settings/secrets", "/api/activity", "/api/apps/movies/envs/dev/secrets/TMDB_API_KEY", "/api/apps/movies/secrets"} {
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
	req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Local: true}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("recovery status=%d body=%s", rec.Code, rec.Body)
	}
	noSessionCookie(t, rec)
	// The token is only in the fragment, never in the path or query.
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/#session=hls_") || strings.Count(loc, "hls_") != 1 {
		t.Fatalf("recovery location=%q", loc)
	}
	session := strings.TrimPrefix(loc, "/#session=")
	req = httptest.NewRequest("GET", "/api/me", nil)
	req.Host = "localhost:7080"
	req.Header.Set("Authorization", "Bearer "+session)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovered API status=%d body=%s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest("GET", "/recover/"+token, nil)
	req.Host = "localhost:7080"
	req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Local: true}))
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
	if rec := post(h, "/api/apps", body, &core.Viewer{}); rec.Code != 403 {
		t.Errorf("stranger: %d", rec.Code)
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
	if rec := do(h, "GET", "/invite/invite-token", "", nil); rec.Code != 200 || rec.Header().Get("Location") != "" {
		t.Fatalf("invite should open the Homelab registration screen: %d location=%q", rec.Code, rec.Header().Get("Location"))
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

func TestInviteRedemptionReturnsPasskeyRegistrationLink(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	invite, err := r.h.InvitePerson(ctx, core.Person{Name: "New person", Email: "new@example.test", Roles: []string{"member"}})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(invite.URL, "http://localhost:7080/invite/")
	h := r.handler()
	path := "/api/invites/" + token + "/redeem"
	got := post(h, path, `{}`, &core.Viewer{Local: true})
	if got.Code != http.StatusCreated {
		t.Fatalf("redeem invite: %d %s", got.Code, got.Body)
	}
	var response struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	const pocketIDLoginPrefix = "http://localhost:1411/login/"
	if !strings.HasPrefix(response.URL, pocketIDLoginPrefix) || strings.TrimPrefix(response.URL, pocketIDLoginPrefix) == "" {
		t.Fatalf("invite returned invalid Pocket ID passkey link: %q", response.URL)
	}
	if r.idp.LoginLinkTTL != time.Hour {
		t.Fatalf("invite login link TTL = %s, want %s", r.idp.LoginLinkTTL, time.Hour)
	}
	if got = post(h, path, `{}`, &core.Viewer{Local: true}); got.Code != http.StatusCreated {
		t.Fatalf("redeem invite for a fresh Pocket ID link: %d %s", got.Code, got.Body)
	}
	people, err := r.h.People(ctx)
	if err != nil || len(people) != 2 {
		t.Fatalf("invited people=%+v err=%v", people, err)
	}
	var invited core.Person
	for _, person := range people {
		if person.Email == "new@example.test" {
			invited = person
		}
	}
	if invited.ID == "" {
		t.Fatal("invited person not found")
	}
	if err := r.store.RecordSignIn(ctx, invited.ID, "pocket-id-subject"); err != nil {
		t.Fatal(err)
	}
	if got = post(h, path, `{}`, &core.Viewer{Local: true}); got.Code != http.StatusGone {
		t.Fatalf("redeem accepted invite after sign-in: %d %s", got.Code, got.Body)
	}

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Host = "example.com"
	req.Header.Set("X-Homelab-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, req)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("redeem remote invite: %d %s", blocked.Code, blocked.Body)
	}
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:7080": true, "localhost:7080": true, "[::1]:7080": true, "127.0.0.1": true,
		"evil.example:7080": false, "192.168.1.5:7080": false,
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
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "ghp_x") || strings.Contains(rec.Body.String(), "TS_") {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}

	if rec := send("DELETE", "/api/apps/movies", ""); rec.Code != 204 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/apps/movies", "", &local); rec.Code != 404 {
		t.Fatalf("after remove: %d", rec.Code)
	}
}

// A Pocket ID one-time link works once. Someone without a passkey who leaves the
// page, or whose link was used or expired, must get a newly issued link from the
// same Homelab page instead of landing on Pocket ID's passkey sign-in page.
func TestPasskeyLinksAreReissuedAfterAnEarlierLinkIsUsed(t *testing.T) {
	t.Run("initial owner setup", func(t *testing.T) {
		r := newRig(t)
		ctx := context.Background()
		if err := r.store.SavePerson(ctx, core.Person{ID: "owner", Name: "Owner", Email: "owner@example.test", IdentityID: "pocket-owner", Status: "invited", Roles: []string{"owner", "admin"}}); err != nil {
			t.Fatal(err)
		}
		h := r.handler()
		for attempt := 1; attempt <= 3; attempt++ {
			if got := post(h, "/api/setup/passkey-link", `{}`, &core.Viewer{Local: true}); got.Code != http.StatusCreated {
				t.Fatalf("attempt %d: %d %s", attempt, got.Code, got.Body)
			}
			if r.idp.LoginLinkCalls != attempt {
				t.Fatalf("attempt %d reused an earlier one-time link (provider calls = %d)", attempt, r.idp.LoginLinkCalls)
			}
		}
	})
	t.Run("invite", func(t *testing.T) {
		r := newRig(t)
		ctx := context.Background()
		invite, err := r.h.InvitePerson(ctx, core.Person{Name: "New person", Email: "new@example.test", Roles: []string{"member"}})
		if err != nil {
			t.Fatal(err)
		}
		h := r.handler()
		path := "/api/invites/" + strings.TrimPrefix(invite.URL, "http://localhost:7080/invite/") + "/redeem"
		before := r.idp.LoginLinkCalls
		for attempt := 1; attempt <= 3; attempt++ {
			if got := post(h, path, `{}`, &core.Viewer{Local: true}); got.Code != http.StatusCreated {
				t.Fatalf("attempt %d: %d %s", attempt, got.Code, got.Body)
			}
			if r.idp.LoginLinkCalls != before+attempt {
				t.Fatalf("attempt %d reused an earlier one-time link (provider calls = %d)", attempt, r.idp.LoginLinkCalls-before)
			}
		}
	})
}

func TestPushResourcesAndBuilderActivity(t *testing.T) {
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
	sub := `{"endpoint":"https://fcm.googleapis.com/fcm/send/x","p256dh":"k","auth":"a","device":"Phone"}`
	if rec := post(h, "/api/push/subscriptions", sub, b); rec.Code != 204 {
		t.Fatalf("subscribe = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/push/test", "", b); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sent":1`) {
		t.Fatalf("test push = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/activity?app=movies", "", b); rec.Code != 200 {
		t.Fatalf("builder activity for their app = %d", rec.Code)
	}
	for _, path := range []string{"/api/activity", "/api/activity?app=other", "/api/resources"} {
		if rec := do(h, "GET", path, "", b); rec.Code != 403 {
			t.Errorf("builder %s = %d, want 403", path, rec.Code)
		}
	}
	if rec := do(h, "GET", "/api/resources", "", &local); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"diskTotal":2`) {
		t.Fatalf("resources = %d %s", rec.Code, rec.Body)
	}
}

func TestDeviceAndUpdate(t *testing.T) {
	r := newRig(t)
	if err := r.store.SavePerson(context.Background(), core.Person{ID: "m", Name: "M", Status: "signed_in", Roles: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	m := &core.Viewer{Local: true, PersonID: "m"}
	if rec := do(h, "GET", "/api/device", "", m); rec.Code != 403 {
		t.Fatalf("member device = %d", rec.Code)
	}
	if rec := post(h, "/api/device/update", "", m); rec.Code != 403 {
		t.Fatalf("member update = %d", rec.Code)
	}
	rec := do(h, "GET", "/api/device", "", &local)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"os":"Ubuntu 24.04.4 LTS"`) || !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("device = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/device/update", "", &local); rec.Code != 202 || !strings.Contains(rec.Body.String(), "build-2") {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/device/tests", "", m); rec.Code != 403 {
		t.Fatalf("member tests = %d", rec.Code)
	}
	if rec := post(h, "/api/device/test", `{"version":"pr-7-abc1234"}`, m); rec.Code != 403 {
		t.Fatalf("member try = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/device/tests", "", &local); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"pr":7`) {
		t.Fatalf("tests = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/device/test", `{"version":"pr-7-abc1234"}`, &local); rec.Code != 202 {
		t.Fatalf("try = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/device", "", &local); !strings.Contains(rec.Body.String(), `"testing":{"version":"pr-7-abc1234"`) {
		t.Fatalf("device while testing = %s", rec.Body)
	}
	if rec := post(h, "/api/device/test/back", "", &local); rec.Code != 202 || !strings.Contains(rec.Body.String(), "build-1") {
		t.Fatalf("go back = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/device/test/keep", "", &local); rec.Code == 202 {
		t.Fatalf("keep without a test = %d", rec.Code)
	}
}

func TestOldSessionCookieIsRefusedAndSignOutEndsTheToken(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if err := r.store.PutSession(context.Background(), digest[:], "owner", time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	token := sessionPrefix + base64.RawURLEncoding.EncodeToString(raw)
	anon := &core.Viewer{Local: true}
	cookie := strings.TrimPrefix(token, sessionPrefix)
	// The same value as an old cookie, which older versions set, no longer signs anyone in.
	if rec := do(h, "GET", "/api/me", "", anon, "Cookie", "homelab_session="+cookie); rec.Code == http.StatusOK {
		t.Fatalf("old cookie accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/me", "", anon, "Authorization", "Bearer "+token); rec.Code != http.StatusOK {
		t.Fatalf("bearer refused: %d %s", rec.Code, rec.Body)
	}
	// A token without the session prefix is treated as an API key and refused.
	if rec := do(h, "GET", "/api/me", "", anon, "Authorization", "Bearer "+cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unprefixed bearer: %d", rec.Code)
	}
	rec := do(h, "POST", "/api/logout", "{}", anon, "X-Homelab-Request", "1", "Content-Type", "application/json", "Authorization", "Bearer "+token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == "homelab_session" && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("logout didn't clear the old cookie")
	}
	if rec := do(h, "GET", "/api/me", "", anon, "Authorization", "Bearer "+token); rec.Code == http.StatusOK {
		t.Fatalf("token still works after sign-out: %d", rec.Code)
	}
}
