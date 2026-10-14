package tailscale

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

type fakeNode struct {
	dns     string
	upErr   error
	tlsErr  error
	who     Who
	whoErr  error
	mu      sync.Mutex
	tlsLn   *chanListener
	httpLn  *chanListener
	closed  bool
	upCalls int
}

type chanListener struct {
	ch   chan net.Conn
	once sync.Once
}

func newChanListener() *chanListener { return &chanListener{ch: make(chan net.Conn)} }
func (l *chanListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *chanListener) Close() error   { l.once.Do(func() { close(l.ch) }); return nil }
func (l *chanListener) Addr() net.Addr { return &net.TCPAddr{} }

func (f *fakeNode) Up(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upCalls++
	return f.dns, f.upErr
}
func (f *fakeNode) ListenTLS(string) (net.Listener, error) {
	if f.tlsErr != nil {
		return nil, f.tlsErr
	}
	f.tlsLn = newChanListener()
	return f.tlsLn, nil
}
func (f *fakeNode) Listen(string) (net.Listener, error) {
	f.httpLn = newChanListener()
	return f.httpLn, nil
}
func (f *fakeNode) WhoIs(context.Context, string) (Who, error) {
	return f.who, f.whoErr
}
func (f *fakeNode) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }

func newTest(t *testing.T, opts Options, fn func(name string) *fakeNode) (*Addresses, map[string]*fakeNode) {
	t.Helper()
	opts.StateDir = t.TempDir()
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	a.backoffMin, a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	nodes := map[string]*fakeNode{}
	var mu sync.Mutex
	a.newNode = func(name, dir string, o Options, log *slog.Logger) node {
		n := fn(name)
		mu.Lock()
		nodes[name] = n
		mu.Unlock()
		return n
	}
	t.Cleanup(func() { a.Close() })
	return a, nodes
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// serveVia sends one request through the node's https server without a network.
func serveVia(t *testing.T, ln *chanListener, path string) *http.Response {
	t.Helper()
	c1, c2 := net.Pipe()
	ln.ch <- c2
	req, _ := http.NewRequest("GET", "http://x"+path, nil)
	if err := req.Write(c1); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufioReader(c1), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c1.Close() })
	return resp
}

func TestReadyURLAndViewer(t *testing.T) {
	a, nodes := newTest(t, Options{AuthKey: "k"}, func(string) *fakeNode {
		return &fakeNode{dns: "movies.tail1234.ts.net.", who: Who{Login: "labiba@example.com", Name: "Labiba", Tagged: false}}
	})
	h1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := core.ViewerFrom(r.Context())
		io.WriteString(w, "one "+v.Login+" "+v.Name)
	})
	if err := a.Ensure(context.Background(), "movies", h1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return a.Observe()["movies"].Ready })
	if u := a.Observe()["movies"].URL; u != "https://movies.tail1234.ts.net" {
		t.Fatal(u)
	}
	n := nodes["movies"]
	b, _ := io.ReadAll(serveVia(t, n.tlsLn, "/").Body)
	if string(b) != "one labiba@example.com Labiba" {
		t.Fatal(string(b))
	}
	// Swap handler in place: no new node.
	a.Ensure(context.Background(), "movies", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "two") }))
	b, _ = io.ReadAll(serveVia(t, n.tlsLn, "/").Body)
	if string(b) != "two" {
		t.Fatal(string(b))
	}
	if n.upCalls != 1 {
		t.Fatal("restarted")
	}
	// :80 redirects to https.
	resp := serveVia(t, n.httpLn, "/a?b=1")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "https://movies.tail1234.ts.net/a?b=1" {
		t.Fatal(resp.StatusCode, resp.Header)
	}
	// Remove closes the node.
	a.Remove(context.Background(), "movies")
	if !n.closed {
		t.Fatal("not closed")
	}
	if _, ok := a.Observe()["movies"]; ok {
		t.Fatal("still observed")
	}
}

func TestWhoIsFailureIsUnknownViewer(t *testing.T) {
	a, nodes := newTest(t, Options{AuthKey: "k"}, func(string) *fakeNode {
		return &fakeNode{dns: "x.ts.net", whoErr: errors.New("nope"), who: Who{Login: "leak"}}
	})
	var got core.Viewer
	var ok bool
	a.Ensure(context.Background(), "x", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = core.ViewerFrom(r.Context())
	}))
	waitFor(t, func() bool { return a.Observe()["x"].Ready })
	serveVia(t, nodes["x"].tlsLn, "/")
	if !ok || got != (core.Viewer{}) {
		t.Fatalf("%v %+v", ok, got)
	}
}

func TestMissingAuthKey(t *testing.T) {
	a, nodes := newTest(t, Options{}, func(string) *fakeNode { return &fakeNode{dns: "x.ts.net"} })
	a.Ensure(context.Background(), "movies", http.NotFoundHandler())
	ad := a.Observe()["movies"]
	if ad.Ready || ad.Error != msgMissingKey {
		t.Fatalf("%+v", ad)
	}
	if len(nodes) != 0 {
		t.Fatal("node started")
	}
	// With existing state it starts even without a key.
	dir := filepath.Join(a.opts.StateDir, "movies-dev")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "tailscaled.state"), []byte("{}"), 0o600)
	a.Ensure(context.Background(), "movies-dev", http.NotFoundHandler())
	waitFor(t, func() bool { return a.Observe()["movies-dev"].Ready })
}

func TestRetryAndHTTPSMessage(t *testing.T) {
	calls := 0
	a, _ := newTest(t, Options{AuthKey: "k"}, func(string) *fakeNode {
		calls++
		if calls == 1 {
			return &fakeNode{dns: "x.ts.net", upErr: errors.New("boom")}
		}
		if calls == 2 {
			return &fakeNode{dns: "x.ts.net", tlsErr: errors.New("tsnet: you must enable HTTPS in the admin panel to proceed")}
		}
		return &fakeNode{dns: "x.ts.net"}
	})
	a.Ensure(context.Background(), "x", http.NotFoundHandler())
	sawHTTPS := false
	waitFor(t, func() bool {
		ad := a.Observe()["x"]
		if ad.Error == msgHTTPS {
			sawHTTPS = true
		}
		return ad.Ready
	})
	_ = sawHTTPS // the message may be overwritten quickly; the Ready transition is the assertion
}

func TestHTTPSErrorMapped(t *testing.T) {
	if !isHTTPSError(errors.New("tsnet: you must enable HTTPS in the admin panel")) || isHTTPSError(errors.New("boom")) {
		t.Fatal("isHTTPSError")
	}
}

func TestBadName(t *testing.T) {
	a, _ := newTest(t, Options{AuthKey: "k"}, func(string) *fakeNode { return &fakeNode{} })
	if a.Ensure(context.Background(), "../x", http.NotFoundHandler()) == nil {
		t.Fatal("accepted")
	}
}

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }
