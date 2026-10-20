// Package fake has in-memory adapters for tests: every contract in package core, with no
// Docker or GitHub needed.
package fake

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Source is a fake Git host. Files maps "repo@ref:path" to contents; a commit SHA is a ref.
type Source struct {
	mu      sync.Mutex
	Files   map[string][]byte
	Commits map[string]core.Commit // by SHA
	Order   []string               // SHAs oldest first, for Compare
}

func NewSource() *Source {
	return &Source{Files: map[string][]byte{}, Commits: map[string]core.Commit{}}
}

// Push adds a commit carrying a manifest and makes it the head of branch.
func (s *Source) Push(repo, branch, sha, message string, manifest []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Files[repo+"@"+sha+":homelab.yaml"] = manifest
	s.Files[repo+"@"+branch+":homelab.yaml"] = manifest
	if _, seen := s.Commits[sha]; !seen {
		s.Order = append(s.Order, sha)
	}
	s.Commits[sha] = core.Commit{SHA: sha, Message: message, Author: "tester", When: time.Now()}
}

func (s *Source) ReadFile(_ context.Context, repo, ref, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.Files[repo+"@"+ref+":"+path]
	if !ok {
		return nil, core.ErrNotFound
	}
	return b, nil
}

func (s *Source) Commit(_ context.Context, _, sha string) (core.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Commits[sha]
	if !ok {
		return c, core.ErrNotFound
	}
	return c, nil
}

func (s *Source) Compare(_ context.Context, _, base, head string) ([]core.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Commit
	in := false
	for _, sha := range s.Order {
		if in {
			out = append(out, s.Commits[sha])
		}
		if sha == base {
			in = true
		}
		if sha == head {
			break
		}
	}
	return out, nil
}

func (s *Source) WebURL(repo, base, head string) string {
	if base != "" {
		return fmt.Sprintf("https://git.example/%s/compare/%s...%s", repo, base, head)
	}
	return fmt.Sprintf("https://git.example/%s/commit/%s", repo, head)
}

// Builder is a fake CI: Latest holds the newest build per repo.
type Builder struct {
	mu     sync.Mutex
	Latest map[string]core.Build
}

func NewBuilder() *Builder { return &Builder{Latest: map[string]core.Build{}} }

func (b *Builder) Set(repo string, build core.Build) {
	b.mu.Lock()
	b.Latest[repo] = build
	b.mu.Unlock()
}

func (b *Builder) LatestBuild(_ context.Context, repo, _, _ string) (core.Build, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	x, ok := b.Latest[repo]
	if !ok {
		return x, core.ErrNotFound
	}
	return x, nil
}

// Registry is a fake image registry: Images maps "image:tag" to a digest.
type Registry struct {
	mu     sync.Mutex
	Images map[string]string
}

func NewRegistry() *Registry { return &Registry{Images: map[string]string{}} }

func (r *Registry) Put(image, tag, digest string) {
	r.mu.Lock()
	r.Images[image+":"+tag] = digest
	r.mu.Unlock()
}

func (r *Registry) Resolve(_ context.Context, image, tag string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Images[image+":"+tag]
	if !ok {
		return "", core.ErrNotFound
	}
	return d, nil
}

func (r *Registry) PullAuth(context.Context, string) (core.RegistryAuth, error) {
	return core.RegistryAuth{}, nil
}

// Deployer is a fake container runtime. Running environments are kept in memory, and an
// optional Serve hook lets tests stand a real HTTP server up on the host port.
type Deployer struct {
	mu       sync.Mutex
	Envs     map[string]core.Running // "app/env"
	Specs    map[string]core.EnvSpec
	FailFor  map[string]error // digest -> error from Deploy
	Calls    []string
	LogTail  string // added to every Logs answer
	FilesErr error  // what CheckFiles returns
}

func NewDeployer() *Deployer {
	return &Deployer{Envs: map[string]core.Running{}, Specs: map[string]core.EnvSpec{}, FailFor: map[string]error{}}
}

func (d *Deployer) Prepare(_ context.Context, spec core.EnvSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls = append(d.Calls, "prepare "+spec.App+"/"+spec.Env+" "+spec.Release.Digest)
	return nil
}

func (d *Deployer) Deploy(_ context.Context, spec core.EnvSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls = append(d.Calls, "deploy "+spec.App+"/"+spec.Env+" "+spec.Release.Digest)
	if err := d.FailFor[spec.Release.Digest]; err != nil {
		return err
	}
	k := spec.App + "/" + spec.Env
	d.Specs[k] = spec
	d.Envs[k] = core.Running{App: spec.App, Env: spec.Env, Commit: spec.Release.Commit, Digest: spec.Release.Digest, State: core.StateRunning, StartedAt: time.Now()}
	return nil
}

func (d *Deployer) Stop(_ context.Context, app, env string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls = append(d.Calls, "stop "+app+"/"+env)
	k := app + "/" + env
	if r, ok := d.Envs[k]; ok {
		r.State = core.StateExited
		d.Envs[k] = r
	}
	return nil
}

// StopApp stops the app's container; the fake has no separate services, so the
// environment is stopped the same way as by Stop.
func (d *Deployer) StopApp(_ context.Context, app, env string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls = append(d.Calls, "stop-app "+app+"/"+env)
	k := app + "/" + env
	if r, ok := d.Envs[k]; ok {
		r.State = core.StateExited
		d.Envs[k] = r
	}
	return nil
}

// RemoveOldImages records the call as "remove-old-images <image> keep=<digests>".
func (d *Deployer) RemoveOldImages(_ context.Context, image string, keep []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	sorted := append([]string(nil), keep...)
	sort.Strings(sorted)
	d.Calls = append(d.Calls, "remove-old-images "+image+" keep="+strings.Join(sorted, ","))
	return nil
}

// Wipe forgets every container, as if the computer had been reset.
func (d *Deployer) Wipe() {
	d.mu.Lock()
	d.Envs = map[string]core.Running{}
	d.mu.Unlock()
}

// SetState changes what Observe reports for an environment, e.g. a container that stopped.
func (d *Deployer) SetState(app, env, state, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.Envs[app+"/"+env]
	r.State, r.Detail = state, detail
	d.Envs[app+"/"+env] = r
}

// SetServiceDown makes Observe report that a service next to the app stopped. The app's own
// state stays as it is.
func (d *Deployer) SetServiceDown(app, env, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.Envs[app+"/"+env]
	r.Incomplete, r.Detail = true, detail
	d.Envs[app+"/"+env] = r
}

func (d *Deployer) Observe(context.Context) ([]core.Running, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []core.Running
	for _, r := range d.Envs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].App+out[i].Env < out[j].App+out[j].Env })
	return out, nil
}

func (d *Deployer) Logs(_ context.Context, app, env string, _ int) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return "logs of " + app + "/" + env + "\n" + d.LogTail, nil
}

// Addresses records which names are served.
type Addresses struct {
	mu       sync.Mutex
	Handlers map[string]http.Handler
}

func NewAddresses() *Addresses { return &Addresses{Handlers: map[string]http.Handler{}} }

func (a *Addresses) Ensure(_ context.Context, name string, h http.Handler) error {
	a.mu.Lock()
	a.Handlers[name] = h
	a.mu.Unlock()
	return nil
}

func (a *Addresses) Remove(_ context.Context, name string) error {
	a.mu.Lock()
	delete(a.Handlers, name)
	a.mu.Unlock()
	return nil
}

func (a *Addresses) Observe() map[string]core.Address {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]core.Address{}
	for n := range a.Handlers {
		out[n] = core.Address{Name: n, URL: "https://" + n + ".example.test", Ready: true}
	}
	return out
}

func (a *Addresses) Close() error { return nil }

// Health answers health checks from a table; URLs not listed are healthy.
type Health struct {
	mu      sync.Mutex
	Failing map[string]bool // url -> failing
	// FailWhen, when set, also fails a URL whenever it returns true (e.g. only while a
	// given version runs).
	FailWhen func(url string) bool
}

func NewHealth() *Health { return &Health{Failing: map[string]bool{}} }

func (h *Health) SetFailing(url string, failing bool) {
	h.mu.Lock()
	h.Failing[url] = failing
	h.mu.Unlock()
}

func (h *Health) SetFailWhen(f func(url string) bool) {
	h.mu.Lock()
	h.FailWhen = f
	h.mu.Unlock()
}

func (h *Health) Check(_ context.Context, url string) core.HealthResult {
	h.mu.Lock()
	failing, when := h.Failing[url], h.FailWhen
	h.mu.Unlock()
	if failing || (when != nil && when(url)) {
		return core.HealthResult{OK: false, Code: 500, Detail: "HTTP 500"}
	}
	return core.HealthResult{OK: true, Code: 200, Latency: time.Millisecond}
}

// Secrets holds values per "app/env/NAME", and platform values by name.
type Secrets struct {
	mu             sync.Mutex
	Values         map[string]string
	PlatformValues map[string]string
}

func NewSecrets() *Secrets {
	return &Secrets{Values: map[string]string{}, PlatformValues: map[string]string{}}
}

func (s *Secrets) Set(app, env, name, value, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app == "" {
		s.PlatformValues[name] = value
		return nil
	}
	s.Values[app+"/"+env+"/"+name] = value
	return nil
}

func (s *Secrets) Delete(app, env, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app == "" {
		delete(s.PlatformValues, name)
		return nil
	}
	delete(s.Values, app+"/"+env+"/"+name)
	return nil
}

func (s *Secrets) List(app, env string) ([]core.SecretInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.SecretInfo
	if app == "" {
		for k := range s.PlatformValues {
			out = append(out, core.SecretInfo{Name: k})
		}
	} else {
		prefix := app + "/" + env + "/"
		for k := range s.Values {
			if strings.HasPrefix(k, prefix) {
				out = append(out, core.SecretInfo{Name: strings.TrimPrefix(k, prefix)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Secrets) DeleteApp(app string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.Values {
		if strings.HasPrefix(k, app+"/") {
			delete(s.Values, k)
		}
	}
	return nil
}

func (s *Secrets) Missing(app, env string, names []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, n := range names {
		if s.Values[app+"/"+env+"/"+n] == "" {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Secrets) Inject(app, env string, names []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, n := range names {
		if v := s.Values[app+"/"+env+"/"+n]; v != "" {
			out[n] = v
		}
	}
	return out, nil
}

func (s *Secrets) Platform(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.PlatformValues[name]
	return v, ok
}

func (s *Secrets) LoadPlatform(name string) (string, error) {
	v, _ := s.Platform(name)
	return v, nil
}

// Compile-time checks that the fakes satisfy the contracts.
var (
	_ core.Source        = (*Source)(nil)
	_ core.Builder       = (*Builder)(nil)
	_ core.Registry      = (*Registry)(nil)
	_ core.Deployer      = (*Deployer)(nil)
	_ core.Addresses     = (*Addresses)(nil)
	_ core.HealthChecker = (*Health)(nil)
	_ core.Secrets       = (*Secrets)(nil)
)

// Smoke records smoke tests and passes them unless Fail is set for the commit.
type Smoke struct {
	mu   sync.Mutex
	Runs []core.SmokeTest
	Fail map[string]bool // commit -> fails
}

func NewSmoke() *Smoke { return &Smoke{Fail: map[string]bool{}} }

func (s *Smoke) Smoke(_ context.Context, t core.SmokeTest) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Runs = append(s.Runs, t)
	if s.Fail[t.Release.Commit] {
		return false, "checking /health\nexpected 200, got 500", nil
	}
	return true, "ok", nil
}

// Mirror records which repos were synced.
type Mirror struct {
	mu     sync.Mutex
	Synced map[string]int
}

func NewMirror() *Mirror { return &Mirror{Synced: map[string]int{}} }

func (m *Mirror) Sync(_ context.Context, repo string) error {
	m.mu.Lock()
	m.Synced[repo]++
	m.mu.Unlock()
	return nil
}

func (m *Mirror) Status(repo string) core.MirrorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := core.MirrorStatus{Path: "/mirrors/" + repo + ".git"}
	if m.Synced[repo] > 0 {
		t := time.Unix(0, 0)
		st.LastSync = &t
	}
	return st
}

var (
	_ core.SmokeTester = (*Smoke)(nil)
	_ core.Mirror      = (*Mirror)(nil)
)
