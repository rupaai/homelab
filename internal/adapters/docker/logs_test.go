package docker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// fakeCont is a container the fake Engine lists.
type fakeCont struct {
	id     string
	labels map[string]string
	tty    bool
	body   []byte // what the log stream sends
}

// fakeEngine is a Docker Engine that only knows the calls the log follower makes.
type fakeEngine struct {
	t     *testing.T
	mu    sync.Mutex
	conts []fakeCont
	// asked records "METHOD path" of every call, and logQ the query of each logs call by id.
	asked []string
	logQ  map[string][]qvals
	srv   *httptest.Server
}

type qvals map[string][]string

func newFakeEngine(t *testing.T, conts ...fakeCont) *fakeEngine {
	f := &fakeEngine{t: t, conts: conts, logQ: map[string][]qvals{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.asked = append(f.asked, r.Method+" "+r.URL.Path)
	conts := append([]fakeCont(nil), f.conts...)
	f.mu.Unlock()
	if r.Method != http.MethodGet {
		f.t.Errorf("the follower sent %s %s", r.Method, r.URL.Path)
		http.Error(w, "read only", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/containers/json" {
		// A real Engine applies the label filter; this one returns everything, so the
		// follower's own checks are what is tested.
		var out []map[string]any
		for _, c := range conts {
			out = append(out, map[string]any{"Id": c.id, "State": "running", "Labels": c.labels})
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "containers" {
		http.NotFound(w, r)
		return
	}
	var c *fakeCont
	for i := range conts {
		if conts[i].id == parts[1] {
			c = &conts[i]
		}
	}
	if c == nil {
		http.NotFound(w, r)
		return
	}
	switch parts[2] {
	case "json":
		_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Tty": c.tty}})
	case "logs":
		f.mu.Lock()
		f.logQ[c.id] = append(f.logQ[c.id], qvals(r.URL.Query()))
		f.mu.Unlock()
		_, _ = w.Write(c.body)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // follow=1: the stream stays open
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeEngine) askedFor(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.asked {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

func (f *fakeEngine) queries(id string) []qvals {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]qvals(nil), f.logQ[id]...)
}

// frame is one piece of Docker's multiplexed stream.
func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

var appLabels = map[string]string{labelApp: "movies", labelEnv: "prod"}

// sinkRec is a LogSink that remembers what it is given.
type sinkRec struct {
	mu       sync.Mutex
	batches  []core.LogBatch
	fail     bool
	attempts int
}

func (s *sinkRec) sink(_ context.Context, b core.LogBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.fail {
		return fmt.Errorf("the log store is down")
	}
	s.batches = append(s.batches, b)
	return nil
}

func (s *sinkRec) setFail(v bool) { s.mu.Lock(); s.fail = v; s.mu.Unlock() }

func (s *sinkRec) tries() int { s.mu.Lock(); defer s.mu.Unlock(); return s.attempts }

func (s *sinkRec) lines() []core.LogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.LogLine
	for _, b := range s.batches {
		out = append(out, b.Lines...)
	}
	return out
}

func (s *sinkRec) wait(t *testing.T, n int) []core.LogLine {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l := s.lines(); len(l) >= n {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wanted %d lines, got %d: %+v", n, len(s.lines()), s.lines())
	return nil
}

// start runs a follower against the fake Engine. stop ends it and waits for it to return.
func start(t *testing.T, f *fakeEngine, s *sinkRec, cursorPath string, tune func(*LogFollower)) (stop func()) {
	t.Helper()
	api, err := newEngine("tcp://" + strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	lf := newLogFollower(api, s.sink, cursorPath, nil)
	lf.poll, lf.flush = 20*time.Millisecond, 20*time.Millisecond
	if tune != nil {
		tune(lf)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); lf.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("the follower did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func ts(sec int, nanos int) string {
	return time.Date(2026, 1, 1, 0, 0, sec, nanos, time.UTC).Format(time.RFC3339Nano)
}

func TestFollowerDemultiplexesFramesWithTimestampsAndStreams(t *testing.T) {
	body := concat(
		frame(1, ts(0, 123456789)+" hello\n"),
		frame(2, ts(1, 0)+" oops\n"),
		frame(1, ts(2, 5000000)+" par"),                       // a line cut across two frames
		frame(2, ts(3, 0)+" err one\n"+ts(3, 1)+" err two\n"), // two lines in one frame
		frame(1, "tial\n"),
		frame(1, ts(4, 0)+" \n"), // an empty line is dropped
		frame(1, ts(5, 0)+" last\n"),
	)
	labels := map[string]string{labelApp: "movies", labelEnv: "prod", labelService: "worker"}
	f := newFakeEngine(t, fakeCont{id: "c1", labels: labels, body: body})
	s := &sinkRec{}
	start(t, f, s, filepath.Join(t.TempDir(), "c.json"), nil)

	got := s.wait(t, 6)
	want := []core.LogLine{
		{T: time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC).UnixMilli(), Stream: "stdout", Text: "hello"},
		{T: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC).UnixMilli(), Stream: "stderr", Text: "oops"},
		{T: time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC).UnixMilli(), Stream: "stderr", Text: "err one"},
		{T: time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC).UnixMilli(), Stream: "stderr", Text: "err two"},
		{T: time.Date(2026, 1, 1, 0, 0, 2, 5000000, time.UTC).UnixMilli(), Stream: "stdout", Text: "partial"},
		{T: time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC).UnixMilli(), Stream: "stdout", Text: "last"},
	}
	byText := map[string]core.LogLine{}
	for _, l := range got {
		byText[l.Text] = l
	}
	for _, w := range want {
		if byText[w.Text] != w {
			t.Errorf("%q: got %+v, want %+v", w.Text, byText[w.Text], w)
		}
	}
	if len(got) != 6 {
		t.Errorf("got %d lines: %+v", len(got), got)
	}
	s.mu.Lock()
	b := s.batches[0]
	s.mu.Unlock()
	if b.App != "movies" || b.Env != "prod" || b.Service != "worker" {
		t.Errorf("batch = %+v", b)
	}
	q := f.queries("c1")[0]
	for k, v := range map[string]string{"follow": "1", "stdout": "1", "stderr": "1", "timestamps": "1"} {
		if q.get(k) != v {
			t.Errorf("logs query %s = %q", k, q.get(k))
		}
	}
}

func (v qvals) get(k string) string {
	if len(v[k]) == 0 {
		return ""
	}
	return v[k][0]
}

func TestFollowerServiceDefaultsToApp(t *testing.T) {
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, body: frame(1, ts(0, 1)+" hi\n")})
	s := &sinkRec{}
	start(t, f, s, filepath.Join(t.TempDir(), "c.json"), nil)
	s.wait(t, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.batches[0].Service != "app" {
		t.Fatalf("service = %q", s.batches[0].Service)
	}
}

func TestFollowerReadsATTYAsRawText(t *testing.T) {
	body := []byte(ts(0, 1) + " first\r\n" + ts(1, 0) + " sec")
	body = append(body, []byte("ond\n")...)
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, tty: true, body: body})
	s := &sinkRec{}
	start(t, f, s, filepath.Join(t.TempDir(), "c.json"), nil)
	got := s.wait(t, 2)
	if got[0].Text != "first" || got[1].Text != "second" || got[0].Stream != "stdout" || got[1].Stream != "stdout" {
		t.Fatalf("%+v", got)
	}
	if got[1].T != time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC).UnixMilli() {
		t.Fatalf("time %d", got[1].T)
	}
}

func TestFollowerNeverTouchesContainersWithoutHomelabLabels(t *testing.T) {
	line := frame(1, ts(0, 1)+" secret stuff\n")
	f := newFakeEngine(t,
		fakeCont{id: "plain", labels: map[string]string{}, body: line},
		fakeCont{id: "nolabels", body: line},
		fakeCont{id: "apponly", labels: map[string]string{labelApp: "movies"}, body: line},
		fakeCont{id: "envonly", labels: map[string]string{labelEnv: "prod"}, body: line},
		fakeCont{id: "ha", labels: map[string]string{"com.docker.compose.project": "home-assistant"}, body: line},
		fakeCont{id: "badapp", labels: map[string]string{labelApp: "../etc", labelEnv: "prod"}, body: line},
		fakeCont{id: "badenv", labels: map[string]string{labelApp: "movies", labelEnv: "PROD"}, body: line},
		fakeCont{id: "badsvc", labels: map[string]string{labelApp: "movies", labelEnv: "prod", labelService: "a b"}, body: line},
		fakeCont{id: "good", labels: appLabels, body: line},
	)
	s := &sinkRec{}
	start(t, f, s, filepath.Join(t.TempDir(), "c.json"), nil)
	s.wait(t, 1)
	time.Sleep(150 * time.Millisecond) // several polls
	for _, id := range []string{"plain", "nolabels", "apponly", "envonly", "ha", "badapp", "badenv", "badsvc"} {
		if f.askedFor("/containers/" + id + "/") {
			t.Errorf("the follower touched container %s", id)
		}
	}
	if !f.askedFor("/containers/good/logs") {
		t.Error("the good container was not followed")
	}
	if n := len(s.lines()); n != 1 {
		t.Errorf("%d lines stored, want 1", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.asked {
		if !strings.HasPrefix(a, "GET ") {
			t.Errorf("not a GET: %s", a)
		}
	}
}

func TestFollowerSkipsLinesAtOrBeforeTheCursorAndKeepsItAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs-cursors.json")
	second := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC).UnixNano()
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"c1":%d,"gone":1}`, second)), 0o600); err != nil {
		t.Fatal(err)
	}
	body := concat(frame(1, ts(1, 0)+" one\n"), frame(1, ts(2, 0)+" two\n"), frame(1, ts(3, 0)+" three\n"))
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, body: body})
	s := &sinkRec{}
	stop := start(t, f, s, path, nil)
	got := s.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	got = s.lines()
	if len(got) != 1 || got[0].Text != "three" {
		t.Fatalf("lines = %+v", got)
	}
	// It asked Docker to start at the cursor.
	wantSince := fmt.Sprintf("%d.%09d", second/1e9, second%1e9)
	if q := f.queries("c1")[0]; q.get("since") != wantSince {
		t.Errorf("since = %q, want %q", q.get("since"), wantSince)
	}
	stop()

	// The cursor is on disk and has moved to the newest line sent.
	var saved map[string]int64
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatalf("cursor file: %s %v", b, err)
	}
	if saved["c1"] != time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC).UnixNano() {
		t.Errorf("saved = %v", saved)
	}

	// A new follower (Homelab restarted) resumes from the file: Docker sends all three
	// lines again and none is stored twice.
	s2 := &sinkRec{}
	start(t, f, s2, path, nil)
	deadline := time.Now().Add(2 * time.Second)
	for len(f.queries("c1")) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(s2.lines()); n != 0 {
		t.Errorf("a restarted follower stored %d lines again", n)
	}
	wantSince = fmt.Sprintf("%d.%09d", saved["c1"]/1e9, saved["c1"]%1e9)
	if qs := f.queries("c1"); len(qs) < 2 || qs[1].get("since") != wantSince {
		t.Errorf("queries = %v, want since %s", qs, wantSince)
	}
}

func TestFollowerStartsWithoutACursorFourteenDaysBack(t *testing.T) {
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels})
	start(t, f, &sinkRec{}, filepath.Join(t.TempDir(), "c.json"), nil)
	deadline := time.Now().Add(2 * time.Second)
	for len(f.queries("c1")) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	qs := f.queries("c1")
	if len(qs) == 0 {
		t.Fatal("not followed")
	}
	var sec int64
	fmt.Sscanf(qs[0].get("since"), "%d", &sec)
	if age := time.Since(time.Unix(sec, 0)); age < 13*24*time.Hour || age > 15*24*time.Hour {
		t.Errorf("since is %v ago", age)
	}
}

func manyLines(n int) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		out = append(out, frame(1, ts(0, i+1)+fmt.Sprintf(" line %d\n", i))...)
	}
	return out
}

func TestFollowerBatchesBy500AndFlushesTheRestOnATimerOrAtStop(t *testing.T) {
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, body: manyLines(1200)})
	s := &sinkRec{}
	// A timer that never fires: only the 500-line rule and the stop flush send.
	stop := start(t, f, s, filepath.Join(t.TempDir(), "c.json"), func(lf *LogFollower) { lf.flush = time.Hour })
	s.wait(t, 1000)
	time.Sleep(100 * time.Millisecond)
	if n := len(s.lines()); n != 1000 {
		t.Fatalf("before the timer: %d lines, want 1000", n)
	}
	stop() // the last try on the way out sends the other 200
	got := s.lines()
	if len(got) != 1200 {
		t.Fatalf("after stop: %d lines", len(got))
	}
	for i, l := range got {
		if l.Text != fmt.Sprintf("line %d", i) {
			t.Fatalf("line %d is %q", i, l.Text)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.batches {
		if len(b.Lines) > 500 {
			t.Errorf("a batch of %d lines", len(b.Lines))
		}
	}
}

func TestFollowerFlushesALittleOnTheTimer(t *testing.T) {
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, body: manyLines(3)})
	s := &sinkRec{}
	start(t, f, s, filepath.Join(t.TempDir(), "c.json"), nil)
	if got := s.wait(t, 3); len(got) != 3 {
		t.Fatalf("%+v", got)
	}
}

func TestFollowerKeepsTheNewestLinesWhenTheSinkFails(t *testing.T) {
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, body: manyLines(300)})
	s := &sinkRec{fail: true}
	path := filepath.Join(t.TempDir(), "c.json")
	start(t, f, s, path, func(lf *LogFollower) { lf.maxBuffer = 100 })
	deadline := time.Now().Add(3 * time.Second)
	for s.tries() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // all 300 lines are read by now
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the cursor moved although nothing was stored")
	}
	s.setFail(false)
	got := s.wait(t, 100)
	time.Sleep(100 * time.Millisecond)
	got = s.lines()
	if len(got) != 100 || got[0].Text != "line 200" || got[99].Text != "line 299" {
		t.Fatalf("got %d lines, first %q last %q", len(got), got[0].Text, got[len(got)-1].Text)
	}
}

func TestFollowerWarnsOncePerMinuteAboutAFailingSink(t *testing.T) {
	f := newFakeEngine(t, fakeCont{id: "c1", labels: appLabels, body: manyLines(5)})
	s := &sinkRec{fail: true}
	var mu sync.Mutex
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var lf *LogFollower
	start(t, f, s, filepath.Join(t.TempDir(), "c.json"), func(l *LogFollower) {
		lf = l
		l.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	})
	for s.tries() < 10 {
		time.Sleep(5 * time.Millisecond)
	}
	// warn is rate limited: lastWarned changes only when a minute has passed.
	lf.mu.Lock()
	first := lf.lastWarned
	lf.mu.Unlock()
	if first.IsZero() {
		t.Fatal("no warning at all")
	}
	if !first.Equal(clock) {
		t.Fatalf("first = %v", first)
	}
	mu.Lock()
	clock = clock.Add(30 * time.Second)
	mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	lf.mu.Lock()
	again := lf.lastWarned
	lf.mu.Unlock()
	if !again.Equal(first) {
		t.Fatalf("warned again after 30s: %v", again)
	}
}
