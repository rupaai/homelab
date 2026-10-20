// Package docker runs app environments with Docker Compose. It implements core.Deployer.
//
// Each environment is its own Compose project (homelab-<app>-<env>) with one service, "app",
// so it gets its own network and its containers are found again by their homelab.* labels.
// Images are pulled through the Docker Engine API so Homelab never touches the user's
// ~/.docker/config.json.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Options configures a Deployer.
type Options struct {
	Root      string       // where compose files live, e.g. ~/.homelab/apps
	Logger    *slog.Logger // default: discard
	DockerBin string       // default "docker"
	Host      string       // Docker host to use; empty means DOCKER_HOST, then the current Docker context
}

// Deployer is the Docker Compose implementation of core.Deployer.
type Deployer struct {
	root string
	bin  string
	host string
	log  *slog.Logger
	api  *engine
}

var _ core.Deployer = (*Deployer)(nil)

const (
	maxLogBytes  = 1 << 20
	errTailLines = 20
)

// New resolves which Docker to talk to (once) and returns a Deployer. It doesn't need
// Docker to be running yet.
func New(opts Options) (*Deployer, error) {
	if opts.Root == "" {
		return nil, errors.New("docker: Root folder is required")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	d := &Deployer{root: root, bin: opts.DockerBin, log: opts.Logger}
	if d.bin == "" {
		d.bin = "docker"
	}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}
	d.host = opts.Host
	if d.host == "" {
		d.host = os.Getenv("DOCKER_HOST")
	}
	if d.host == "" {
		d.host = d.contextHost()
	}
	if d.api, err = newEngine(d.host); err != nil {
		return nil, err
	}
	d.log.Info("using docker", "host", d.host)
	return d, nil
}

// contextHost asks the docker CLI for the current context's endpoint (this is how OrbStack
// is found) and falls back to the standard socket.
func (d *Deployer) contextHost() string {
	const fallback = "unix:///var/run/docker.sock"
	cmd := exec.Command(d.bin, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	cmd.Env = d.env()
	out, err := cmd.Output()
	if h := strings.TrimSpace(string(out)); err == nil && (strings.HasPrefix(h, "unix://") || strings.HasPrefix(h, "tcp://")) {
		return h
	}
	return fallback
}

// env is the environment for every docker command: the resolved host, no context override.
func (d *Deployer) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DOCKER_HOST=") || strings.HasPrefix(kv, "DOCKER_CONTEXT=") {
			continue
		}
		env = append(env, kv)
	}
	if d.host != "" {
		env = append(env, "DOCKER_HOST="+d.host)
	}
	return env
}

func (d *Deployer) envDir(app, env string) string { return filepath.Join(d.root, app, env) }

// Prepare downloads the release's image unless it is already here.
func (d *Deployer) Prepare(ctx context.Context, spec core.EnvSpec) error {
	if err := checkNames(spec.App, spec.Env); err != nil {
		return err
	}
	ref := spec.Release.ImageRef()
	if spec.Release.Image == "" {
		return fmt.Errorf("%s/%s has no image to download", spec.App, spec.Env)
	}
	// A tag can move, so only an exact digest lets us trust what is already here.
	if spec.Release.Digest != "" {
		have, err := d.api.hasImage(ctx, ref)
		if err != nil {
			return err
		}
		if have {
			return d.prepareServices(ctx, spec)
		}
	}
	d.log.Info("downloading image", "app", spec.App, "env", spec.Env, "image", ref)
	if err := d.api.pull(ctx, ref, spec.Auth); err != nil {
		return err
	}
	return d.prepareServices(ctx, spec)
}

// prepareServices downloads the services' images that aren't here yet. Their tags are
// fixed, so an image that is here is the right one. They come from public registries.
func (d *Deployer) prepareServices(ctx context.Context, spec core.EnvSpec) error {
	for _, s := range spec.Services {
		have, err := d.api.hasImage(ctx, s.Image)
		if err != nil {
			return err
		}
		if have {
			continue
		}
		d.log.Info("downloading image", "app", spec.App, "env", spec.Env, "service", s.Name, "image", s.Image)
		if err := d.api.pull(ctx, s.Image, core.RegistryAuth{}); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return nil
}

// Deploy writes the compose project and settings, makes sure the image is here, and starts
// the containers, replacing any older ones.
func (d *Deployer) Deploy(ctx context.Context, spec core.EnvSpec) error {
	compose, err := composeYAML(spec)
	if err != nil {
		return err
	}
	env, err := envFile(spec.Vars)
	if err != nil {
		return err
	}
	if err := d.Prepare(ctx, spec); err != nil {
		return err
	}
	dir := d.envDir(spec.App, spec.Env)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("can't create %s: %w", dir, err)
	}
	if err := writeFile(filepath.Join(dir, ".env"), env); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "compose.yaml"), compose); err != nil {
		return err
	}
	d.log.Info("starting", "app", spec.App, "env", spec.Env, "commit", spec.Release.Short())
	_, err = d.compose(ctx, dir, spec.App, spec.Env, "up", "-d", "--remove-orphans")
	return err
}

// writeFile replaces path with data (mode 0600) in one step, so a crash never leaves half a file.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("can't write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("can't write %s: %w", path, err)
	}
	return nil
}

// Stop stops the environment's containers and keeps them and their data. It does nothing
// if the environment doesn't exist.
func (d *Deployer) Stop(ctx context.Context, app, env string) error {
	if err := checkNames(app, env); err != nil {
		return err
	}
	cs, err := d.api.containers(ctx, labelApp+"="+app, labelEnv+"="+env)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return nil
	}
	if _, err := d.compose(ctx, d.envDir(app, env), app, env, "stop"); err != nil {
		return err
	}
	// Callers copy the data folder next, so make sure the engine no longer reports the
	// container as running (it can lag behind compose after a slow, killed stop).
	for deadline := time.Now().Add(stopSettle); ; {
		cs, err := d.api.containers(ctx, labelApp+"="+app, labelEnv+"="+env)
		if err != nil {
			return err
		}
		running := false
		for _, c := range cs {
			running = running || c.State == "running" || c.State == "restarting"
		}
		if !running {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s/%s is still running after being stopped", app, env)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// stopSettle is how long Stop waits for the engine to report a stopped container.
const stopSettle = 15 * time.Second

// Observe reports one entry per environment Homelab has containers for.
func (d *Deployer) Observe(ctx context.Context) ([]core.Running, error) {
	cs, err := d.api.containers(ctx, labelApp)
	if err != nil {
		return nil, err
	}
	byEnv := map[[2]string][]container{}
	for _, c := range cs {
		app, env := c.Labels[labelApp], c.Labels[labelEnv]
		if app == "" || env == "" {
			continue
		}
		k := [2]string{app, env}
		byEnv[k] = append(byEnv[k], c)
	}
	out := make([]core.Running, 0, len(byEnv))
	for k, group := range byEnv {
		r, appID := envRunning(group)
		r.App, r.Env = k[0], k[1]
		if appID != "" {
			if t, err := d.api.startedAt(ctx, appID); err == nil && !t.IsZero() {
				r.StartedAt = t
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Env < out[j].Env
	})
	return out, nil
}

// envRunning sums up one environment's containers: the version is the app container's, and
// the state is the worst of all of them. So a service that stopped, a container that was
// created but never started, or a missing app container all make the environment exited,
// and the reconciler starts it again. It also returns the app container's id, or "".
func envRunning(cs []container) (core.Running, string) {
	var app *container
	for i := range cs {
		if isAppService(cs[i]) {
			app = &cs[i]
		}
	}
	if app == nil {
		return core.Running{State: core.StateExited, Detail: "the app's container is missing"}, ""
	}
	r := core.Running{Commit: app.Labels[labelCommit], Digest: app.Labels[labelDigest]}
	r.State, r.Detail = mapState(app.State, app.Status)
	names := make([]string, 0, len(cs))
	others := map[string]container{}
	for _, c := range cs {
		if !isAppService(c) {
			name := c.Labels["com.docker.compose.service"]
			names = append(names, name)
			others[name] = c
		}
	}
	sort.Strings(names) // the same detail every time
	for _, name := range names {
		c := others[name]
		state, detail := mapState(c.State, c.Status)
		if worse(state, r.State) {
			r.State, r.Detail = state, name+" "+detail
		}
	}
	return r, app.ID
}

// worse says whether state a is worse than b: exited, then restarting, then running.
func worse(a, b string) bool {
	rank := map[string]int{core.StateRunning: 0, core.StateRestarting: 1, core.StateExited: 2}
	return rank[a] > rank[b]
}

func isAppService(c container) bool { return c.Labels["com.docker.compose.service"] == serviceName }

var exitCodeRe = regexp.MustCompile(`\((-?\d+)\)`)

// mapState turns Docker's state and status text into a core state and a short explanation.
func mapState(state, status string) (string, string) {
	code := ""
	if m := exitCodeRe.FindStringSubmatch(status); m != nil {
		code = m[1]
	}
	switch state {
	case "running":
		return core.StateRunning, ""
	case "restarting":
		if code != "" {
			return core.StateRestarting, "restarting after exiting with code " + code
		}
		return core.StateRestarting, "restarting"
	case "created":
		// Compose creates and starts in one go, and Homelab only looks while no deploy runs,
		// so a container still "created" failed to start (e.g. its port was taken).
		return core.StateExited, "never started"
	case "paused":
		return core.StateExited, "paused"
	case "exited", "dead", "removing":
		if code != "" {
			return core.StateExited, "exited with code " + code
		}
		return core.StateExited, "exited"
	}
	return core.StateExited, "docker state: " + state
}

// Logs returns the last lines of the app's output (stdout and stderr together), at most about 1 MB.
func (d *Deployer) Logs(ctx context.Context, app, env string, lines int) (string, error) {
	if err := checkNames(app, env); err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = 200
	}
	out, err := d.compose(ctx, d.envDir(app, env), app, env, "logs", "--no-color", "--no-log-prefix", "--tail", fmt.Sprint(lines), serviceName)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// compose runs `docker compose -p <project> [-f compose.yaml] args...` in dir and returns
// stdout and stderr together (the last ~1 MB). On failure the error holds the end of the output.
func (d *Deployer) compose(ctx context.Context, dir, app, env string, args ...string) ([]byte, error) {
	return d.composeProject(ctx, dir, project(app, env), app+"/"+env, args...)
}

func (d *Deployer) composeProject(ctx context.Context, dir, projectName, label string, args ...string) ([]byte, error) {
	return d.composeProjectWithEnv(ctx, dir, projectName, label, nil, args...)
}

func (d *Deployer) composeProjectWithEnv(ctx context.Context, dir, projectName, label string, extraEnv map[string]string, args ...string) ([]byte, error) {
	full := []string{"compose", "-p", projectName}
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err == nil {
		full = append(full, "-f", "compose.yaml")
	} else {
		dir = d.root // no project folder yet: containers are still found by project name
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, d.bin, full...)
	cmd.Dir = dir
	cmd.Env = d.env()
	for k, v := range extraEnv {
		prefix := k + "="
		filtered := cmd.Env[:0]
		for _, existing := range cmd.Env {
			if !strings.HasPrefix(existing, prefix) {
				filtered = append(filtered, existing)
			}
		}
		cmd.Env = filtered
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out := &tailBuffer{max: maxLogBytes}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("docker compose %s failed for %s: %w\n%s", args[0], label, err, lastLines(out.Bytes(), errTailLines))
	}
	return out.Bytes(), nil
}

// lastLines returns the last n lines of b, trimmed.
func lastLines(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return n, nil
}

func (t *tailBuffer) Bytes() []byte { return t.buf.Bytes() }

// BridgeGateway is the address of Docker's default bridge on this computer (usually
// 172.17.0.1). On Linux, Homelab's app door listens there, and app containers reach it as
// host.docker.internal. It's only meaningful when Docker runs on this computer itself.
func (d *Deployer) BridgeGateway(ctx context.Context) (string, error) {
	var nw struct {
		IPAM struct {
			Config []struct {
				Gateway string `json:"Gateway"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := d.api.getJSON(ctx, "/networks/bridge", &nw); err != nil {
		return "", fmt.Errorf("look at Docker's bridge network: %w", err)
	}
	for _, c := range nw.IPAM.Config {
		if ip := net.ParseIP(c.Gateway); ip != nil && ip.To4() != nil {
			return ip.String(), nil
		}
	}
	return "", errors.New("Docker's bridge network has no IPv4 gateway")
}
