package docker

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestSmokeArgs(t *testing.T) {
	st := core.SmokeTest{
		App: "movies", Env: "dev", Port: 8080,
		Release: core.Release{Image: "ghcr.io/o/movies", Digest: "sha256:abc"},
		Command: []string{"python", "-m", "app.smoke"},
	}
	got := smokeArgs("homelab-smoke-movies-dev-1234", st)
	want := []string{
		"run", "--rm", "--name", "homelab-smoke-movies-dev-1234",
		"--network", "homelab-movies-dev_default",
		"--memory", "512m", "--pids-limit", "256", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--read-only", "--tmpfs", "/tmp",
		"-e", "HOMELAB_TARGET_URL=http://app:8080",
		"-e", "HOMELAB_APP=movies", "-e", "HOMELAB_ENV=dev",
		"--entrypoint", "python", "ghcr.io/o/movies@sha256:abc", "-m", "app.smoke",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args:\n got %q\nwant %q", got, want)
	}
}

func TestSmokeArgsSingleWordCommand(t *testing.T) {
	st := core.SmokeTest{App: "a", Env: "dev", Port: 80, Release: core.Release{Image: "img", Tag: "t"}, Command: []string{"/smoke"}}
	got := smokeArgs("n", st)
	if got[len(got)-1] != "img:t" || got[len(got)-2] != "/smoke" {
		t.Fatalf("unexpected tail: %q", got)
	}
}

func TestSmokeDefaultTimeout(t *testing.T) {
	if defaultSmokeTimeout.Minutes() != 5 {
		t.Fatalf("default timeout is %s, want 5m", defaultSmokeTimeout)
	}
}

func TestSmokeRefusesBadInput(t *testing.T) {
	d := &Deployer{bin: "docker-does-not-exist"}
	rel := core.Release{Image: "img", Tag: "t"}
	cases := map[string]core.SmokeTest{
		"bad name":   {App: "Bad App", Env: "dev", Port: 80, Release: rel, Command: []string{"x"}},
		"no command": {App: "a", Env: "dev", Port: 80, Release: rel},
		"no port":    {App: "a", Env: "dev", Release: rel, Command: []string{"x"}},
		"no image":   {App: "a", Env: "dev", Port: 80, Command: []string{"x"}},
	}
	for name, st := range cases {
		if _, _, err := d.Smoke(context.Background(), st); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSmokeName(t *testing.T) {
	a, _ := smokeName("movies", "dev")
	b, _ := smokeName("movies", "dev")
	if !strings.HasPrefix(a, "homelab-smoke-movies-dev-") || a == b {
		t.Fatalf("names %q %q", a, b)
	}
}
