package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

var _ core.SmokeTester = (*Deployer)(nil)

const (
	defaultSmokeTimeout = 5 * time.Minute
	smokeOutputBytes    = 4 << 10
)

// Smoke runs an app's smoke test inside the release's own image, on the environment's
// network, and reports whether it passed. A test that runs and fails is passed=false with
// no error; err is only for when the test couldn't be run at all.
func (d *Deployer) Smoke(ctx context.Context, t core.SmokeTest) (bool, string, error) {
	if err := checkNames(t.App, t.Env); err != nil {
		return false, "", err
	}
	if len(t.Command) == 0 || t.Command[0] == "" {
		return false, "", fmt.Errorf("%s has no smoke test command to run", t.App)
	}
	if t.Port <= 0 || t.Port > 65535 {
		return false, "", fmt.Errorf("%s needs a port between 1 and 65535 for its smoke test", t.App)
	}
	if t.Release.Image == "" {
		return false, "", fmt.Errorf("%s/%s has no image to run the smoke test in", t.App, t.Env)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultSmokeTimeout
	}

	if err := d.Prepare(ctx, core.EnvSpec{App: t.App, Env: t.Env, Release: t.Release, Auth: t.Auth}); err != nil {
		return false, "", fmt.Errorf("couldn't get the image for the smoke test: %w", err)
	}

	name, err := smokeName(t.App, t.Env)
	if err != nil {
		return false, "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, d.bin, smokeArgs(name, t)...)
	cmd.Env = d.env()
	out := &tailBuffer{max: smokeOutputBytes}
	cmd.Stdout, cmd.Stderr = out, out
	runErr := cmd.Run()
	output := strings.TrimSpace(string(out.Bytes()))

	if runErr == nil {
		return true, output, nil
	}
	if runCtx.Err() != nil {
		d.removeSmoke(name)
		if ctx.Err() != nil {
			return false, output, ctx.Err()
		}
		line := fmt.Sprintf("The smoke test took longer than the limit of %s and was stopped.", timeout)
		if output != "" {
			line = output + "\n" + line
		}
		return false, line, nil
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		if code := exit.ExitCode(); code != 125 && code != 126 && code != 127 || !dockerFailed(output) {
			return false, output, nil // the test ran and failed
		}
		return false, output, fmt.Errorf("docker couldn't run the smoke test for %s/%s (is %s running?): %s",
			t.App, t.Env, project(t.App, t.Env), lastLines([]byte(output), 3))
	}
	return false, output, fmt.Errorf("couldn't start docker to run the smoke test: %w", runErr)
}

// dockerFailed tells docker's own failure (exit 125 and friends) from a test that
// happened to exit with the same code. Docker reports its failures with this prefix.
func dockerFailed(output string) bool {
	return strings.Contains(output, "docker:") || strings.Contains(output, "Error response from daemon")
}

// removeSmoke force-removes a smoke container, ignoring errors (it may be gone already).
func (d *Deployer) removeSmoke(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.bin, "rm", "-f", name)
	cmd.Env = d.env()
	_ = cmd.Run()
}

func smokeName(app, env string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "homelab-smoke-" + app + "-" + env + "-" + hex.EncodeToString(b), nil
}

// smokeArgs builds the `docker run` arguments. The container is locked down: no mounts, no
// published ports, no secrets, a read-only filesystem and no extra privileges. --entrypoint
// makes the command run as given, whatever the image's ENTRYPOINT is.
func smokeArgs(name string, t core.SmokeTest) []string {
	args := []string{
		"run", "--rm",
		"--name", name,
		"--network", project(t.App, t.Env) + "_default",
		"--memory", "512m",
		"--pids-limit", "256",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp",
		"-e", fmt.Sprintf("HOMELAB_TARGET_URL=http://%s:%d", serviceName, t.Port),
		"-e", "HOMELAB_APP=" + t.App,
		"-e", "HOMELAB_ENV=" + t.Env,
		"--entrypoint", t.Command[0],
		t.Release.ImageRef(),
	}
	return append(args, t.Command[1:]...)
}
