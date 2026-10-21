// Package hub is Homelab's core: it keeps desired state, runs the reconciler, and carries
// out the builder actions (add app, deploy, promote, rollback). It only talks to adapters
// through the contracts in package core.
package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/rupaai/homelab/internal/sharedir"
	"github.com/rupaai/homelab/internal/state"
)

// Config tunes the hub. Zero values get sensible defaults.
type Config struct {
	DataDir        string            // root of everything Homelab keeps, e.g. ~/.homelab
	HostPortBase   int               // app environments publish on 127.0.0.1:HostPortBase+slot*10+n
	ReconcileEvery time.Duration     // default 15s
	HealthEvery    time.Duration     // default 30s (O1)
	BuildsEvery    time.Duration     // default 60s (C4)
	HealthyWait    time.Duration     // how long promote and rollback wait for health; default 3m
	KeepBackups    int               // data copies kept per environment; default 5
	ImageHost      string            // registry for images a manifest doesn't name; default ghcr.io
	WatchFor       time.Duration     // how long prod is watched after a promote (C7); default 10m
	MirrorEvery    time.Duration     // how often repos are mirrored (G2); default 10m
	SmokeTimeout   time.Duration     // longest a smoke test may run; default 5m
	Identity       *identity.Signer  // signs the identity headers apps get (A2); nil sends none
	PushPublicKey  string            // VAPID public key browsers subscribe with (O4)
	AppAPIURL      string            // where apps reach the Homelab API (the app door), e.g. http://host.docker.internal:7085
	OpenApps       bool              // addresses: ip; anyone who reaches an app's port may use it, without sign-in
	Folders        map[string]string // the device's shared folders apps may ask for (config.yaml folders:), e.g. media: /mnt/storage
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
	Identity  core.Identity
	Email     core.Email
	Notifier  core.Notifier  // may be nil: no push notifications
	Resources core.Resources // may be nil: no resource view or disk alert
	System    core.System    // may be nil: no device card (milestone 6)
	Updater   core.Updater   // may be nil: Homelab doesn't update itself (milestone 6)
	Services  core.Services  // may be nil: no files, databases or data (platform services)
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

	diskCheckedAt time.Time // when the disk was last checked for the full-disk alert
	kick          chan struct{}

	keyMu      sync.Mutex
	keyTouched map[string]time.Time // key id -> last "last used" written
	keyLogged  map[string]time.Time // key id + read -> last time it was logged
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
		appLocks:   map[string]*sync.Mutex{},
		status:     map[envKey]*envStatus{},
		builds:     map[string]core.Build{},
		failed:     map[string]string{},
		addresses:  map[string]int{},
		watches:    map[string]*watch{},
		smoked:     map[string]string{},
		autoNote:   map[string]autoState{},
		kick:       make(chan struct{}, 1),
		keyTouched: map[string]time.Time{},
		keyLogged:  map[string]time.Time{},
	}
}

// Run runs the reconciler, health checks and build checks until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, Action: "homelab.started", Detail: "Homelab started and is checking that everything runs as it should"})
	go h.every(ctx, h.cfg.BuildsEvery, h.CheckBuilds)
	go h.every(ctx, h.cfg.HealthEvery, func(ctx context.Context) {
		h.CheckHealth(ctx)
		h.AutoPromote(ctx)
		h.checkAlerts(ctx)
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
	h.reconcilePeople(ctx)
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
	known := map[string]bool{}
	for _, a := range apps {
		known[a.ID] = true
		lock := h.appLock(a.ID)
		if !lock.TryLock() {
			continue // an action is working on this app; it reconciles it itself
		}
		// Look only now, with the lock held: an earlier look may predate an action's end.
		envs, err := h.observeApp(ctx, a.ID)
		if err != nil {
			lock.Unlock()
			h.log.Error("reconcile: observe", "err", err)
			continue
		}
		for _, env := range []string{core.Dev, core.Prod} {
			h.reconcileEnv(ctx, a, env, envs[env])
		}
		lock.Unlock()
	}
	// Environments of apps that are no longer registered: stop them, keep their data.
	for i := range running {
		r := &running[i]
		if !known[r.App] {
			if stopped, _ := h.stopIfRunning(ctx, r.App, r.Env, r); stopped {
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: r.App, Env: r.Env, Action: "env.stopped", Detail: "Stopped, because the app isn't registered any more"})
			}
		}
	}
}

// observeApp reports what runs for one app, by environment.
func (h *Hub) observeApp(ctx context.Context, app string) (map[string]*core.Running, error) {
	running, err := h.ad.Deployer.Observe(ctx)
	if err != nil {
		return nil, err
	}
	envs := map[string]*core.Running{}
	for i := range running {
		if running[i].App == app {
			envs[running[i].Env] = &running[i]
		}
	}
	return envs, nil
}

// stopIfRunning stops an environment's containers unless they are known to be stopped
// already (r is what was observed, or nil if unknown). It says whether it stopped them.
func (h *Hub) stopIfRunning(ctx context.Context, app, env string, r *core.Running) (bool, error) {
	if r != nil && r.State == core.StateExited {
		return false, nil
	}
	if err := h.ad.Deployer.Stop(ctx, app, env); err != nil {
		return false, err
	}
	return true, nil
}

func (h *Hub) reconcilePeople(ctx context.Context) {
	if h.ad.Identity == nil {
		return
	}
	people, err := h.store.People(ctx)
	if err != nil {
		h.log.Error("people reconcile: read state", "err", err)
		return
	}
	observed, err := h.ad.Identity.Observe(ctx)
	if err != nil {
		h.log.Warn("people reconcile: observe sign-in service", "err", err)
		return
	}
	byID := make(map[string]core.IdentityUser, len(observed))
	for _, user := range observed {
		byID[user.ID] = user
	}
	for _, person := range people {
		want := personGroups(person)
		user, found := byID[providerID(person)]
		if found && identityMatches(user, person, want) {
			// Nothing to restore: don't call the sign-in service.
			if user.ID != person.IdentityID {
				if err := h.store.RecordIdentityID(ctx, person.ID, user.ID); err != nil {
					h.log.Warn("people reconcile: save sign-in account id", "person", person.ID, "err", err)
				}
			}
			continue
		}
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, Action: "person.identity_changed", Detail: "Restored sign-in account settings for " + person.Name})
		identityID, err := h.ad.Identity.Ensure(ctx, person, want)
		if err != nil {
			h.log.Warn("people reconcile: update sign-in account", "person", person.ID, "err", err)
			continue
		}
		if identityID != "" && identityID != person.IdentityID {
			if err := h.store.RecordIdentityID(ctx, person.ID, identityID); err != nil {
				h.log.Warn("people reconcile: save sign-in account id", "person", person.ID, "err", err)
			}
		}
	}
}

func identityMatches(user core.IdentityUser, person core.Person, groups []string) bool {
	if user.Email != person.Email || user.Name != person.Name || user.Disabled {
		return false
	}
	if len(user.Groups) != len(groups) {
		return false
	}
	have := map[string]bool{}
	for _, group := range user.Groups {
		have[group] = true
	}
	for _, group := range groups {
		if !have[group] {
			return false
		}
	}
	return true
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
			_, _ = h.stopIfRunning(ctx, a.ID, env, r)
		}
		h.removeAddress(ctx, name)
		return
	}
	needsDeploy := r == nil || r.Digest != e.Want.Digest || r.State == core.StateExited || r.Incomplete
	if needsDeploy {
		h.mu.Lock()
		s := h.st(a.ID, env)
		wait := h.cfg.Now().Before(s.nextAttempt)
		h.mu.Unlock()
		if !wait {
			why := "it wasn't running"
			if r != nil && r.Digest != e.Want.Digest {
				why = "a different version was running"
			} else if r != nil && r.State != core.StateExited {
				why = r.Detail // a service next to the app had stopped
			} else if r != nil {
				why = "it had stopped"
			}
			h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: env, Action: "deploy.started",
				Detail: fmt.Sprintf("Starting %s, because %s", e.Want.Short(), why)})
			if err := h.deploy(ctx, a, env, *e.Want); err != nil {
				h.deployFailed(ctx, a.ID, env, err, true)
			} else {
				h.deployOK(a.ID, env)
				if r != nil && r.Digest == e.Want.Digest {
					// A restart: if it stops again right away, wait a minute before the next one.
					h.mu.Lock()
					h.st(a.ID, env).nextAttempt = h.cfg.Now().Add(time.Minute)
					h.mu.Unlock()
				}
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
	if err := h.ensureGenerated(m, a.ID, env); err != nil {
		return core.EnvSpec{}, err
	}
	secrets, err := h.ad.Secrets.Inject(a.ID, env, m.SecretNames())
	if err != nil {
		return core.EnvSpec{}, fmt.Errorf("read secrets: %w", err)
	}
	if m.SecretsAs != manifest.SecretsAsFiles {
		for k, v := range secrets {
			vars[k] = v
		}
	}
	vars["HOMELAB_APP"], vars["HOMELAB_ENV"], vars["HOMELAB_COMMIT"] = a.ID, env, rel.Commit
	// The app's own API key, and where to use it (platform services).
	token, err := h.appKey(ctx, a.ID, env)
	if err != nil {
		return core.EnvSpec{}, err
	}
	vars[AppTokenSecret] = token
	if h.cfg.AppAPIURL != "" {
		vars["HOMELAB_API_URL"] = h.cfg.AppAPIURL
	}
	if h.cfg.Identity != nil {
		vars[identity.EnvPublicKey] = h.cfg.Identity.PublicKey()
	}
	mounts, err := h.mounts(a.ID, env, m.Data, m.Folders)
	if err != nil {
		return core.EnvSpec{}, err
	}
	auth, err := h.ad.Registry.PullAuth(ctx, rel.Image)
	if err != nil {
		return core.EnvSpec{}, fmt.Errorf("registry sign-in: %w", err)
	}
	spec := core.EnvSpec{
		App: a.ID, Env: env, Release: rel, Port: m.Port, HostPort: h.hostPort(a.Slot, env),
		Memory: m.Memory, Mounts: mounts, Vars: vars, Auth: auth, OpenPorts: h.cfg.OpenApps,
		Secrets: secrets, SecretsAsFiles: m.SecretsAs == manifest.SecretsAsFiles,
	}
	// Ports are fixed numbers on the device, so only one environment can have them: prod.
	if env == core.Prod {
		spec.Ports = m.Ports
	}
	for _, s := range m.Services {
		mounts, err := h.mounts(a.ID, env, s.Data, s.Folders)
		if err != nil {
			return core.EnvSpec{}, fmt.Errorf("%s: %w", s.Name, err)
		}
		svc := core.Service{Name: s.Name, Image: s.Image, Memory: s.Memory, Mounts: mounts, Vars: s.Env, GPU: s.GPU}
		if env == core.Prod {
			svc.Ports = s.Ports
		}
		spec.Services = append(spec.Services, svc)
	}
	return spec, nil
}

// mounts makes an environment's data folders and finds the shared folders a container asks for.
func (h *Hub) mounts(app, env string, data, folders []manifest.Data) ([]core.Mount, error) {
	var mounts []core.Mount
	for _, d := range data {
		dir := filepath.Join(h.dataDir(app, env), d.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := sharedir.Open(dir); err != nil {
			return nil, fmt.Errorf("let the app write its data: %w", err)
		}
		mounts = append(mounts, core.Mount{HostPath: dir, ContainerPath: d.Path})
	}
	for _, f := range folders {
		// Prod gets the device's folder; dev gets "<name>-dev" if the device has one, and
		// otherwise none, so dev can't change prod's files.
		name := f.Name
		if env != core.Prod {
			name += "-" + env
		}
		dir, ok := h.cfg.Folders[name]
		if !ok && env != core.Prod {
			continue
		}
		if !ok {
			return nil, fmt.Errorf("it needs the shared folder %q; add it to config.yaml under folders:, e.g. \"folders: {%s: /mnt/storage}\", and restart Homelab", f.Name, f.Name)
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("the shared folder %q (%s) isn't a folder on this device", f.Name, dir)
		}
		mounts = append(mounts, core.Mount{HostPath: dir, ContainerPath: f.Path})
	}
	return mounts, nil
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
		v, ok := core.ViewerFrom(r.Context())
		person, err := h.store.Person(r.Context(), v.PersonID)
		if !h.cfg.OpenApps && (!ok || err != nil || !Can(person, ActionApps, app) || (env == core.Dev && !Can(person, ActionProjects, app))) {
			return
		}
		// Only Homelab may set its identity headers, so drop any a client sent.
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-homelab-") {
				r.Header.Del(k)
			}
		}
		base(r)
		proto := "http"
		if r.TLS != nil {
			proto = "https"
		}
		r.Header.Set("X-Forwarded-Proto", proto)
		if h.cfg.Identity != nil {
			v, _ := core.ViewerFrom(r.Context())
			if err := h.cfg.Identity.Apply(r, app, env, h.person(v), h.cfg.Now()); err != nil {
				h.log.Warn("identity headers", "app", app, "err", err)
				_ = h.cfg.Identity.Apply(r, app, env, nil, h.cfg.Now())
			}
		}
	}
	serve := p.ServeHTTP
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.OpenApps {
			// addresses: ip: the app is open to whoever reaches its port, and gets no person.
			// Homelab's own cookies reach this port too (same host), so they are dropped.
			h.serveAs(w, r, core.Person{}, serve)
			return
		}
		if r.URL.Path == "/_homelab/callback" {
			h.appCallback(w, r, app, env)
			return
		}
		person, ok := h.appSessionPerson(r, app, env)
		if !ok {
			// The companion's sign-in is not visible on this port. Send the person through the
			// companion, which checks the app grant and hands them back with a code.
			returnURL := "http://" + r.Host + r.URL.RequestURI()
			query := url.Values{"app": {app}, "env": {env}, "return": {returnURL}}
			http.Redirect(w, r, "http://localhost:7080/auth/app?"+query.Encode(), http.StatusSeeOther)
			return
		}
		if !Can(person, ActionApps, app) || (env == core.Dev && !Can(person, ActionProjects, app)) {
			h.proxyForbidden(w)
			return
		}
		h.serveAs(w, r, person, serve)
	})
}

// serveAs forwards r to the app as person, without Homelab's own headers and cookies.
func (h *Hub) serveAs(w http.ResponseWriter, r *http.Request, person core.Person, serve http.HandlerFunc) {
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-homelab-") {
			r.Header.Del(name)
		}
	}
	kept := forwardedCookies(r.Header.Values("Cookie"))
	r.Header.Del("Cookie")
	if kept != "" {
		r.Header.Set("Cookie", kept)
	}
	r = r.WithContext(core.WithViewer(r.Context(), core.Viewer{PersonID: person.ID, Login: person.Email, Name: person.Name}))
	serve(w, r)
}

func forwardedCookies(headers []string) string {
	var kept []string
	for _, header := range headers {
		start, quoted, escaped := 0, false, false
		for i := 0; i <= len(header); i++ {
			if i < len(header) {
				c := header[i]
				if escaped {
					escaped = false
					continue
				}
				if quoted && c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					quoted = !quoted
					continue
				}
				if c != ';' || quoted {
					continue
				}
			}
			part := strings.TrimSpace(header[start:i])
			start = i + 1
			if part == "" {
				continue
			}
			name, _, hasValue := strings.Cut(part, "=")
			if hasValue && strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "homelab_") {
				continue
			}
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

func (h *Hub) proxyForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html><meta name="viewport" content="width=device-width"><title>Access denied</title><p>You don't have access to this app.</p>`))
}

func (h *Hub) appSessionPerson(r *http.Request, app, env string) (core.Person, bool) {
	c, err := r.Cookie("homelab_app_" + app + "_" + env)
	if err != nil {
		return core.Person{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 32 {
		return core.Person{}, false
	}
	hash := sha256.Sum256(raw)
	p, err := h.store.AppSessionPerson(r.Context(), hash[:], app, env, h.cfg.Now(), 8*time.Hour)
	return p, err == nil && Can(p, ActionApps, app) && (env != core.Dev || Can(p, ActionProjects, app))
}

func (h *Hub) appCallback(w http.ResponseWriter, r *http.Request, app, env string) {
	if r.Method != http.MethodGet {
		http.Error(w, "That app link isn't valid.", http.StatusMethodNotAllowed)
		return
	}
	code, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("code"))
	if err != nil || len(code) != 32 {
		http.Error(w, "That app link has expired. Open the app again from Homelab.", http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256(code)
	grant, err := h.store.ConsumeAppCode(r.Context(), digest[:], h.cfg.Now())
	if err != nil || grant.App != app || grant.Env != env {
		http.Error(w, "That app link has expired. Open the app again from Homelab.", http.StatusBadRequest)
		return
	}
	person, err := h.store.Person(r.Context(), grant.PersonID)
	if err != nil || !Can(person, ActionApps, app) || (env == core.Dev && !Can(person, ActionProjects, app)) {
		h.proxyForbidden(w)
		return
	}
	path := grant.ReturnPath
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") {
		path = "/"
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "Homelab couldn't open that app. Try again.", http.StatusInternalServerError)
		return
	}
	now := h.cfg.Now()
	hash := sha256.Sum256(raw)
	if err := h.store.PutAppSession(r.Context(), hash[:], person.ID, app, env, now, now.Add(24*time.Hour)); err != nil {
		http.Error(w, "Homelab couldn't open that app. Try again.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "homelab_app_" + app + "_" + env, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 24 * 60 * 60})
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// person is the signed-in household member an app receives.
func (h *Hub) person(v core.Viewer) *identity.Person {
	if v.PersonID != "" {
		p, err := h.store.Person(context.Background(), v.PersonID)
		if err != nil || p.Status != "signed_in" {
			return nil
		}
		groups := make([]string, 0, len(p.Roles)+len(p.Grants))
		for _, role := range p.Roles {
			groups = append(groups, "role:"+role)
		}
		for app := range p.Grants {
			groups = append(groups, "app:"+app)
		}
		sort.Strings(groups)
		return &identity.Person{User: firstNonEmpty(p.Email, p.ID), Name: p.Name, Groups: groups}
	}
	// A local connection isn't a household session.
	// The API session and app-session cookies provide real identities in milestone 3.
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
	for _, a := range apps {
		h.checkApp(ctx, a)
	}
}

// checkApp probes one app's environments. It skips an app an action is working on.
func (h *Hub) checkApp(ctx context.Context, a state.App) {
	lock := h.appLock(a.ID)
	if !lock.TryLock() {
		return
	}
	// Look only now, with the lock held, so a deploy that just finished isn't seen half done.
	envs, err := h.observeApp(ctx, a.ID)
	var prod *core.Release
	var status string
	if err == nil {
		for _, env := range []string{core.Dev, core.Prod} {
			e, err := h.store.Env(ctx, a.ID, env)
			if err != nil || e.Want == nil {
				continue
			}
			s := h.checkEnv(ctx, a, env, e.Want, envs[env])
			if env == core.Prod {
				prod, status = e.Want, s
			}
		}
	}
	lock.Unlock() // watchStep may need the lock to roll back
	if prod != nil {
		h.watchStep(ctx, a, prod, status)
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
			if r.Incomplete {
				detail += " (" + r.Detail + ")"
			}
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
	if status == core.Healthy {
		// The wanted version runs and answers, however it got started (e.g. by hand), so an
		// earlier failed start no longer applies.
		s.lastErr, s.nextAttempt = "", time.Time{}
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

// backupData copies the app's own data folders (names) of an environment while its app is
// stopped, so the copy is consistent (SQLite files included), and keeps the newest
// KeepBackups copies. Service data (Jellyfin's config, say) isn't copied. The copies are made
// by a helper container, so files that belong to other users are kept as they are.
func (h *Hub) backupData(ctx context.Context, app, env, label string, names []string) (string, error) {
	var have []string
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(h.dataDir(app, env), n)); err == nil {
			have = append(have, n)
		}
	}
	if len(have) == 0 {
		return "", nil // nothing to copy yet
	}
	root := filepath.Join(h.cfg.DataDir, "backups", app, env)
	dst := filepath.Join(root, h.cfg.Now().UTC().Format("20060102-150405")+"-"+label)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	for _, n := range have {
		if err := h.ad.Deployer.Copy(ctx, filepath.Join(h.dataDir(app, env), n), filepath.Join(dst, n)); err != nil {
			h.ad.Deployer.Remove(ctx, dst)
			return "", err
		}
	}
	entries, _ := os.ReadDir(root)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	for len(dirs) > h.cfg.KeepBackups {
		h.ad.Deployer.Remove(ctx, filepath.Join(root, dirs[0]))
		dirs = dirs[1:]
	}
	return dst, nil
}

// restoreData replaces the data folders found in a copy made by backupData. The folders it
// replaces are moved next to the copies (named after label) rather than deleted. The app
// must be stopped.
func (h *Hub) restoreData(ctx context.Context, app, env, from, label string) (kept string, err error) {
	entries, err := os.ReadDir(from)
	if err != nil {
		return "", err
	}
	kept = filepath.Join(h.cfg.DataDir, "backups", app, env, h.cfg.Now().UTC().Format("20060102-150405")+"-replaced-by-restore-"+label)
	if err := os.MkdirAll(kept, 0o700); err != nil {
		return "", err
	}
	for _, e := range entries {
		dir := filepath.Join(h.dataDir(app, env), e.Name())
		if err := h.ad.Deployer.Move(ctx, dir, filepath.Join(kept, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return kept, err
		}
		if err := h.ad.Deployer.Copy(ctx, filepath.Join(from, e.Name()), dir); err != nil {
			return kept, err
		}
	}
	return kept, nil
}
