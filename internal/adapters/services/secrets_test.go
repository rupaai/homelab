package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// secretsBackend stands in for the secrets routes of homelab-services. It records each call
// (with the caller it was signed for) and answers with a fixed status and body.
type secretsBackend struct {
	mu     sync.Mutex
	calls  []seenCall
	status int
	body   string
}

type seenCall struct {
	method, uri, body string
	caller            signed
	req               *http.Request
}

func newSecretsBackend(t *testing.T) (*Services, *secretsBackend) {
	t.Helper()
	b := &secretsBackend{status: 200, body: "{}"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.calls = append(b.calls, seenCall{method: r.Method, uri: r.URL.RequestURI(), body: string(body), req: r.Clone(context.Background())})
		status, out := b.status, b.body
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	s, err := New(Options{Port: p})
	if err != nil {
		t.Fatal(err)
	}
	s.set(true, "")
	return s, b
}

func (b *secretsBackend) answer(status int, body string) {
	b.mu.Lock()
	b.status, b.body = status, body
	b.mu.Unlock()
}

func (b *secretsBackend) last(t *testing.T, s *Services) seenCall {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		t.Fatal("nothing reached the backend")
	}
	c := b.calls[len(b.calls)-1]
	c.caller = verify(t, s.PublicKey(), c.req)
	return c
}

func (b *secretsBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func TestHomelabdCallsAreSignedAsTheSystem(t *testing.T) {
	s, b := newSecretsBackend(t)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	b.answer(200, `{"values":{"TMDB_API_KEY":"tmdb-value-123"}}`)
	got, err := s.Secrets().Inject("movies", "prod", []string{"TMDB_API_KEY", "OTHER"})
	if err != nil || got["TMDB_API_KEY"] != "tmdb-value-123" || len(got) != 1 {
		t.Fatalf("inject = %v, %v", got, err)
	}
	c := b.last(t, s)
	if c.method != "POST" || c.uri != "/internal/secrets/values" {
		t.Fatalf("call %s %s", c.method, c.uri)
	}
	if c.caller.V != 1 || c.caller.Kind != "system" || c.caller.App != "" || c.caller.Key != "" || c.caller.Person != "" || len(c.caller.Scopes) != 0 {
		t.Fatalf("caller %+v", c.caller)
	}
	// Signed for this request only.
	if c.caller.Method != "POST" || c.caller.Path != "/internal/secrets/values" || c.caller.TS != now.Unix() {
		t.Fatalf("signed for %s %s at %d", c.caller.Method, c.caller.Path, c.caller.TS)
	}
	var in struct {
		App, Env string
		Names    []string
	}
	if json.Unmarshal([]byte(c.body), &in) != nil || in.App != "movies" || in.Env != "prod" || len(in.Names) != 2 {
		t.Fatalf("body %s", c.body)
	}
}

func TestAForwardedCallIsNeverASystemCall(t *testing.T) {
	s, b := newSecretsBackend(t)
	for _, target := range []string{"/internal/secrets", "/api/secrets", "/api/files"} {
		rec := httptest.NewRecorder()
		s.Forward(rec, httptest.NewRequest("GET", target, nil), core.ServiceCaller{Kind: core.ServiceKindSystem})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
	if b.count() != 0 {
		t.Fatal("a system caller was forwarded")
	}
	// What a request brings with it can't make it a system call: the caller is chosen by
	// Homelab, and a forged header is dropped.
	req := httptest.NewRequest("GET", "/api/secrets", nil)
	req.Header.Set(HeaderCaller, "forged")
	req.Header.Set(HeaderSignature, "forged")
	req.Header.Set("X-Homelab-Kind", "system")
	rec := httptest.NewRecorder()
	s.Forward(rec, req, core.ServiceCaller{Kind: "app", App: "movies", Env: "prod", Scopes: []string{"secrets:read"}})
	c := b.last(t, s)
	if c.caller.Kind != "app" || c.req.Header.Get("X-Homelab-Kind") != "" {
		t.Fatalf("caller %+v, header %q", c.caller, c.req.Header.Get("X-Homelab-Kind"))
	}
}

func TestSecretsFailPlainlyWhileTheServiceIsDown(t *testing.T) {
	// Not running at all: nothing is even tried.
	s, _ := New(Options{Port: 7086})
	c := s.Secrets()
	calls := map[string]func() error{
		"missing":  func() error { _, err := c.Missing("movies", "prod", []string{"A"}); return err },
		"inject":   func() error { _, err := c.Inject("movies", "prod", []string{"A"}); return err },
		"platform": func() error { _, err := c.LoadPlatform("GITHUB_TOKEN"); return err },
		"set":      func() error { return c.Set("movies", "prod", "A", "v", "me") },
		"delete":   func() error { return c.Delete("movies", "prod", "A") },
		"list":     func() error { _, err := c.List("movies", "prod"); return err },
		"saved":    func() error { _, err := c.ListSaved("movies", "prod"); return err },
		"get saved": func() error {
			_, _, err := c.GetSaved("movies", "prod", "A")
			return err
		},
		"del saved": func() error { return c.DeleteSaved("movies", "prod", "A") },
		"del app":   func() error { return c.DeleteApp("movies") },
		"versions":  func() error { _, err := c.Versions("movies", "prod", "A"); return err },
		"restore":   func() error { _, err := c.Restore("movies", "prod", "A", 1, "me"); return err },
		"check":     func() error { return c.Check() },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, core.ErrSecretsUnavailable) || err.Error() != "Homelab's secrets service isn't running yet; try again in a minute." {
			t.Errorf("%s: %v", name, err)
		}
	}
	if v, ok := c.Platform("GITHUB_TOKEN"); ok || v != "" {
		t.Errorf("platform while down = %q %v", v, ok)
	}

	// Marked up, but the program doesn't answer.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s2, _ := New(Options{Port: port})
	s2.set(true, "")
	if _, err := s2.Secrets().Inject("movies", "prod", []string{"A"}); !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Errorf("stopped backend: %v", err)
	}
}

func TestAServiceThatIsUpButHasNoSecretsIsUnavailableWithAReason(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(503, `{"error":"The secrets service isn't running: the master key /h/secrets/master.key is missing. Put the key file back (from your copy)."}`)
	c := s.Secrets()
	err := c.Check()
	if !IsPermanent(err) || !strings.Contains(err.Error(), "master key") || strings.HasSuffix(err.Error(), ".") {
		t.Fatalf("check = %v", err)
	}
	if _, err := c.Inject("movies", "prod", []string{"A"}); !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("inject = %v", err)
	}
	if err := c.Set("movies", "prod", "A", "v", "me"); !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("set = %v", err)
	}
	// A service that is still starting is not permanent.
	if IsPermanent(core.ErrSecretsUnavailable) {
		t.Fatal("starting counted as permanent")
	}
}

func TestSecretsErrorsAreThePlainMessagesOfTheService(t *testing.T) {
	s, b := newSecretsBackend(t)
	c := s.Secrets()
	b.answer(400, `{"error":"A config secret name uses letters, digits and _, and doesn't start with a digit."}`)
	err := c.Set("movies", "prod", "1X", "v", "me")
	if err == nil || err.Error() != "A config secret name uses letters, digits and _, and doesn't start with a digit" || errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("set = %v", err)
	}
	b.answer(409, `{"error":"The app already saved a secret with that name. Delete that one first."}`)
	if err := c.Set("movies", "prod", "saved", "v", "me"); err == nil || !strings.Contains(err.Error(), "already saved") {
		t.Fatalf("clash = %v", err)
	}
	b.answer(500, `{"error":"Something went wrong."}`)
	if err := c.Set("movies", "prod", "A", "v", "me"); err == nil || errors.Is(err, core.ErrSecretsUnavailable) || !strings.Contains(err.Error(), "had a problem") {
		t.Fatalf("500 = %v", err)
	}
	b.answer(404, `{"error":"There's no version 9 of A."}`)
	if _, err := c.Restore("movies", "prod", "A", 9, "me"); err == nil || err.Error() != "there's no version 9 of A" {
		t.Fatalf("restore = %v", err)
	}
}

func TestSecretsCallsGoToTheRightRoutes(t *testing.T) {
	s, b := newSecretsBackend(t)
	c := s.Secrets()
	type want struct{ method, uri string }
	check := func(name string, w want) seenCall {
		t.Helper()
		got := b.last(t, s)
		if got.method != w.method || got.uri != w.uri {
			t.Errorf("%s: %s %s, want %s %s", name, got.method, got.uri, w.method, w.uri)
		}
		if got.caller.Method != w.method || got.caller.Path != w.uri {
			t.Errorf("%s: signed for %s %s", name, got.caller.Method, got.caller.Path)
		}
		return got
	}
	if err := c.Set("movies", "prod", "TMDB_API_KEY", "tmdb-value-123", "owner@example.test"); err != nil {
		t.Fatal(err)
	}
	got := check("set", want{"PUT", "/internal/secrets/item/TMDB_API_KEY?app=movies&env=prod"})
	var put struct{ Value, Kind, By string }
	if json.Unmarshal([]byte(got.body), &put) != nil || put.Value != "tmdb-value-123" || put.Kind != "config" || put.By != "owner@example.test" {
		t.Errorf("set body %s", got.body)
	}
	if err := c.Set("", "", "GITHUB_TOKEN", "ghp_x", "owner"); err != nil {
		t.Fatal(err)
	}
	check("set platform", want{"PUT", "/internal/secrets/item/GITHUB_TOKEN"})
	if err := c.Delete("movies", "prod", "TMDB_API_KEY"); err != nil {
		t.Fatal(err)
	}
	check("delete", want{"DELETE", "/internal/secrets/item/TMDB_API_KEY?app=movies&env=prod&kind=config"})
	if err := c.DeleteSaved("movies", "prod", "google_token:u1"); err != nil {
		t.Fatal(err)
	}
	check("delete saved", want{"DELETE", "/internal/secrets/item/google_token:u1?app=movies&env=prod&kind=app"})
	if err := c.DeleteApp("movies"); err != nil {
		t.Fatal(err)
	}
	check("delete app", want{"DELETE", "/internal/secrets/app/movies"})
	b.answer(200, `{"versions":[{"version":3,"set_at":1800000000,"set_by":"owner"},{"version":2,"set_at":1799990000,"set_by":"homelab"}]}`)
	vs, err := c.Versions("movies", "prod", "TMDB_API_KEY")
	if err != nil || len(vs) != 2 || vs[0].Version != 3 || vs[0].SetBy != "owner" || vs[0].SetAt.Unix() != 1_800_000_000 {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	check("versions", want{"GET", "/internal/secrets/item/TMDB_API_KEY/versions?app=movies&env=prod"})
	b.answer(200, `{"version":4}`)
	if n, err := c.Restore("movies", "prod", "TMDB_API_KEY", 2, "owner"); err != nil || n != 4 {
		t.Fatalf("restore = %d, %v", n, err)
	}
	got = check("restore", want{"POST", "/internal/secrets/item/TMDB_API_KEY/restore?app=movies&env=prod"})
	if !strings.Contains(got.body, `"version":2`) {
		t.Errorf("restore body %s", got.body)
	}
	b.answer(200, `{"secrets":[{"name":"A","kind":"config","version":2,"updated_at":1800000000,"updated_by":"owner"}]}`)
	list, err := c.List("movies", "prod")
	if err != nil || len(list) != 1 || list[0].Name != "A" || list[0].Version != 2 || list[0].UpdatedBy != "owner" || list[0].UpdatedAt.Unix() != 1_800_000_000 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	check("list", want{"GET", "/internal/secrets?app=movies&env=prod&kind=config"})
	if _, err := c.ListSaved("movies", "prod"); err != nil {
		t.Fatal(err)
	}
	check("list saved", want{"GET", "/internal/secrets?app=movies&env=prod&kind=app"})
	b.answer(200, `{"missing":["B"]}`)
	if m, err := c.Missing("movies", "prod", []string{"A", "B"}); err != nil || len(m) != 1 || m[0] != "B" {
		t.Fatalf("missing = %v, %v", m, err)
	}
	check("missing", want{"POST", "/internal/secrets/missing"})
	// Names with odd characters are escaped, and still signed as sent.
	b.answer(200, `{"name":"a b/c","kind":"app","value":"v","version":1}`)
	if v, ok, err := c.GetSaved("movies", "prod", "a b/c"); err != nil || !ok || v != "v" {
		t.Fatalf("get saved = %q %v %v", v, ok, err)
	}
	check("get saved", want{"GET", "/internal/secrets/item/a%20b%2Fc?app=movies&env=prod"})
}

func TestGetSavedNeverReturnsAConfigSecret(t *testing.T) {
	s, b := newSecretsBackend(t)
	c := s.Secrets()
	b.answer(200, `{"name":"TMDB_API_KEY","kind":"config","value":"tmdb-value-123","version":1}`)
	if v, ok, err := c.GetSaved("movies", "prod", "TMDB_API_KEY"); err != nil || ok || v != "" {
		t.Fatalf("got %q %v %v", v, ok, err)
	}
	b.answer(404, `{"error":"There's no such secret."}`)
	if v, ok, err := c.GetSaved("movies", "prod", "nope"); err != nil || ok || v != "" {
		t.Fatalf("missing: %q %v %v", v, ok, err)
	}
}

func TestPlatformValuesAreRememberedBrieflyAndForgottenOnChange(t *testing.T) {
	s, b := newSecretsBackend(t)
	c := s.Secrets()
	b.answer(200, `{"values":{"GITHUB_TOKEN":"ghp_1"}}`)
	for i := 0; i < 3; i++ {
		if v, ok := c.Platform("GITHUB_TOKEN"); !ok || v != "ghp_1" {
			t.Fatalf("platform = %q %v", v, ok)
		}
	}
	if b.count() != 1 {
		t.Fatalf("%d calls for 3 reads", b.count())
	}
	b.answer(200, `{"values":{"GITHUB_TOKEN":"ghp_2"}}`)
	if err := c.Set("", "", "GITHUB_TOKEN", "ghp_2", "owner"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Platform("GITHUB_TOKEN"); v != "ghp_2" {
		t.Fatalf("after a change = %q", v)
	}
	// A value that isn't set is "", not an error, and is remembered too.
	b.answer(200, `{"values":{}}`)
	if v, err := c.LoadPlatform("OTHER"); err != nil || v != "" {
		t.Fatalf("unset = %q, %v", v, err)
	}
	// A read failure is an error, never "not set", and is not remembered.
	b.answer(503, `{"error":"The secrets service isn't running: x."}`)
	if _, err := c.LoadPlatform("NEW_ONE"); !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("down = %v", err)
	}
}

func TestInjectNeedsAnApp(t *testing.T) {
	s, b := newSecretsBackend(t)
	if _, err := s.Secrets().Inject("", "", []string{"A"}); err == nil {
		t.Fatal("Inject without an app was allowed")
	}
	if b.count() != 0 {
		t.Fatal("it asked the service anyway")
	}
}

func TestSecretsKeyIsPassedAsAPathNotAValue(t *testing.T) {
	// The environment of the child names the key file and the old store; it never holds a key.
	dir := t.TempDir()
	script := filepath.Join(dir, "homelab-services")
	out := filepath.Join(dir, "env.txt")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nenv > "+out+"\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Program: script, DataDir: dir, Port: 1, SecretsKey: "/h/secrets/master.key", SecretsImport: "/h/secrets/secrets.db"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	b, _ := os.ReadFile(out)
	env := string(b)
	if !strings.Contains(env, "HOMELAB_SERVICES_SECRETS_KEY=/h/secrets/master.key\n") || !strings.Contains(env, "HOMELAB_SERVICES_SECRETS_IMPORT=/h/secrets/secrets.db\n") {
		t.Fatalf("env:\n%s", env)
	}
}

// ---- the real homelab-services ----

// startReal runs the real program (HOMELAB_SERVICES_BIN) and waits for it.
func startReal(t *testing.T, o Options) *Services {
	t.Helper()
	bin := os.Getenv("HOMELAB_SERVICES_BIN")
	if bin == "" {
		t.Skip("set HOMELAB_SERVICES_BIN to a built homelab-services")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	o.Port = ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	o.Program = bin
	if o.DataDir == "" {
		o.DataDir = filepath.Join(t.TempDir(), "services")
	}
	o.Storage = []core.Disk{{Name: "main", Path: t.TempDir()}}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("homelab-services didn't stop")
		}
	})
	for i := 0; !s.Up(); i++ {
		if i > 100 {
			t.Fatalf("never came up: %s", s.Problem())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s
}

func fixtureCopy(t *testing.T) (key, oldDB string) {
	t.Helper()
	src := filepath.Join("..", "..", "..", "services", "homelab-services", "tests", "fixtures", "go-secrets")
	dir := t.TempDir()
	key, oldDB = filepath.Join(dir, "master.key"), filepath.Join(dir, "secrets.db")
	for from, to := range map[string]string{"master.key": key, "secrets.sqlite": oldDB} {
		b, err := os.ReadFile(filepath.Join(src, from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return key, oldDB
}

func TestIntegrationSecrets(t *testing.T) {
	key, oldDB := fixtureCopy(t)
	before, _ := os.ReadFile(oldDB)
	s := startReal(t, Options{SecretsKey: key, SecretsImport: oldDB})
	c := s.Secrets()
	if err := c.Check(); err != nil {
		t.Fatalf("check: %v", err)
	}

	// Imported from the file the real Go store made.
	got, err := c.Inject("movies", "prod", []string{"TMDB_API_KEY", "HOMELAB_TOKEN", "NOPE"})
	if err != nil || got["TMDB_API_KEY"] != "tmdb-v7" || got["HOMELAB_TOKEN"] != "hlk_fixture" || len(got) != 2 {
		t.Fatalf("inject = %v, %v", got, err)
	}
	if v, ok := c.Platform("GITHUB_TOKEN"); !ok || v != "ghp_fixture_token" {
		t.Fatalf("platform = %q %v", v, ok)
	}
	vs, err := c.Versions("movies", "prod", "TMDB_API_KEY")
	if err != nil || len(vs) != 5 || vs[0].Version != 7 || vs[4].Version != 3 || vs[0].SetBy != "fixture" {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if after, _ := os.ReadFile(oldDB); string(after) != string(before) {
		t.Fatal("the old secrets file was changed")
	}

	// What homelabd does with config secrets.
	if err := c.Set("movies", "prod", "NEW_ONE", "new-value-1", "owner@example.test"); err != nil {
		t.Fatal(err)
	}
	if m, err := c.Missing("movies", "prod", []string{"NEW_ONE", "TMDB_API_KEY", "ABSENT"}); err != nil || len(m) != 1 || m[0] != "ABSENT" {
		t.Fatalf("missing = %v, %v", m, err)
	}
	if err := c.Set("movies", "prod", "1BAD", "v", "x"); err == nil || !strings.Contains(err.Error(), "doesn't start with a digit") {
		t.Fatalf("bad name = %v", err)
	}
	if n, err := c.Restore("movies", "prod", "TMDB_API_KEY", 5, "owner"); err != nil || n != 8 {
		t.Fatalf("restore = %d, %v", n, err)
	}
	if got, _ := c.Inject("movies", "prod", []string{"TMDB_API_KEY"}); got["TMDB_API_KEY"] != "tmdb-v5" {
		t.Fatalf("restored value = %v", got)
	}
	if _, err := c.Restore("movies", "prod", "TMDB_API_KEY", 1, "owner"); err == nil || !strings.Contains(err.Error(), "no version 1") {
		t.Fatalf("restore of a version that is gone = %v", err)
	}

	// The same secrets through the app door's path: Homelab forwards the app's key, and
	// homelab-services applies its own rules.
	call := func(who core.ServiceCaller, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.Forward(rec, req, who)
		return rec
	}
	reader := core.ServiceCaller{Kind: "app", Key: "k", App: "movies", Env: "prod", Scopes: []string{"users:read", "secrets:read"}}
	writer := core.ServiceCaller{Kind: "app", Key: "k", App: "movies", Env: "prod", Scopes: []string{"users:read", "secrets:read", "secrets:write"}}

	rec := call(reader, "GET", "/api/secrets", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"TMDB_API_KEY"`) || strings.Contains(rec.Body.String(), "HOMELAB_TOKEN") || strings.Contains(rec.Body.String(), "tmdb-v") {
		t.Fatalf("app list: %d %s", rec.Code, rec.Body)
	}
	rec = call(reader, "GET", "/api/secrets/TMDB_API_KEY", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":"tmdb-v5"`) {
		t.Fatalf("app get: %d %s", rec.Code, rec.Body)
	}
	if rec := call(reader, "PUT", "/api/secrets/google_token:u1", `{"value":"tok-1"}`); rec.Code != 403 {
		t.Fatalf("a read-only app saved a secret: %d %s", rec.Code, rec.Body)
	}
	if rec := call(writer, "PUT", "/api/secrets/google_token:u1", `{"value":"tok-1"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("app put: %d %s", rec.Code, rec.Body)
	}
	if rec := call(writer, "PUT", "/api/secrets/TMDB_API_KEY", `{"value":"mine"}`); rec.Code != 403 {
		t.Fatalf("an app changed a config secret: %d %s", rec.Code, rec.Body)
	}
	if rec := call(writer, "DELETE", "/api/secrets/TMDB_API_KEY", ""); rec.Code != 403 {
		t.Fatalf("an app deleted a config secret: %d %s", rec.Code, rec.Body)
	}
	if rec := call(writer, "GET", "/api/secrets/GITHUB_TOKEN", ""); rec.Code != 404 || strings.Contains(rec.Body.String(), "ghp_") {
		t.Fatalf("an app read a platform secret: %d %s", rec.Code, rec.Body)
	}
	other := core.ServiceCaller{Kind: "app", Key: "k", App: "notes", Env: "prod", Scopes: writer.Scopes}
	if rec := call(other, "GET", "/api/secrets/google_token:u1", ""); rec.Code != 404 {
		t.Fatalf("another app read it: %d %s", rec.Code, rec.Body)
	}
	for name, who := range map[string]core.ServiceCaller{
		"person":      {Kind: "person", Person: "owner"},
		"admin key":   {Kind: "admin", Key: "k", Person: "owner", Scopes: []string{"files:read", "files:write", "users:read", "secrets:read", "secrets:write"}},
		"system":      {Kind: "system"},
		"app, no env": {Kind: "app", App: "movies", Scopes: writer.Scopes},
	} {
		for _, path := range []string{"/api/secrets", "/api/secrets/TMDB_API_KEY", "/internal/secrets", "/internal/secrets/item/TMDB_API_KEY?app=movies&env=prod"} {
			if rec := call(who, "GET", path, ""); rec.Code != 403 || strings.Contains(rec.Body.String(), "tmdb-v") {
				t.Errorf("%s on %s = %d %s", name, path, rec.Code, rec.Body)
			}
		}
	}
	if rec := call(writer, "GET", "/internal/secrets/item/TMDB_API_KEY?app=movies&env=prod", ""); rec.Code != 403 {
		t.Fatalf("an app used an internal route: %d", rec.Code)
	}

	// What the companion shows of what the app saved.
	list, err := c.ListSaved("movies", "prod")
	if err != nil || len(list) != 1 || list[0].Name != "google_token:u1" || list[0].UpdatedBy != "app:movies/prod" {
		t.Fatalf("saved = %+v, %v", list, err)
	}
	if v, ok, err := c.GetSaved("movies", "prod", "google_token:u1"); err != nil || !ok || v != "tok-1" {
		t.Fatalf("get saved = %q %v %v", v, ok, err)
	}
	if v, ok, _ := c.GetSaved("movies", "prod", "TMDB_API_KEY"); ok || v != "" {
		t.Fatalf("a config secret came out as saved: %q", v)
	}
	// A saved secret doesn't count as a config value, and isn't given to a deploy.
	if got, _ := c.Inject("movies", "prod", []string{"google_token:u1"}); len(got) != 0 {
		t.Fatalf("inject gave a saved secret: %v", got)
	}
	// The owner can't take over the name of a saved secret, and clearing the config view
	// can't delete it.
	if err := c.Set("movies", "prod", "google_token", "v", "x"); err != nil {
		t.Fatalf("a different name: %v", err)
	}
	if err := c.Delete("movies", "prod", "google_token:u1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.GetSaved("movies", "prod", "google_token:u1"); !ok {
		t.Fatal("deleting a config secret removed a saved one")
	}
	if err := c.DeleteSaved("movies", "prod", "google_token:u1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.GetSaved("movies", "prod", "google_token:u1"); ok {
		t.Fatal("still there")
	}
	if err := c.DeleteApp("movies"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Inject("movies", "prod", []string{"TMDB_API_KEY"}); len(got) != 0 {
		t.Fatalf("after removing the app: %v", got)
	}
	if v, ok := c.Platform("GITHUB_TOKEN"); !ok || v != "ghp_fixture_token" {
		t.Fatalf("removing an app removed a platform secret: %q", v)
	}
}

func TestIntegrationWithoutAKeyOnlyTheSecretsAreOff(t *testing.T) {
	s := startReal(t, Options{SecretsKey: filepath.Join(t.TempDir(), "missing.key")})
	err := s.Secrets().Check()
	if !IsPermanent(err) || !strings.Contains(err.Error(), "master key") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("check = %v", err)
	}
	if _, err := s.Secrets().Inject("movies", "prod", []string{"A"}); !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("inject = %v", err)
	}
	// An app is told briefly; the reason is for homelabd.
	rec := httptest.NewRecorder()
	s.Forward(rec, httptest.NewRequest("GET", "/api/secrets", nil), core.ServiceCaller{Kind: "app", App: "movies", Env: "prod", Scopes: []string{"secrets:read"}})
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "master") {
		t.Fatalf("app: %d %s", rec.Code, rec.Body)
	}
	// Files still work.
	rec = httptest.NewRecorder()
	s.Forward(rec, httptest.NewRequest("GET", "/api/files/folders", nil), core.ServiceCaller{Kind: "person", Person: "p"})
	if rec.Code != 200 {
		t.Fatalf("files: %d %s", rec.Code, rec.Body)
	}
}

func TestIntegrationAWrongKeyIsRefusedAndNothingIsImported(t *testing.T) {
	_, oldDB := fixtureCopy(t)
	wrong := filepath.Join(t.TempDir(), "wrong.key")
	if err := os.WriteFile(wrong, []byte("CQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQk=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := startReal(t, Options{SecretsKey: wrong, SecretsImport: oldDB})
	err := s.Secrets().Check()
	if !IsPermanent(err) || !strings.Contains(err.Error(), "importing the old secrets") {
		t.Fatalf("check = %v", err)
	}
}
