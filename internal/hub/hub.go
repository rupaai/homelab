// Package hub is Homelab's core: it keeps desired state, runs the reconciler, and carries
// out the builder actions (add app, deploy, promote, rollback). It only talks to adapters
// through the contracts in package core.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/identity"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// Config tunes the hub. Zero values get sensible defaults.
type Config struct {
	DataDir        string           // root of everything Homelab keeps, e.g. ~/.homelab
	HostPortBase   int              // app environments publish on 127.0.0.1:HostPortBase+slot*10+n
	ReconcileEvery time.Duration    // default 15s
	HealthEvery    time.Duration    // default 30s (O1)
	BuildsEvery    time.Duration    // default 60s (C4)
	HealthyWait    time.Duration    // how long promote and rollback wait for health; default 3m
	KeepBackups    int              // data copies kept per environment; default 5
	ImageHost      string           // registry for images a manifest doesn't name; default ghcr.io
	WatchFor       time.Duration    // how long prod is watched after a promote (C7); default 10m
	MirrorEvery    time.Duration    // how often repos are mirrored (G2); default 10m
	SmokeTimeout   time.Duration    // longest a smoke test may run; default 5m
	Owners         []string         // Tailscale logins of the owners, from config.yaml
	Identity       *identity.Signer // signs the identity headers apps get (A2); nil sends none
	Now            func() time.Time
	Logger         *slog.Logger
}

// Adapters are the blocks the hub drives.
type Adapters struct {
	Source    core.Source
	Builder   core.Builder
	Registry  core.Registry
	Deployer  core.Deployer
	Addresses core.Addresses
	Health    core.HealthChecker
	Secrets   core.Secrets
	Smoke     core.SmokeTester // may be nil: auto promote then refuses apps with a smoke test
	Mirror    core.Mirror      // may be nil: no mirrors
}

// Hub is the core. Create it with New and run it with Run.
type Hub struct {
	cfg   Config
	store *state.Store
	ad    Adapters
	log   *slog.Logger

	appLocksMu sync.Mutex
	appLocks   map[string]*sync.Mutex

	mu        sync.Mutex
	status    map[envKey]*envStatus
	builds    map[string]core.Build // latest build seen per app
	failed    map[string]string     // app -> failed build commit already logged
	addresses map[string]int        // address name -> host port it forwards to
	watches   map[string]*watch     // app -> prod being watched after a promote
	smoked    map[string]string     // app -> dev commit whose smoke test passed
	autoNote  map[string]autoState  // app -> what auto-promote is doing, for people
	kick      chan struct{}
}

// watch is prod being watched after a promote (C7).
type watch struct {
	until time.Time
	rel   core.Release
	prev  *core.Release
	fails int
}

type envKey struct{ app, env string }

type envStatus struct {
	health      string
	detail      string
	since       time.Time
	checkedAt   time.Time
	running     *core.Running
	busy        string // what an action is doing right now, for people
	lastErr     string
	lastErrAt   time.Time
	nextAttempt time.Time
}

// New creates a hub. It doesn't start anything yet.
func New(cfg Config, store *state.Store, ad Adapters) *Hub {
	if cfg.HostPortBase == 0 {
		cfg.HostPortBase = 17000
	}
	if cfg.ReconcileEvery == 0 {
		cfg.ReconcileEvery = 15 * time.Second
	}
	if cfg.HealthEvery == 0 {
		cfg.HealthEvery = 30 * time.Second
	}
	if cfg.BuildsEvery == 0 {
		cfg.BuildsEvery = time.Minute
	}
	if cfg.HealthyWait == 0 {
		cfg.HealthyWait = 3 * time.Minute
	}
	if cfg.KeepBackups == 0 {
		cfg.KeepBackups = 5
	}
	if cfg.ImageHost == "" {
		cfg.ImageHost = "ghcr.io"
	}
	if cfg.WatchFor == 0 {
		cfg.WatchFor = 10 * time.Minute
	}
	if cfg.MirrorEvery == 0 {
		cfg.MirrorEvery = 10 * time.Minute
	}
	if cfg.SmokeTimeout == 0 {
		cfg.SmokeTimeout = 5 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Hub{
		cfg: cfg, store: store, ad: ad, log: cfg.Logger,
		appLocks:  map[string]*sync.Mutex{},
		status:    map[envKey]*envStatus{},
		builds:    map[string]core.Build{},
		failed:    map[string]string{},
		addresses: map[string]int{},
		watches:   map[string]*watch{},
		smoked:    map[string]string{},
		autoNote:  map[string]autoState{},
		kick:      make(chan struct{}, 1),
	}
}

// Run runs the reconciler, health checks and build checks until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, Action: "homelab.started", Detail: "Homelab started and is checking that everything runs as it should"})
	go h.every(ctx, h.cfg.BuildsEvery, h.CheckBuilds)
	go h.every(ctx, h.cfg.HealthEvery, func(ctx context.Context) {
		h.CheckHealth(ctx)
		h.AutoPromote(ctx)
	})
	if h.ad.Mirror != nil {
		go h.every(ctx, h.cfg.MirrorEvery, h.SyncMirrors)
	}
	go h.every(ctx, 24*time.Hour, func(ctx context.Context) { _ = h.store.PruneEvents(ctx, 366*24*time.Hour) })
	t := time.NewTicker(h.cfg.ReconcileEvery)
	defer t.Stop()
	for {
		h.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.kick:
		}
	}
}

func (h *Hub) every(ctx context.Context, d time.Duration, f func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		f(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Kick asks the reconciler to run now instead of waiting for its next round.
func (h *Hub) Kick() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Hub) appLock(app string) *sync.Mutex {
	h.appLocksMu.Lock()
	defer h.appLocksMu.Unlock()
	l, ok := h.appLocks[app]
	if !ok {
		l = &sync.Mutex{}
		h.appLocks[app] = l
	}
	return l
}

func (h *Hub) st(app, env string) *envStatus {
	k := envKey{app, env}
	s, ok := h.status[k]
	if !ok {
		s = &envStatus{health: core.Unknown}
		h.status[k] = s
	}
	return s
}

func (h *Hub) logEvent(ctx context.Context, e state.Event) {
	if err := h.store.Log(ctx, e); err != nil {
		h.log.Error("activity log", "err", err)
	}
	h.log.Info(e.Action, "app", e.App, "env", e.Env, "actor", e.Actor, "detail", e.Detail)
}

// parse reads an app's manifest with the defaults that come from its repo.
func (h *Hub) parse(raw []byte, repo string) (*manifest.Manifest, error) {
	return manifest.Parse(raw, manifest.Defaults{Repo: repo, ImageHost: h.cfg.ImageHost})
}

// hostPort is where an environment's app is published on 127.0.0.1.
func (h *Hub) hostPort(slot int, env string) int {
	n := 0
	if env == core.Prod {
		n = 1
	}
	return h.cfg.HostPortBase + slot*10 + n
}

func (h *Hub) dataDir(app, env string) string {
	return filepath.Join(h.cfg.DataDir, "apps", app, env, "data")
}

// ---- Reconciler ----

// Reconcile makes every environment match its desired state, once.
func (h *Hub) Reconcile(ctx context.Context) {
	apps, err := h.store.Apps(ctx)
	if err != nil {
		h.log.Error("reconcile: read state", "err", err)
		return
	}
	running, err := h.ad.Deployer.Observe(ctx)
	if err != nil {
		h.log.Error("reconcile: observe", "err", err)
		return
	}
	byKey := map[envKey]core.Running{}
	for _, r := range running {
		byKey[envKey{r.App, r.Env}] = r
	}
	known := map[string]bool{}
	for _, a := range apps {
		known[a.ID] = true
		lock := h.appLock(a.ID)
		if !lock.TryLock() {
			continue // an action is working on this app; it reconciles it itself
		}
		for _, env := range []string{core.Dev, core.Prod} {
			var r *core.Running
			if x, ok := byKey[envKey{a.ID, env}]; ok {
				r = &x
			}
			h.reconcileEnv(ctx, a, env, r)
		}
		lock.Unlock()
	}
	// Environments of apps that are no longer registered: stop them, keep their data.
	for k := range byKey {
		if !known[k.app] {
			if err := h.ad.Deployer.Stop(ctx, k.app, k.env); err == nil {
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: k.app, Env: k.env, Action: "env.stopped", Detail: "Stopped, because the app isn't registered any more"})
			}
		}
	}
}

func (h *Hub) reconcileEnv(ctx context.Context, a state.App, env string, r *core.Running) {
	e, err := h.store.Env(ctx, a.ID, env)
	if err != nil {
		h.log.Error("reconcile: read env", "app", a.ID, "env", env, "err", err)
		return
	}
	h.mu.Lock()
	h.st(a.ID, env).running = r
	h.mu.Unlock()

	name := manifest.AddressName(a.ID, env)
	if e.Want == nil {
		if r != nil {
			_ = h.ad.Deployer.Stop(ctx, a.ID, env)
		}
		h.removeAddress(ctx, name)
		return
	}
	needsDeploy := r == nil || r.Digest != e.Want.Digest || r.State == core.StateExited
	if needsDeploy {
		h.mu.Lock()
		s := h.st(a.ID, env)
		wait := h.cfg.Now().Before(s.nextAttempt)
		h.mu.Unlock()
		if !wait {
			why := "it wasn't running"
			if r != nil && r.Digest != e.Want.Digest {
				why = "a different version was running"
			} else if r != nil {
				why = "it had stopped"
			}
			h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: env, Action: "deploy.started",
				Detail: fmt.Sprintf("Starting %s, because %s", e.Want.Short(), why)})
			if err := h.deploy(ctx, a, env, *e.Want); err != nil {
				h.deployFailed(ctx, a.ID, env, err, true)
			} else {
				h.deployOK(a.ID, env)
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: env, Action: "deploy.done", Detail: e.Want.Short() + " is running"})
			}
		}
	}
	h.ensureAddress(ctx, name, h.hostPort(a.Slot, env), a.ID, a.Name, env)
}

func (h *Hub) deployOK(app, env string) {
	h.mu.Lock()
	s := h.st(app, env)
	s.lastErr, s.nextAttempt = "", time.Time{}
	s.health, s.detail, s.since = core.Unknown, "Just started", h.cfg.Now()
	h.mu.Unlock()
}

// deployFailed records a failed start. The reconciler logs only the first of a streak.
func (h *Hub) deployFailed(ctx context.Context, app, env string, err error, logIt bool) {
	h.mu.Lock()
	s := h.st(app, env)
	first := s.lastErr == ""
	s.lastErr, s.lastErrAt = err.Error(), h.cfg.Now()
	s.nextAttempt = h.cfg.Now().Add(time.Minute)
	h.mu.Unlock()
	if first && logIt {
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: app, Env: env, Action: "deploy.failed", Detail: "Couldn't start it, will keep trying every minute: " + err.Error()})
	}
}

// spec builds what the deploy adapter needs for one release in one environment.
func (h *Hub) spec(ctx context.Context, a state.App, env string, rel core.Release) (core.EnvSpec, error) {
	m, err := h.parse(rel.Manifest, a.Repo)
	if err != nil {
		return core.EnvSpec{}, err
	}
	vars := m.VarsFor(env)
	secrets, err := h.ad.Secrets.Inject(a.ID, env, m.Secrets)
	if err != nil {
		return core.EnvSpec{}, fmt.Errorf("read secrets: %w", err)
	}
	for k, v := range secrets {
		vars[k] = v
	}
	vars["HOMELAB_APP"], vars["HOMELAB_ENV"], vars["HOMELAB_COMMIT"] = a.ID, env, rel.Commit
	if h.cfg.Identity != nil {
		vars[identity.EnvPublicKey] = h.cfg.Identity.PublicKey()
	}
	var mounts []core.Mount
	for _, d := range m.Data {
		dir := filepath.Join(h.dataDir(a.ID, env), d.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return core.EnvSpec{}, err
		}
		mounts = append(mounts, core.Mount{HostPath: dir, ContainerPath: d.Path})
	}
	auth, err := h.ad.Registry.PullAuth(ctx, rel.Image)
	if err != nil {
		return core.EnvSpec{}, fmt.Errorf("registry sign-in: %w", err)
	}
	return core.EnvSpec{
		App: a.ID, Env: env, Release: rel, Port: m.Port, HostPort: h.hostPort(a.Slot, env),
		Memory: m.Memory, Mounts: mounts, Vars: vars, Auth: auth,
	}, nil
}

func (h *Hub) deploy(ctx context.Context, a state.App, env string, rel core.Release) error {
	spec, err := h.spec(ctx, a, env, rel)
	if err != nil {
		return err
	}
	if err := h.ad.Deployer.Prepare(ctx, spec); err != nil {
		return err
	}
	return h.ad.Deployer.Deploy(ctx, spec)
}

// ---- Addresses ----

func (h *Hub) ensureAddress(ctx context.Context, name string, port int, app, appName, env string) {
	h.mu.Lock()
	cur, ok := h.addresses[name]
	h.mu.Unlock()
	if ok && cur == port {
		return
	}
	if err := h.ad.Addresses.Ensure(ctx, name, h.appProxy(port, app, appName, env)); err != nil {
		h.log.Error("address", "name", name, "err", err)
		return
	}
	h.mu.Lock()
	h.addresses[name] = port
	h.mu.Unlock()
}

func (h *Hub) removeAddress(ctx context.Context, name string) {
	h.mu.Lock()
	_, ok := h.addresses[name]
	delete(h.addresses, name)
	h.mu.Unlock()
	if ok {
		_ = h.ad.Addresses.Remove(ctx, name)
	}
}

// appProxy forwards an address's traffic to the app's published port, with signed headers
// saying who is asking (A2).
func (h *Hub) appProxy(port int, app, appName, env string) http.Handler {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	p := httputil.NewSingleHostReverseProxy(target)
	base := p.Director
	p.Director = func(r *http.Request) {
		// Only Homelab may set its identity headers, so drop any a client sent.
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-homelab-") {
				r.Header.Del(k)
			}
		}
		base(r)
		r.Header.Set("X-Forwarded-Proto", "https")
		if h.cfg.Identity != nil {
			v, _ := core.ViewerFrom(r.Context())
			if err := h.cfg.Identity.Apply(r, app, env, h.person(v), h.cfg.Now()); err != nil {
				h.log.Warn("identity headers", "app", app, "err", err)
				_ = h.cfg.Identity.Apply(r, app, env, nil, h.cfg.Now())
			}
		}
	}
	label := appName
	if env != core.Prod {
		label += " (" + env + ")"
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `<!doctype html><meta name="viewport" content="width=device-width"><title>Not running</title>`+
			`<body style="font-family:system-ui;padding:2rem;max-width:30rem;margin:auto"><h1>%s isn't answering right now</h1>`+
			`<p>It may be starting up. Try again in a moment, or check it in the Homelab companion.</p>`, htmlEscape(label))
	}
	return p
}

// person is who a viewer is to apps. Milestone 2 knows only the owners; everyone else is
// anonymous (nil). Real people and roles arrive in milestone 3 on the same headers.
func (h *Hub) person(v core.Viewer) *identity.Person {
	switch {
	case v.Tagged:
		return nil
	case v.Local:
		return &identity.Person{User: "owner", Name: "Owner", Groups: []string{"role:owner"}}
	case v.Login == "":
		return nil
	}
	if len(h.cfg.Owners) == 0 { // as the companion: with no owners listed, every person on the tailnet is one
		return &identity.Person{User: v.Login, Name: firstNonEmpty(v.Name, v.Login), Groups: []string{"role:owner"}}
	}
	for _, o := range h.cfg.Owners {
		if strings.EqualFold(strings.TrimSpace(o), v.Login) {
			return &identity.Person{User: v.Login, Name: firstNonEmpty(v.Name, v.Login), Groups: []string{"role:owner"}}
		}
	}
	return nil
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// ---- Health (O1) ----

// CheckHealth probes every environment that should be running, once.
func (h *Hub) CheckHealth(ctx context.Context) {
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return
	}
	running, err := h.ad.Deployer.Observe(ctx)
	if err != nil {
		return
	}
	byKey := map[envKey]core.Running{}
	for _, r := range running {
		byKey[envKey{r.App, r.Env}] = r
	}
	for _, a := range apps {
		for _, env := range []string{core.Dev, core.Prod} {
			e, err := h.store.Env(ctx, a.ID, env)
			if err != nil || e.Want == nil {
				continue
			}
			var r *core.Running
			if x, ok := byKey[envKey{a.ID, env}]; ok {
				r = &x
			}
			status := h.checkEnv(ctx, a, env, e.Want, r)
			if env == core.Prod {
				h.watchStep(ctx, a, e.Want, status)
			}
		}
	}
}

// checkEnv probes one environment and records the result. It returns the new status.
func (h *Hub) checkEnv(ctx context.Context, a state.App, env string, want *core.Release, r *core.Running) string {
	status, detail := core.Down, "Not running"
	switch {
	case r == nil:
	case r.State != core.StateRunning:
		detail = "Not running: " + firstNonEmpty(r.Detail, r.State)
	case r.Digest != want.Digest:
		detail = "Running an older version while the new one starts"
		status = core.Degraded
	default:
		m, err := h.parse(want.Manifest, a.Repo)
		if err != nil {
			status, detail = core.Degraded, err.Error()
			break
		}
		u := fmt.Sprintf("http://127.0.0.1:%d%s", h.hostPort(a.Slot, env), m.Health)
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		res := h.ad.Health.Check(cctx, u)
		cancel()
		if res.OK {
			status, detail = core.Healthy, fmt.Sprintf("Health check passed in %d ms", res.Latency.Milliseconds())
		} else {
			status, detail = core.Degraded, "Running, but its health check fails: "+res.Detail
		}
	}

	h.mu.Lock()
	s := h.st(a.ID, env)
	prev := s.health
	s.running, s.checkedAt, s.detail = r, h.cfg.Now(), detail
	if prev != status {
		s.health, s.since = status, h.cfg.Now()
	}
	h.mu.Unlock()

	if prev != status && !(prev == core.Unknown && status == core.Healthy) {
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: env, Action: "health." + status, Detail: detail})
	}
	return status
}

// waitHealthy probes an environment until it's healthy or the wait runs out.
func (h *Hub) waitHealthy(ctx context.Context, a state.App, env string, want *core.Release) (bool, string) {
	deadline := h.cfg.Now().Add(h.cfg.HealthyWait)
	for {
		running, err := h.ad.Deployer.Observe(ctx)
		if err == nil {
			var r *core.Running
			for i := range running {
				if running[i].App == a.ID && running[i].Env == env {
					r = &running[i]
				}
			}
			if h.checkEnv(ctx, a, env, want, r) == core.Healthy {
				return true, ""
			}
		}
		if !h.cfg.Now().Before(deadline) {
			h.mu.Lock()
			d := h.st(a.ID, env).detail
			h.mu.Unlock()
			return false, d
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err().Error()
		case <-time.After(2 * time.Second):
		}
	}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---- Data copies before promote ----

// backupData copies an environment's data folder while its app is stopped, so the copy is
// consistent (SQLite files included), and keeps the newest KeepBackups copies.
func (h *Hub) backupData(app, env, label string) (string, error) {
	src := h.dataDir(app, env)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return "", nil // nothing to copy yet
	}
	root := filepath.Join(h.cfg.DataDir, "backups", app, env)
	dst := filepath.Join(root, h.cfg.Now().UTC().Format("20060102-150405")+"-"+label)
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return "", err
	}
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > h.cfg.KeepBackups {
		os.RemoveAll(filepath.Join(root, names[0]))
		names = names[1:]
	}
	return dst, nil
}

// restoreData replaces an environment's data with a copy made by backupData. The data it
// replaces is moved next to the copies (named after label) rather than deleted. The app
// must be stopped.
func (h *Hub) restoreData(app, env, from, label string) (kept string, err error) {
	dir := h.dataDir(app, env)
	kept = filepath.Join(h.cfg.DataDir, "backups", app, env, h.cfg.Now().UTC().Format("20060102-150405")+"-replaced-by-restore-"+label)
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(dir, kept); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := copyTree(from, dir); err != nil {
		return kept, err
	}
	return kept, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		}
		return nil // sockets and the like aren't data
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
