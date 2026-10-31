// Package hub is Homelab's core: it keeps desired state, runs the reconciler, and carries
// out the builder actions (add app, deploy, promote, rollback). It only talks to adapters
// through the contracts in package core.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
	Storage        []core.Disk       // where homelab-services keeps files (config.yaml storage:)
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
	Databases core.Databases // may be nil: homelab-services isn't told about databases
	Versions  core.Versions  // may be nil: builds get no release versions
	Logs      core.Logs      // may be nil: logs aren't stored
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

	versionProblem map[string]string    // app -> why versions can't be made now
	versionTried   map[string]time.Time // app -> when its first release was last tried
	versionDone    map[string]bool      // app -> its first release is settled

	diskCheckedAt time.Time // when the disk was last checked for the full-disk alert
	kick          chan struct{}

	keyMu      sync.Mutex
	keyTouched map[string]time.Time // key id -> last "last used" written
	keyLogged  map[string]time.Time // key id + read -> last time it was logged

	logMu      sync.Mutex
	logMasks   map[envKey]logMask // app environment -> what IngestLogs needs, for a minute
	logSending chan struct{}      // limits the activity events being sent to Logs at once
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

		versionProblem: map[string]string{},
		versionTried:   map[string]time.Time{},
		versionDone:    map[string]bool{},
		kick:           make(chan struct{}, 1),
		keyTouched:     map[string]time.Time{},
		keyLogged:      map[string]time.Time{},
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
	h.sendLogEvent(e)
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

// dbPort is where an environment's postgres is published on 127.0.0.1.
func (h *Hub) dbPort(slot int, env string) int { return h.hostPort(slot, env) + 5 }

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
	running, err := h.ad.Deployer.ObserveApp(ctx, app)
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
				Detail: fmt.Sprintf("Starting %s, because %s", e.Want.Label(), why)})
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
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: env, Action: "deploy.done", Detail: e.Want.Label() + " is running"})
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

// noSecrets gives the plain message when the secrets service is down, and any other error
// as it is.
func noSecrets(err error) error {
	if errors.Is(err, core.ErrSecretsUnavailable) {
		return core.ErrSecretsUnavailable
	}
	return err
}

// spec builds what the deploy adapter needs for one release in one environment.
func (h *Hub) spec(ctx context.Context, a state.App, env string, rel core.Release) (core.EnvSpec, error) {
	m, err := h.parse(rel.Manifest, a.Repo)
	if err != nil {
		return core.EnvSpec{}, err
	}
	vars := m.VarsFor(env)
	// While the secrets service is down the deploy fails with its plain message. It never
	// goes ahead without the app's secrets.
	if err := h.ensureGenerated(m, a.ID, env); err != nil {
		return core.EnvSpec{}, noSecrets(err)
	}
	secrets, err := h.ad.Secrets.Inject(a.ID, env, slices.Concat(m.Secrets, generatedNames(m)))
	if err != nil {
		return core.EnvSpec{}, noSecrets(fmt.Errorf("read secrets: %w", err))
	}
	// The database's settings. Postgres's URL is a secret (it holds the password), so it
	// follows secrets_as; sqlite's are plain settings.
	switch m.Database {
	case core.DatabasePostgres:
		if secrets[core.DatabasePassword] == "" {
			return core.EnvSpec{}, errors.New("the database password is missing")
		}
		secrets[core.DatabaseURL] = fmt.Sprintf("postgres://%s:%s@%s:5432/%s", core.DatabaseUser, secrets[core.DatabasePassword], core.DatabaseService, core.DatabaseName)
	case core.DatabaseSQLite:
		file, _, _, _ := m.SQLiteFile()
		vars[core.DatabaseURL], vars[core.DatabasePath] = "sqlite:///"+file, file
	}
	if m.SecretsAs != manifest.SecretsAsFiles {
		for k, v := range secrets {
			vars[k] = v
		}
	}
	vars["HOMELAB_APP"], vars["HOMELAB_ENV"], vars["HOMELAB_COMMIT"] = a.ID, env, rel.Commit
	if rel.Version != "" {
		vars["HOMELAB_VERSION"] = rel.Version
	}
	// The app's own API key, and where to use it (platform services).
	token, err := h.appKey(ctx, a.ID, env, appKeyScopes(m))
	if err != nil {
		return core.EnvSpec{}, noSecrets(err)
	}
	vars[AppTokenSecret] = token
	if h.cfg.AppAPIURL != "" {
		vars["HOMELAB_API_URL"] = h.cfg.AppAPIURL
	}
	if h.cfg.Identity != nil {
		vars[identity.EnvPublicKey] = h.cfg.Identity.PublicKey()
	}
	data := m.Data
	if m.OwnsDatabaseFolder() {
		data = append(slices.Clone(data), manifest.Data{Name: core.DatabaseFolder, Path: core.DatabaseSQLiteDir})
	}
	mounts, err := h.mounts(a.ID, env, data, m.Folders)
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
	if m.Database == core.DatabasePostgres {
		// Postgres changes the owner of its folder itself, so Homelab only makes it.
		dir := filepath.Join(h.dataDir(a.ID, env), core.DatabaseFolder)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return core.EnvSpec{}, err
		}
		spec.Postgres = &core.Postgres{Port: h.dbPort(a.Slot, env), DataDir: dir}
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
	if err := h.ad.Deployer.Deploy(ctx, spec); err != nil {
		return err
	}
	h.forgetLogMask(a.ID, env) // the app may print its new secrets from its first second
	h.registerDatabase(a, env, rel)
	return nil
}

// registerDatabase tells homelab-services where the environment's database is. It runs at
// every deploy; a failure is only logged, because the app runs without it.
func (h *Hub) registerDatabase(a state.App, env string, rel core.Release) {
	m, err := h.parse(rel.Manifest, a.Repo)
	if err != nil || m.Database == "" {
		return
	}
	if err := h.register(a, env, m); err != nil {
		h.log.Warn("database: tell the services", "app", a.ID, "env", env, "err", err)
	}
}
