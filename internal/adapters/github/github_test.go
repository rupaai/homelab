package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

func newTestClient(t *testing.T, h http.Handler, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Options{Token: token, APIBase: srv.URL, RegistryBase: srv.URL})
}

func TestReadFile(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/contents/dir/homelab.yaml" && r.URL.Query().Get("ref") == "main" {
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" ||
				r.Header.Get("Authorization") != "Bearer tok" ||
				r.Header.Get("User-Agent") != "homelab" ||
				r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Errorf("bad headers: %v", r.Header)
			}
			w.Write([]byte("name: x\n"))
			return
		}
		http.NotFound(w, r)
	}), "tok")
	b, err := c.ReadFile(context.Background(), "o/r", "main", "dir/homelab.yaml")
	if err != nil || string(b) != "name: x\n" {
		t.Fatalf("got %q, %v", b, err)
	}
	_, err = c.ReadFile(context.Background(), "o/r", "main", "nope.yaml")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

const commitJSON = `{"sha":"abc","html_url":"https://github.com/o/r/commit/abc",
"commit":{"message":"Fix it\n\nlong body","author":{"name":"Git Name","date":"2026-01-02T03:04:05Z"}},
"author":{"login":"octo"}}`

func TestCommit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/commits/abc" {
			fmt.Fprint(w, commitJSON)
			return
		}
		if r.URL.Path == "/repos/o/r/commits/nologin" {
			fmt.Fprint(w, `{"sha":"n","commit":{"message":"m","author":{"name":"Git Name","date":"2026-01-02T03:04:05Z"}},"author":null}`)
			return
		}
		http.NotFound(w, r)
	}), "")
	cm, err := c.Commit(context.Background(), "o/r", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if cm.Message != "Fix it" || cm.Author != "octo" || cm.URL == "" || cm.When.Year() != 2026 || cm.SHA != "abc" {
		t.Fatalf("%+v", cm)
	}
	cm, err = c.Commit(context.Background(), "o/r", "nologin")
	if err != nil || cm.Author != "Git Name" {
		t.Fatalf("%+v %v", cm, err)
	}
	if _, err := c.Commit(context.Background(), "o/r", "zzz"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestCompare(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"commits":[`)
	for i := 0; i < 130; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"sha":"s%d","commit":{"message":"m%d","author":{"name":"n","date":"2026-01-01T00:00:00Z"}}}`, i, i)
	}
	sb.WriteString(`]}`)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/compare/a...b" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, sb.String())
	}), "")
	cs, err := c.Compare(context.Background(), "o/r", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 100 || cs[0].SHA != "s30" || cs[99].SHA != "s129" {
		t.Fatalf("len %d first %s last %s", len(cs), cs[0].SHA, cs[len(cs)-1].SHA)
	}
}

func TestWebURL(t *testing.T) {
	c := New(Options{})
	cases := []struct{ base, head, want string }{
		{"", "", "https://github.com/o/r"},
		{"", "h", "https://github.com/o/r/commit/h"},
		{"b", "h", "https://github.com/o/r/compare/b...h"},
	}
	for _, tc := range cases {
		if got := c.WebURL("o/r", tc.base, tc.head); got != tc.want {
			t.Errorf("got %s want %s", got, tc.want)
		}
	}
}

func TestLatestBuildStatusAndURLs(t *testing.T) {
	cases := []struct {
		status, conclusion, want string
	}{
		{"completed", "success", core.BuildPassed},
		{"completed", "failure", core.BuildFailed},
		{"completed", "cancelled", core.BuildFailed},
		{"in_progress", "", core.BuildRunning},
		{"queued", "", core.BuildRunning},
	}
	for _, tc := range cases {
		var gotPath string
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			q := r.URL.Query()
			if q.Get("branch") != "main" || q.Get("event") != "push" || q.Get("per_page") != "1" {
				t.Errorf("query %v", q)
			}
			concl := "null"
			if tc.conclusion != "" {
				concl = `"` + tc.conclusion + `"`
			}
			fmt.Fprintf(w, `{"workflow_runs":[{"head_sha":"sha1","status":%q,"conclusion":%s,"html_url":"https://x/run/1"}]}`, tc.status, concl)
		}), "")
		b, err := c.LatestBuild(context.Background(), "o/r", "main", "")
		if err != nil || b.Status != tc.want || b.Commit != "sha1" || b.URL != "https://x/run/1" {
			t.Errorf("%s/%s: %+v %v", tc.status, tc.conclusion, b, err)
		}
		if gotPath != "/repos/o/r/actions/runs" {
			t.Errorf("path %s", gotPath)
		}
		c.LatestBuild(context.Background(), "o/r", "main", "ci.yml")
		if gotPath != "/repos/o/r/actions/workflows/ci.yml/runs" {
			t.Errorf("workflow path %s", gotPath)
		}
	}
}

func TestLatestBuildNoRuns(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
	}), "")
	if _, err := c.LatestBuild(context.Background(), "o/r", "main", ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestLatestBuildETag(t *testing.T) {
	var calls, conditional int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			atomic.AddInt32(&conditional, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"workflow_runs":[{"head_sha":"s","status":"completed","conclusion":"success","html_url":"u"}]}`)
	}), "")
	for i := 0; i < 3; i++ {
		b, err := c.LatestBuild(context.Background(), "o/r", "main", "")
		if err != nil || b.Status != core.BuildPassed || b.Commit != "s" {
			t.Fatalf("round %d: %+v %v", i, b, err)
		}
	}
	if calls != 3 || conditional != 2 {
		t.Fatalf("calls %d conditional %d", calls, conditional)
	}
}

func TestErrors(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).Unix()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/commits/a":
			w.WriteHeader(401)
		case "/repos/o/r/commits/b":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset))
			w.WriteHeader(403)
		case "/repos/o/r/commits/c":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
		case "/repos/o/r/commits/d":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"Resource not accessible"}`)
		}
	}), "")
	ctx := context.Background()
	_, err := c.Commit(ctx, "o/r", "a")
	if err == nil || !strings.Contains(err.Error(), "GitHub said 401: the GitHub token in Settings is missing or wrong") {
		t.Errorf("401: %v", err)
	}
	_, err = c.Commit(ctx, "o/r", "b")
	if err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "resets at") {
		t.Errorf("rate: %v", err)
	}
	_, err = c.Commit(ctx, "o/r", "c")
	if err == nil || !strings.Contains(err.Error(), "in 2m0s") {
		t.Errorf("429: %v", err)
	}
	_, err = c.Commit(ctx, "o/r", "d")
	if err == nil || !strings.Contains(err.Error(), "isn't allowed") {
		t.Errorf("403: %v", err)
	}
}

// registryServer serves a ghcr-like registry that needs a bearer token.
func registryServer(t *testing.T, wantBasicPass string, tokenCalls, manifestCalls *int32) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			atomic.AddInt32(tokenCalls, 1)
			u, p, ok := r.BasicAuth()
			if wantBasicPass == "" && ok {
				t.Errorf("expected anonymous token request")
			}
			if wantBasicPass != "" && (!ok || u != "homelab" || p != wantBasicPass) {
				t.Errorf("basic auth %v %q %q", ok, u, p)
			}
			if r.URL.Query().Get("service") != "ghcr.io" || r.URL.Query().Get("scope") != "repository:o/app:pull" {
				t.Errorf("token query %v", r.URL.Query())
			}
			fmt.Fprint(w, `{"token":"bt","expires_in":300}`)
		case strings.HasPrefix(r.URL.Path, "/v2/o/app/manifests/"):
			atomic.AddInt32(manifestCalls, 1)
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") ||
				!strings.Contains(r.Header.Get("Accept"), "application/vnd.docker.distribution.manifest.v2+json") {
				t.Errorf("accept %q", r.Header.Get("Accept"))
			}
			if r.Header.Get("Authorization") != "Bearer bt" {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="ghcr.io",scope="repository:o/app:pull"`, srv.URL))
				w.WriteHeader(401)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/missing") {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func registryClient(srv *httptest.Server, token string) *Client {
	c := New(Options{Token: token, APIBase: srv.URL, RegistryBase: srv.URL})
	c.registryHost = "ghcr.io" // the test server stands in for ghcr.io
	return c
}

func TestResolveTokenFlowAndCache(t *testing.T) {
	var tokens, manifests int32
	srv := registryServer(t, "secret", &tokens, &manifests)
	c := registryClient(srv, "secret")
	for i := 0; i < 2; i++ {
		d, err := c.Resolve(context.Background(), "ghcr.io/o/app", "sha-1")
		if err != nil || d != "sha256:deadbeef" {
			t.Fatalf("%q %v", d, err)
		}
	}
	// First resolve: 401, token, retry. Second: the cached token is sent up front... but the
	// client only learns the scope from a 401, so it fetches the token from cache after it.
	if tokens != 1 {
		t.Errorf("token fetched %d times, want 1 (cached)", tokens)
	}
}

func TestResolveAnonymous(t *testing.T) {
	var tokens, manifests int32
	srv := registryServer(t, "", &tokens, &manifests)
	c := registryClient(srv, "")
	d, err := c.Resolve(context.Background(), "ghcr.io/o/app", "latest")
	if err != nil || d != "sha256:deadbeef" {
		t.Fatalf("%q %v", d, err)
	}
}

func TestResolveNotFound(t *testing.T) {
	var tokens, manifests int32
	srv := registryServer(t, "", &tokens, &manifests)
	c := registryClient(srv, "")
	_, err := c.Resolve(context.Background(), "ghcr.io/o/app", "missing")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveOtherRegistry(t *testing.T) {
	c := New(Options{})
	_, err := c.Resolve(context.Background(), "docker.io/library/nginx", "latest")
	if err == nil || !strings.Contains(err.Error(), "isn't supported yet") || errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveBadToken(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="ghcr.io"`)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := registryClient(srv, "bad")
	_, err := c.Resolve(context.Background(), "ghcr.io/o/app", "t")
	if err == nil || !strings.Contains(err.Error(), "GitHub token in Settings") {
		t.Fatalf("got %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	var tokens, manifests int32
	srv := registryServer(t, "", &tokens, &manifests)
	c := registryClient(srv, "")
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Resolve(context.Background(), "ghcr.io/o/app", "a")
	c.Resolve(context.Background(), "ghcr.io/o/app", "a")
	if tokens != 1 {
		t.Fatalf("tokens %d", tokens)
	}
	now = now.Add(290 * time.Second) // inside the 30s safety margin of the 300s token
	c.Resolve(context.Background(), "ghcr.io/o/app", "a")
	if tokens != 2 {
		t.Fatalf("expired token not refreshed: %d", tokens)
	}
}

func TestParseBearer(t *testing.T) {
	r, s, sc, ok := parseBearer(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:o/a:pull"`)
	if !ok || r != "https://ghcr.io/token" || s != "ghcr.io" || sc != "repository:o/a:pull" {
		t.Fatalf("%q %q %q %v", r, s, sc, ok)
	}
	if _, _, _, ok := parseBearer(`Basic realm="x"`); ok {
		t.Fatal("basic should not parse")
	}
}

func TestPullAuth(t *testing.T) {
	c := New(Options{Token: "t"})
	a, err := c.PullAuth(context.Background(), "ghcr.io/o/app")
	if err != nil || a != (core.RegistryAuth{Server: "ghcr.io", Username: "homelab", Password: "t"}) {
		t.Fatalf("%+v %v", a, err)
	}
	if a, _ := c.PullAuth(context.Background(), "docker.io/x/y"); a != (core.RegistryAuth{}) {
		t.Fatalf("%+v", a)
	}
	if a, _ := New(Options{}).PullAuth(context.Background(), "ghcr.io/o/app"); a != (core.RegistryAuth{}) {
		t.Fatalf("%+v", a)
	}
}
