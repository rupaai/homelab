package services

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

func TestProgramSitsNextToHomelabd(t *testing.T) {
	for exe, want := range map[string]string{
		"/var/lib/homelab/bin/homelabd-build-7": "/var/lib/homelab/bin/homelab-services-build-7",
		"/home/me/homelab/bin/homelabd":         "/home/me/homelab/bin/homelab-services",
		"/tmp/dist/homelabd-linux-amd64":        "/tmp/dist/homelab-services-linux-amd64",
		"/tmp/go-build/exe/main":                "/tmp/go-build/exe/homelab-services",
	} {
		if got := Program(exe); got != want {
			t.Errorf("Program(%q)=%q want %q", exe, got, want)
		}
	}
}

// backend stands in for homelab-services and records what reaches it.
func backend(t *testing.T) (*Services, *http.Request) {
	t.Helper()
	got := new(http.Request)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = *r.Clone(context.Background())
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	s, err := New(Options{Port: p})
	if err != nil {
		t.Fatal(err)
	}
	s.set(true, "")
	return s, got
}

// verify checks a forwarded request's caller the way homelab-services does.
func verify(t *testing.T, pub string, r *http.Request) signed {
	t.Helper()
	key, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil || len(key) != ed25519.PublicKeySize {
		t.Fatalf("public key %q: %v", pub, err)
	}
	value := r.Header.Get(HeaderCaller)
	sig, err := base64.RawURLEncoding.DecodeString(r.Header.Get(HeaderSignature))
	if err != nil || !ed25519.Verify(key, []byte(signContext+value), sig) {
		t.Fatalf("signature doesn't verify: %v", err)
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	var c signed
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestForwardSignsTheCallerForThisRequestOnly(t *testing.T) {
	s, got := backend(t)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	req := httptest.NewRequest("POST", "/api/files/photos?tag=cat%20pics", strings.NewReader("{}"))
	req.Header.Set("Cookie", "homelab_session=abc")
	req.Header.Set("Authorization", "Bearer hlk_x")
	req.Header.Set("X-Homelab-Caller", "forged")
	req.Header.Set("X-Homelab-Signature", "forged")
	req.Header.Set("x-homelab-person", "owner")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Forward(rec, req, core.ServiceCaller{Kind: "app", Key: "k1", App: "movies", Env: "dev", Scopes: []string{"users:read"}})
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	for _, h := range []string{"Cookie", "Authorization", "X-Homelab-Person"} {
		if got.Header.Get(h) != "" {
			t.Errorf("%s reached the services", h)
		}
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Error("other headers were dropped too")
	}
	if len(got.Header.Values(HeaderCaller)) != 1 || len(got.Header.Values(HeaderSignature)) != 1 {
		t.Fatalf("caller headers: %v", got.Header)
	}
	c := verify(t, s.PublicKey(), got)
	if c.V != 1 || c.Kind != "app" || c.Key != "k1" || c.App != "movies" || c.Env != "dev" || len(c.Scopes) != 1 {
		t.Errorf("caller %+v", c)
	}
	if c.Method != "POST" || c.Path != "/api/files/photos?tag=cat%20pics" || c.Path != got.URL.RequestURI() || c.TS != now.Unix() {
		t.Errorf("caller is for %s %s at %d; the request was %s %s", c.Method, c.Path, c.TS, got.Method, got.URL.RequestURI())
	}
}

func TestPersonCallerHasEmptyScopesNotNull(t *testing.T) {
	s, got := backend(t)
	s.Forward(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/db", nil), core.ServiceCaller{Kind: "person", Person: "p1"})
	b, _ := base64.RawURLEncoding.DecodeString(got.Header.Get(HeaderCaller))
	if !strings.Contains(string(b), `"scopes":[]`) || !strings.Contains(string(b), `"person":"p1"`) {
		t.Fatalf("caller json %s", b)
	}
}

func TestEachStartHasItsOwnKey(t *testing.T) {
	a, _ := New(Options{Port: 7086})
	b, _ := New(Options{Port: 7086})
	if a.PublicKey() == b.PublicKey() {
		t.Fatal("two starts share a signing key")
	}
}

func TestForwardWhileDownIs503(t *testing.T) {
	s, _ := New(Options{Port: 7086})
	rec := httptest.NewRecorder()
	s.Forward(rec, httptest.NewRequest("GET", "/api/files", nil), core.ServiceCaller{Kind: "person"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "aren't running") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if s.Problem() == "" {
		t.Fatal("no problem while down")
	}
}

func TestMissingProgramIsAProblemNotACrash(t *testing.T) {
	s, _ := New(Options{Program: filepath.Join(t.TempDir(), "homelab-services"), Port: 7086})
	s.Run(context.Background()) // returns at once
	if p := s.Problem(); !strings.Contains(p, "aren't installed") {
		t.Fatalf("problem %q", p)
	}
}

func TestStoppedBackendIs503(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s2, _ := New(Options{Port: port})
	s2.set(true, "")
	rec := httptest.NewRecorder()
	s2.Forward(rec, httptest.NewRequest("GET", "/api/data/x", nil), core.ServiceCaller{Kind: "person"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", rec.Code)
	}
}

func TestLineLoggerSplitsLines(t *testing.T) {
	var buf bytes.Buffer
	w := &lineLogger{log: slog.New(slog.NewTextHandler(&buf, nil))}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\n\n"))
	out := buf.String()
	if strings.Count(out, "\n") != 2 || !strings.Contains(out, `msg="homelab-services: one"`) || !strings.Contains(out, `msg="homelab-services: two"`) {
		t.Fatalf("%s", out)
	}
}

// TestIntegration runs the real homelab-services when HOMELAB_SERVICES_BIN names it.
func TestIntegration(t *testing.T) {
	bin := os.Getenv("HOMELAB_SERVICES_BIN")
	if bin == "" {
		t.Skip("set HOMELAB_SERVICES_BIN to a built homelab-services")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	disk := t.TempDir()
	s, err := New(Options{Program: bin, DataDir: filepath.Join(t.TempDir(), "services"), Port: port,
		Storage: []core.Disk{{Name: "main", Path: disk}}, MaxUpload: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("homelab-services didn't stop")
		}
	}()
	for i := 0; !s.Up(); i++ {
		if i > 100 {
			t.Fatalf("never came up: %s", s.Problem())
		}
		time.Sleep(100 * time.Millisecond)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	// Signed by Homelab: the caller is accepted, and nothing lives there yet.
	rec := httptest.NewRecorder()
	s.Forward(rec, httptest.NewRequest("GET", "/api/files/photos?x=1", nil), core.ServiceCaller{Kind: "admin", Key: "k", Person: "p"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("signed call: %d %s", rec.Code, rec.Body)
	}
	// Straight to the program, without Homelab: refused.
	res, err := http.Get(base + "/api/files/photos")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned call: %d", res.StatusCode)
	}
	// A real signature replayed on another path: refused.
	got := new(http.Request)
	rec2 := httptest.NewRecorder()
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { *got = *r.Clone(context.Background()) }))
	defer capture.Close()
	_, cport, _ := net.SplitHostPort(strings.TrimPrefix(capture.URL, "http://"))
	cp, _ := strconv.Atoi(cport)
	spy, _ := New(Options{Port: cp})
	spy.key = s.key
	spy.set(true, "")
	spy.Forward(rec2, httptest.NewRequest("GET", "/api/files/a", nil), core.ServiceCaller{Kind: "person", Person: "p"})
	req, _ := http.NewRequest("GET", base+"/api/files/b", nil)
	req.Header.Set(HeaderCaller, got.Header.Get(HeaderCaller))
	req.Header.Set(HeaderSignature, got.Header.Get(HeaderSignature))
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed on another path: %d", res.StatusCode)
	}
	// The same signature on its own path works, so the refusal above was the path.
	req, _ = http.NewRequest("GET", base+"/api/files/a", nil)
	req.Header.Set(HeaderCaller, got.Header.Get(HeaderCaller))
	req.Header.Set(HeaderSignature, got.Header.Get(HeaderSignature))
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("same path: %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(s.o.DataDir)); err != nil {
		t.Fatalf("data folder: %v", err)
	}

	// Files, end to end: a folder, an upload, a list, part of a download.
	person := core.ServiceCaller{Kind: "person", Person: "p"}
	call := func(c core.ServiceCaller, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		s.Forward(rec, req, c)
		return rec
	}
	if rec := call(person, "POST", "/api/files/folders", `{"name":"photos"}`, "Content-Type", "application/json"); rec.Code != http.StatusCreated {
		t.Fatalf("new folder: %d %s", rec.Code, rec.Body)
	}
	rec = call(person, "POST", "/api/files/photos?name=../note.txt&tags=cats", "hello, files")
	var up struct{ ID, Name string }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &up) != nil || up.Name != "_note.txt" {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := call(person, "GET", "/api/files/photos?tag=cats", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), up.ID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := call(person, "GET", "/api/files/photos/"+up.ID, "", "Range", "bytes=7-"); rec.Code != http.StatusPartialContent || rec.Body.String() != "files" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body)
	}
	if rec := call(person, "POST", "/api/files/photos?name=big", strings.Repeat("x", 2<<20)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body)
	}
	// Another app's key sees nothing there.
	if rec := call(core.ServiceCaller{Kind: "app", App: "movies", Env: "dev"}, "GET", "/api/files/photos/"+up.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other app: %d %s", rec.Code, rec.Body)
	}
	matches, _ := filepath.Glob(filepath.Join(disk, "homelab-files", "photos", "*", "*", up.ID+"-_note.txt"))
	if len(matches) != 1 {
		t.Fatalf("stored file not where expected: %v", matches)
	}
}
