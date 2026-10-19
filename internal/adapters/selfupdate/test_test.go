package selfupdate

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTestGitHub serves a normal release, a draft and one test version.
func fakeTestGitHub(t *testing.T, tag string, prog []byte) *httptest.Server {
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assets := fmt.Sprintf(`[{"name":%q,"url":"%s/asset/prog"},{"name":"checksums.txt","url":"%s/asset/sums"}]`, name, srv.URL, srv.URL)
		test := fmt.Sprintf(`{"tag_name":%q,"name":"Better sign-in","prerelease":true,"assets":%s}`, tag, assets)
		switch r.URL.Path {
		case "/repos/rupaai/homelab/releases":
			fmt.Fprintf(w, `[%s,{"tag_name":"build-3","assets":%s},{"tag_name":"pr-9-ffff","prerelease":true,"draft":true,"assets":%s}]`, test, assets, assets)
		case "/repos/rupaai/homelab/releases/tags/" + tag:
			fmt.Fprint(w, test)
		case "/repos/rupaai/homelab/releases/tags/build-3":
			fmt.Fprintf(w, `{"tag_name":"build-3","assets":%s}`, assets)
		case "/asset/prog":
			w.Write(prog)
		case "/asset/sums":
			fmt.Fprintf(w, "%s  %s\n", sha(prog), name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func run(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func rows(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rs, err := db.Query("SELECT v FROM t ORDER BY v")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var v string
		rs.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestTestPR(t *testing.T) {
	for v, want := range map[string]int{"pr-12-1a2b3c4": 12, "pr-12": 0, "pr-x-1": 0, "build-3": 0, "pr-0-a": 0} {
		if n, _ := TestPR(v); n != want {
			t.Errorf("TestPR(%q) = %d, want %d", v, n, want)
		}
	}
}

func TestTryAndGoBackPutsDataBack(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-2")
	bin := BinDir(home)
	state := filepath.Join(home, "homelab.db")
	db := openDB(t, state)
	run(t, db, "CREATE TABLE t (v TEXT)")
	run(t, db, "INSERT INTO t VALUES ('before')")
	secrets := openDB(t, filepath.Join(home, "secrets", "secrets.db"))
	run(t, secrets, "CREATE TABLE t (v TEXT)")

	const tag = "pr-7-abc1234"
	srv := fakeTestGitHub(t, tag, script(tag))
	restarts := make(chan struct{}, 2)
	u := New(Options{Home: home, Version: "build-2", API: srv.URL, Restart: func() { restarts <- struct{}{} }, Now: func() time.Time { return t0 }})

	tests, err := u.Tests(context.Background())
	if err != nil || len(tests) != 1 || tests[0].Version != tag || tests[0].PR != 7 || tests[0].Title != "Better sign-in" {
		t.Fatalf("tests = %+v, %v", tests, err)
	}
	if _, err := u.Try(context.Background(), "build-3"); err == nil {
		t.Fatal("a normal release was tried as a test version")
	}
	if _, err := u.Try(context.Background(), tag); err != nil {
		t.Fatal(err)
	}
	<-restarts
	if r, _ := Running(bin); r != tag {
		t.Fatalf("running %q", r)
	}
	s, _ := u.Status(context.Background())
	if s.Testing == nil || s.Testing.From != "build-2" || s.Testing.PR != 7 {
		t.Fatalf("status testing = %+v", s.Testing)
	}
	if err := u.Keep(context.Background()); err == nil {
		t.Fatal("kept a test version that hasn't proven itself")
	}

	// The test version changes the data; going back must undo that.
	run(t, db, "INSERT INTO t VALUES ('during')")
	if from, err := u.GoBack(context.Background()); err != nil || from != "build-2" {
		t.Fatalf("go back = %q, %v", from, err)
	}
	<-restarts
	db.Close()
	secrets.Close()
	if r, _ := Running(bin); r != "build-2" || Pending(bin) {
		t.Fatalf("running %q pending %v", r, Pending(bin))
	}
	msg, err := FinishTest(home, t0)
	if err != nil || !strings.Contains(msg, "data from before") {
		t.Fatalf("finish = %q, %v", msg, err)
	}
	if got := rows(t, state); len(got) != 1 || got[0] != "before" {
		t.Fatalf("data after going back = %v", got)
	}
	if _, ok := ReadTest(bin); ok {
		t.Fatal("test still recorded")
	}
	if _, err := os.Stat(filepath.Join(bin, testSaved)); !os.IsNotExist(err) {
		t.Fatalf("saved copy left behind: %v", err)
	}
	if r, ok := LastResult(bin); !ok || !strings.Contains(r.Message(), "Went back") {
		t.Fatalf("result = %+v", r)
	}
}

func TestGuardGoingBackAlsoPutsDataBack(t *testing.T) {
	home := installed(t, "build-2")
	bin := BinDir(home)
	state := filepath.Join(home, "homelab.db")
	db := openDB(t, state)
	run(t, db, "CREATE TABLE t (v TEXT)")
	run(t, db, "INSERT INTO t VALUES ('before')")
	if err := SaveData(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(bin, "pr-7-abc", script("pr-7-abc")); err != nil {
		t.Fatal(err)
	}
	if err := StartTest(bin, Test{Version: "pr-7-abc", PR: 7}, t0); err != nil {
		t.Fatal(err)
	}
	run(t, db, "INSERT INTO t VALUES ('during')")
	db.Close()
	for i := 0; i <= maxTries; i++ {
		if _, err := Guard(bin, t0); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ := Running(bin); r != "build-2" {
		t.Fatalf("running %q", r)
	}
	if _, err := FinishTest(home, t0); err != nil {
		t.Fatal(err)
	}
	if got := rows(t, state); len(got) != 1 || got[0] != "before" {
		t.Fatalf("data = %v", got)
	}
}

func TestUpdatingDuringATestEndsIt(t *testing.T) {
	home := installed(t, "build-2")
	bin := BinDir(home)
	state := filepath.Join(home, "homelab.db")
	db := openDB(t, state)
	run(t, db, "CREATE TABLE t (v TEXT)")
	if err := SaveData(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"pr-7-abc", "build-3"} {
		if _, err := Install(bin, v, script(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := StartTest(bin, Test{Version: "pr-7-abc", PR: 7}, t0); err != nil {
		t.Fatal(err)
	}
	if msg, err := FinishTest(home, t0); msg != "" || err != nil {
		t.Fatalf("finish while testing = %q, %v", msg, err)
	}
	run(t, db, "INSERT INTO t VALUES ('kept')")
	db.Close()
	if err := Activate(bin, "build-3"); err != nil {
		t.Fatal(err)
	}
	if msg, err := FinishTest(home, t0); msg != "" || err != nil {
		t.Fatalf("finish = %q, %v", msg, err)
	}
	if got := rows(t, state); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("data = %v", got)
	}
	if _, ok := ReadTest(bin); ok {
		t.Fatal("test still recorded")
	}
}
