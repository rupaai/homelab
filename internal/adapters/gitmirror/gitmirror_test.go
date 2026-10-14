package gitmirror

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// fixture makes <remotes>/owner/name.git with one commit on main, and a work tree to push more from.
func fixture(t *testing.T) (remotes, work string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	remotes = t.TempDir()
	bare := filepath.Join(remotes, "owner", "name.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "--bare", "-b", "main")
	work = t.TempDir()
	git(t, work, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "first")
	git(t, work, "remote", "add", "origin", bare)
	git(t, work, "push", "origin", "main")
	return remotes, work
}

func newMirror(t *testing.T, remotes string) *Mirror {
	t.Helper()
	m, err := New(Options{Dir: t.TempDir(), BaseURL: "file://" + remotes, Token: func() string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func branches(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", path, "branch", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

func TestSyncCloneThenUpdate(t *testing.T) {
	remotes, work := fixture(t)
	m := newMirror(t, remotes)
	ctx := context.Background()

	if s := m.Status("owner/name"); s.LastSync != nil || s.Error != "" || !strings.HasSuffix(s.Path, filepath.Join("owner", "name.git")) {
		t.Fatalf("status before: %+v", s)
	}
	if err := m.Sync(ctx, "owner/name"); err != nil {
		t.Fatal(err)
	}
	s := m.Status("owner/name")
	if s.LastSync == nil || s.Error != "" {
		t.Fatalf("status after: %+v", s)
	}
	if !strings.Contains(branches(t, s.Path), "main") {
		t.Fatal("main missing from the mirror")
	}

	git(t, work, "checkout", "-b", "feature")
	git(t, work, "commit", "--allow-empty", "-m", "second")
	git(t, work, "push", "origin", "feature")
	if err := m.Sync(ctx, "owner/name"); err != nil {
		t.Fatal(err)
	}
	if b := branches(t, s.Path); !strings.Contains(b, "feature") {
		t.Fatalf("feature missing after second sync:\n%s", b)
	}
}

func TestLastSyncSurvivesRestart(t *testing.T) {
	remotes, _ := fixture(t)
	m := newMirror(t, remotes)
	if err := m.Sync(context.Background(), "owner/name"); err != nil {
		t.Fatal(err)
	}
	m2, _ := New(Options{Dir: m.dir, BaseURL: m.baseURL})
	if s := m2.Status("owner/name"); s.LastSync == nil || time.Since(*s.LastSync) > time.Minute {
		t.Fatalf("status after restart: %+v", s)
	}
}

func TestBadRepoNames(t *testing.T) {
	remotes, _ := fixture(t)
	m := newMirror(t, remotes)
	for _, repo := range []string{"", "owner", "a/b/c", "../x", "owner/..", "./name", "own er/name", "owner/na;me", "/name"} {
		if err := m.Sync(context.Background(), repo); err == nil {
			t.Errorf("Sync(%q) should be refused", repo)
		}
	}
}

func TestFailedSyncKeepsMirror(t *testing.T) {
	remotes, _ := fixture(t)
	m := newMirror(t, remotes)
	ctx := context.Background()
	if err := m.Sync(ctx, "owner/name"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(remotes, "owner", "name.git")); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(ctx, "owner/name"); err == nil {
		t.Fatal("expected the sync to fail")
	}
	s := m.Status("owner/name")
	if s.Error == "" || s.LastSync == nil {
		t.Fatalf("status: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "HEAD")); err != nil {
		t.Fatalf("mirror was removed: %v", err)
	}
}

func TestFirstCloneFailureLeavesNothing(t *testing.T) {
	remotes, _ := fixture(t)
	m := newMirror(t, remotes)
	if err := m.Sync(context.Background(), "owner/missing"); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(m.Status("owner/missing").Path); err == nil {
		t.Fatal("a half mirror was left behind")
	}
	if m.Status("owner/missing").Error == "" {
		t.Fatal("error not recorded")
	}
}

func TestEnvHidesTokenAndInheritedGit(t *testing.T) {
	t.Setenv("GIT_DIR", "/nowhere")
	m, _ := New(Options{Dir: t.TempDir()})
	env := strings.Join(m.env("s3cret"), "\n")
	if strings.Contains(env, "GIT_DIR") || strings.Contains(env, "s3cret") {
		t.Fatal("env leaked inherited GIT_* or raw token")
	}
	if !strings.Contains(env, "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader") || !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("env missing auth config:\n%s", env)
	}
	if got := scrub("oops s3cret here", "s3cret"); strings.Contains(got, "s3cret") {
		t.Fatal("scrub failed")
	}
}

func TestExplainOrder(t *testing.T) {
	ctx := context.Background()
	out := "fatal: unable to access 'https://github.com/a/b.git/': The requested URL returned error: 403"
	if err := explain(ctx, out, errors.New("exit 128"), "clone"); !strings.Contains(err.Error(), "refused the token") {
		t.Fatalf("403: %v", err)
	}
	out = "fatal: unable to access 'https://github.com/a/b.git/': Could not resolve host: github.com"
	if err := explain(ctx, out, errors.New("exit 128"), "clone"); err.Error() != "Couldn't reach GitHub" {
		t.Fatalf("dns: %v", err)
	}
}
