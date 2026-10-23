package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// installed makes home look like `homelabd setup` installed version.
func installed(t *testing.T, version string) string {
	t.Helper()
	home := t.TempDir()
	bin := BinDir(home)
	if _, err := Install(bin, version, script(version)); err != nil {
		t.Fatal(err)
	}
	if err := relink(bin, current, VersionFile(version)); err != nil {
		t.Fatal(err)
	}
	return home
}

func script(version string) []byte { return []byte("#!/bin/sh\necho homelabd " + version + "\n") }

func TestSwitchGuardConfirm(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	if _, err := Install(bin, "build-2", script("build-2")); err != nil {
		t.Fatal(err)
	}
	if err := Switch(bin, "build-2", t0); err != nil {
		t.Fatal(err)
	}
	if v, _ := Running(bin); v != "build-2" || !Pending(bin) {
		t.Fatalf("running %q pending %v", v, Pending(bin))
	}
	if msg, err := Guard(bin, t0); err != nil || !strings.Contains(msg, "try 1") {
		t.Fatalf("guard = %q, %v", msg, err)
	}
	if ok, err := Confirm(bin, "build-1", t0); ok || err != nil {
		t.Fatalf("confirm for the wrong version = %v, %v", ok, err)
	}
	if ok, err := Confirm(bin, "build-2", t0); !ok || err != nil {
		t.Fatalf("confirm = %v, %v", ok, err)
	}
	if Pending(bin) {
		t.Fatal("still pending after confirm")
	}
	if r, ok := LastResult(bin); !ok || !r.OK || r.Message() != "Updated to build-2." {
		t.Fatalf("result = %+v", r)
	}
	if msg, err := Guard(bin, t0); msg != "" || err != nil {
		t.Fatalf("guard with nothing pending = %q, %v", msg, err)
	}
}

func TestGuardGoesBack(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	Install(bin, "build-2", script("build-2"))
	if err := Switch(bin, "build-2", t0); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxTries; i++ {
		if _, err := Guard(bin, t0); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := Guard(bin, t0)
	if err != nil || !strings.Contains(msg, "went back to build-1") {
		t.Fatalf("guard = %q, %v", msg, err)
	}
	if v, _ := Running(bin); v != "build-1" || Pending(bin) {
		t.Fatalf("running %q pending %v", v, Pending(bin))
	}
	if r, _ := LastResult(bin); r.OK || !strings.Contains(r.Message(), "went back to build-1") {
		t.Fatalf("result = %+v", r)
	}
}

func TestGuardIgnoresHandSwitch(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	Install(bin, "build-2", script("build-2"))
	Switch(bin, "build-2", t0)
	relink(bin, current, VersionFile("build-1")) // setup ran again with build-1
	if msg, err := Guard(bin, t0); msg != "" || err != nil || Pending(bin) {
		t.Fatalf("guard = %q, %v, pending %v", msg, err, Pending(bin))
	}
}

func TestPruneKeepsRecentAndLinked(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	for i := 2; i <= 6; i++ {
		v := fmt.Sprintf("build-%d", i)
		Install(bin, v, script(v))
		Switch(bin, v, t0)
		Confirm(bin, v, t0)
	}
	for _, v := range []string{"build-6", "build-5", "build-4"} {
		if _, err := os.Stat(filepath.Join(bin, VersionFile(v))); err != nil {
			t.Errorf("%s was deleted", v)
		}
	}
	for _, v := range []string{"build-1", "build-2"} {
		if _, err := os.Stat(filepath.Join(bin, VersionFile(v))); err == nil {
			t.Errorf("%s was kept", v)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"build-3", "build-2", true},
		{"build-2", "build-2", false},
		{"build-1", "build-2", false},
		{"build-3", "70200a1-dirty", true},
		{"v1", "build-2", false},
		{"", "dev", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.latest, c.current, got)
		}
	}
}

func TestInstallRefusesOddVersions(t *testing.T) {
	for _, v := range []string{"", "../x", "a/b", ".hidden"} {
		if _, err := Install(t.TempDir(), v, nil); err == nil {
			t.Errorf("Install(%q) worked", v)
		}
	}
}

// fakeGitHub serves one release with the program for this computer.
func fakeGitHub(t *testing.T, tag string, prog []byte, sum string) (*httptest.Server, *atomic.Int32) {
	return fakeGitHubWithServices(t, tag, prog, sum, nil, "")
}

// fakeGitHubWithServices also serves homelab-services, when svc isn't nil.
func fakeGitHubWithServices(t *testing.T, tag string, prog []byte, sum string, svc []byte, svcSum string) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	svcName := ServicesAssetName(runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/rupaai/homelab/releases/latest", "/repos/rupaai/homelab/releases/tags/" + tag:
			extra := ""
			if svc != nil {
				extra = fmt.Sprintf(`,{"name":%q,"url":"%s/asset/svc"}`, svcName, srv.URL)
			}
			fmt.Fprintf(w, `{"tag_name":%q,"body":"## New\n\n- add y (#2)\n- add w\n\n## Fixes\n\n- mend x (#3)\n","assets":[{"name":%q,"url":"%s/asset/prog"},{"name":"checksums.txt","url":"%s/asset/sums"}%s]}`, tag, name, srv.URL, srv.URL, extra)
		case "/asset/prog":
			w.Write(prog)
		case "/asset/svc":
			w.Write(svc)
		case "/asset/sums":
			fmt.Fprintf(w, "%s  %s\n%s  other\n", sum, name, strings.Repeat("0", 64))
			if svc != nil {
				fmt.Fprintf(w, "%s  %s\n", svcSum, svcName)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestApply(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-1")
	prog := script("build-2")
	srv, _ := fakeGitHub(t, "build-2", prog, sha(prog))
	restarted := make(chan struct{})
	u := New(Options{Home: home, Version: "build-1", API: srv.URL, Token: func() string { return "tok" },
		Restart: func() { close(restarted) }, Now: func() time.Time { return t0 }})
	s, err := u.Status(context.Background())
	if err != nil || !s.Managed || !s.Available || s.Latest != "build-2" {
		t.Fatalf("status = %+v, %v", s, err)
	}
	if c := s.Changes; c == nil || c.New != 2 || c.Fixes != 1 || c.Breaking != 0 || !strings.Contains(c.Notes, "- add w") {
		t.Fatalf("changes = %+v", c)
	}
	v, err := u.Apply(context.Background())
	if err != nil || v != "build-2" {
		t.Fatalf("apply = %q, %v", v, err)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("no restart")
	}
	if r, _ := Running(BinDir(home)); r != "build-2" {
		t.Fatalf("running %q", r)
	}
	if _, err := u.Apply(context.Background()); err == nil {
		t.Fatal("a second apply while the first is pending worked")
	}
}

func TestApplyRefusesBadChecksum(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-1")
	srv, _ := fakeGitHub(t, "build-2", script("build-2"), strings.Repeat("a", 64))
	u := New(Options{Home: home, Version: "build-1", API: srv.URL, Token: func() string { return "tok" }})
	if _, err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("apply = %v", err)
	}
	if r, _ := Running(BinDir(home)); r != "build-1" || Pending(BinDir(home)) {
		t.Fatalf("running %q", r)
	}
}

func TestStatusUnmanaged(t *testing.T) {
	u := New(Options{Home: t.TempDir(), Version: "dev"})
	s, err := u.Status(context.Background())
	if err != nil || s.Managed || s.Note == "" {
		t.Fatalf("status = %+v, %v", s, err)
	}
	if _, err := u.Apply(context.Background()); err == nil {
		t.Fatal("apply worked on an unmanaged Homelab")
	}
}

func TestActivate(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	Install(bin, "build-2", script("build-2"))
	Switch(bin, "build-2", t0)
	Install(bin, "build-3", script("build-3"))
	if err := Activate(bin, "build-3"); err != nil {
		t.Fatal(err)
	}
	if v, _ := Running(bin); v != "build-3" || Pending(bin) {
		t.Fatalf("running %q pending %v", v, Pending(bin))
	}
	if p, _ := os.Readlink(filepath.Join(bin, previous)); p != VersionFile("build-2") {
		t.Fatalf("previous = %q", p)
	}
	if !Managed(home) && runtime.GOOS == "linux" {
		t.Fatal("not managed")
	}
}

func svcScript(version string) []byte {
	return []byte("#!/bin/sh\necho homelab-services " + version + "\n")
}

func TestApplyInstallsServicesNextToHomelabd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-1")
	prog, svc := script("build-2"), svcScript("build-2")
	srv, _ := fakeGitHubWithServices(t, "build-2", prog, sha(prog), svc, sha(svc))
	u := New(Options{Home: home, Version: "build-1", API: srv.URL, Token: func() string { return "tok" },
		Restart: func() {}, Now: func() time.Time { return t0 }})
	if _, err := u.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(BinDir(home), "homelab-services-build-2"))
	if err != nil || string(got) != string(svc) {
		t.Fatalf("services program: %q %v", got, err)
	}
}

func TestApplyRefusesBadServicesChecksum(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-1")
	prog, svc := script("build-2"), svcScript("build-2")
	srv, _ := fakeGitHubWithServices(t, "build-2", prog, sha(prog), svc, strings.Repeat("b", 64))
	u := New(Options{Home: home, Version: "build-1", API: srv.URL, Token: func() string { return "tok" }})
	if _, err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("apply = %v", err)
	}
	if r, _ := Running(BinDir(home)); r != "build-1" || Pending(BinDir(home)) {
		t.Fatalf("switched to %q with a bad services program", r)
	}
}

func TestPruneTakesServicesAlong(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	InstallServices(bin, "build-1", svcScript("build-1"))
	for i := 2; i <= 5; i++ {
		v := fmt.Sprintf("build-%d", i)
		Install(bin, v, script(v))
		InstallServices(bin, v, svcScript(v))
		Switch(bin, v, t0)
		Confirm(bin, v, t0)
	}
	if _, err := os.Stat(filepath.Join(bin, "homelab-services-build-1")); err == nil {
		t.Error("services of a pruned version were kept")
	}
	if _, err := os.Stat(filepath.Join(bin, "homelab-services-build-5")); err != nil {
		t.Error("services of the running version were deleted")
	}
}

func TestFetchServicesForTheRunningVersion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-2")
	prog, svc := script("build-2"), svcScript("build-2")
	srv, _ := fakeGitHubWithServices(t, "build-2", prog, sha(prog), svc, sha(svc))
	u := New(Options{Home: home, Version: "build-2", API: srv.URL, Token: func() string { return "tok" }})
	if err := u.FetchServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(BinDir(home), "homelab-services-build-2")); err != nil || string(got) != string(svc) {
		t.Fatalf("%q %v", got, err)
	}
	other := New(Options{Home: home, Version: "build-3", API: srv.URL, Token: func() string { return "tok" }})
	if err := other.FetchServices(context.Background()); err == nil {
		t.Fatal("fetched for a version GitHub doesn't have")
	}
}

func TestApplyRefusedDuringATest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates are for Linux")
	}
	home := installed(t, "build-1")
	bin := BinDir(home)
	if _, err := Install(bin, "pr-7-abc", script("pr-7-abc")); err != nil {
		t.Fatal(err)
	}
	if err := StartTest(bin, Test{Version: "pr-7-abc", PR: 7}, t0); err != nil {
		t.Fatal(err)
	}
	if ok, err := Confirm(bin, "pr-7-abc", t0.Add(time.Hour)); !ok || err != nil {
		t.Fatalf("confirm = %v, %v", ok, err)
	}
	prog := script("build-2")
	srv, _ := fakeGitHub(t, "build-2", prog, sha(prog))
	u := New(Options{Home: home, Version: "pr-7-abc", API: srv.URL, Now: func() time.Time { return t0 }})
	if _, err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "test version") {
		t.Fatalf("apply = %v", err)
	}
	if r, _ := Running(bin); r != "pr-7-abc" {
		t.Fatalf("running %q", r)
	}
}
