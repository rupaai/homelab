package local

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

const (
	testBase      = 47100
	testCompanion = 47080
)

func skipIfBusy(t *testing.T, ports ...int) {
	t.Helper()
	for _, p := range ports {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			t.Skipf("port %d busy", p)
		}
		ln.Close()
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func viewerHandler(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := core.ViewerFrom(r.Context())
		io.WriteString(w, tag+":"+strconv.FormatBool(ok && v.Local))
	})
}

func TestEnsureSwapRemovePersist(t *testing.T) {
	skipIfBusy(t, testBase, testBase+1, testCompanion)
	dir := t.TempDir()
	opts := Options{Dir: dir, BasePort: testBase, CompanionPort: testCompanion}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.Ensure(ctx, "movies", viewerHandler("one")); err != nil {
		t.Fatal(err)
	}
	if err := a.Ensure(ctx, "homehub", viewerHandler("hub")); err != nil {
		t.Fatal(err)
	}
	obs := a.Observe()
	if m := obs["movies"]; !m.Ready || m.URL != "http://127.0.0.1:"+strconv.Itoa(testBase) {
		t.Fatalf("movies: %+v", m)
	}
	if h := obs["homehub"]; !h.Ready || h.URL != "http://127.0.0.1:"+strconv.Itoa(testCompanion) {
		t.Fatalf("homehub: %+v", h)
	}
	if got := get(t, obs["movies"].URL); got != "one:true" {
		t.Fatal(got)
	}
	// Swap handler, same port.
	a.Ensure(ctx, "movies", viewerHandler("two"))
	if got := get(t, obs["movies"].URL); got != "two:true" {
		t.Fatal(got)
	}
	// Remove closes the listener but keeps the port.
	a.Remove(ctx, "movies")
	if _, err := http.Get(obs["movies"].URL); err == nil {
		t.Fatal("still listening")
	}
	if _, ok := a.Observe()["movies"]; ok {
		t.Fatal("still observed")
	}
	a.Ensure(ctx, "movies-dev", viewerHandler("dev"))
	if u := a.Observe()["movies-dev"].URL; u != "http://127.0.0.1:"+strconv.Itoa(testBase+1) {
		t.Fatal(u)
	}
	a.Close()

	// A new instance remembers the assignments.
	b, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Ensure(ctx, "movies-dev", viewerHandler("dev"))
	b.Ensure(ctx, "movies", viewerHandler("again"))
	if u := b.Observe()["movies"].URL; u != "http://127.0.0.1:"+strconv.Itoa(testBase) {
		t.Fatal(u)
	}
	if u := b.Observe()["movies-dev"].URL; u != "http://127.0.0.1:"+strconv.Itoa(testBase+1) {
		t.Fatal(u)
	}
}

func TestListenFailureReported(t *testing.T) {
	skipIfBusy(t, testCompanion)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(testCompanion))
	if err != nil {
		t.Skip()
	}
	defer ln.Close()
	a, _ := New(Options{Dir: t.TempDir(), BasePort: testBase, CompanionPort: testCompanion})
	defer a.Close()
	a.Ensure(context.Background(), "homehub", viewerHandler("x"))
	h := a.Observe()["homehub"]
	if h.Ready || h.Error == "" {
		t.Fatalf("%+v", h)
	}
}
