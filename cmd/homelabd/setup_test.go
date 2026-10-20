package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/selfupdate"
)

// install puts a fake program for version in bin and makes it the running one.
func install(t *testing.T, bin, version string) {
	t.Helper()
	if _, err := selfupdate.Install(bin, version, []byte("program "+version)); err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.Activate(bin, version); err != nil {
		t.Fatal(err)
	}
}

func TestSetupRefusesAnOlderProgram(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	install(t, bin, "build-12")
	err := checkNotOlder(bin, "build-9")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("older program accepted: %v", err)
	}
	for _, v := range []string{"build-12", "build-13", "dev"} {
		if err := checkNotOlder(bin, v); err != nil {
			t.Errorf("%s refused: %v", v, err)
		}
	}
}

func TestSetupEndsARunningTestVersion(t *testing.T) {
	home := t.TempDir()
	bin := selfupdate.BinDir(home)
	install(t, bin, "build-12")
	if _, err := selfupdate.Install(bin, "pr-5-abc1234", []byte("test program")); err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.StartTest(bin, selfupdate.Test{Version: "pr-5-abc1234", PR: 5}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bin, "test-saved"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := activate(bin, "build-12", []byte("program build-12"), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := selfupdate.ReadTest(bin); ok {
		t.Fatal("the test version is still recorded after setup")
	}
	// The next start must not put back data from before the test.
	if msg, err := selfupdate.FinishTest(home, testNow); err != nil || msg != "" {
		t.Fatalf("FinishTest = %q, %v", msg, err)
	}
	if err := activate(bin, "build-3", []byte("old"), false); err == nil {
		t.Fatal("setup went back to an older version without --force")
	}
	if err := activate(bin, "build-3", []byte("old"), true); err != nil {
		t.Fatalf("forced: %v", err)
	}
}

var testNow = time.Unix(1700000000, 0)
