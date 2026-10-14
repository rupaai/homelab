package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

const repo = "rupaai/movies"

const manifestDoc = `
id: movies
name: Movie Night
image: ghcr.io/rupaai/movies
tag: "sha-{commit}"
port: 8000
health: /healthz
data: [{name: db, path: /data}]
secrets: [TMDB_API_KEY]
`

type rig struct {
	t      *testing.T
	dir    string
	store  *state.Store
	hub    *Hub
	src    *fake.Source
	build  *fake.Builder
	reg    *fake.Registry
	dep    *fake.Deployer
	addr   *fake.Addresses
	health *fake.Health
	sec    *fake.Secrets
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, dir: t.TempDir(), src: fake.NewSource(), build: fake.NewBuilder(), reg: fake.NewRegistry(),
		dep: fake.NewDeployer(), addr: fake.NewAddresses(), health: fake.NewHealth(), sec: fake.NewSecrets()}
	r.open()
	return r
}

// open (re)creates the hub on the same state file, like restarting homelabd.
func (r *rig) open() {
	if r.store != nil {
		r.store.Close()
	}
	s, err := state.Open(filepath.Join(r.dir, "homelab.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	r.store = s
	r.hub = New(Config{DataDir: r.dir, HealthyWait: 3 * time.Second}, s, Adapters{
		Source: r.src, Builder: r.build, Registry: r.reg, Deployer: r.dep,
		Addresses: r.addr, Health: r.health, Secrets: r.sec,
	})
	r.t.Cleanup(func() { s.Close() })
}

// push simulates a commit whose build passed and whose image was pushed.
func (r *rig) push(sha string) {
	r.src.Push(repo, "main", sha, "change "+sha, []byte(manifestDoc))
	r.reg.Put("ghcr.io/rupaai/movies", "sha-"+sha, "sha256:"+sha)
	r.build.Set(repo, core.Build{Commit: sha, Status: core.BuildPassed, URL: "https://ci/" + sha})
}

// settle waits until no action is running on the app.
func (r *rig) settle() {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l := r.hub.appLock("movies")
		if l.TryLock() {
			l.Unlock()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("action didn't finish")
}

func (r *rig) env(name string) EnvView {
	r.t.Helper()
	v, err := r.hub.App(context.Background(), "movies")
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range v.Envs {
		if e.Env == name {
			return e
		}
	}
	r.t.Fatalf("no env %s", name)
	return EnvView{}
}

func (r *rig) running(env string) string {
	o, _ := r.dep.Observe(context.Background())
	for _, x := range o {
		if x.App == "movies" && x.Env == env && x.State == core.StateRunning {
			return x.Commit
		}
	}
	return ""
}

var owner = core.Viewer{Login: "labiba@example.com"}

func TestDeployLoop(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)

	// Add the app from its repo.
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, "https://github.com/"+repo+".git", "", owner); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err == nil {
		t.Fatal("adding the same app twice should fail")
	}

	// Done-when 1: a push shows up on dev.
	r.push("aaaaaaa1")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if got := r.running(core.Dev); got != "aaaaaaa1" {
		t.Fatalf("dev runs %q", got)
	}
	if e := r.env(core.Dev); e.Health != core.Healthy || e.Version.Short != "aaaaaaa" {
		t.Fatalf("dev view: %+v", e)
	}
	if _, ok := r.addr.Handlers["movies-dev"]; !ok {
		t.Fatal("movies-dev address not served")
	}
	// The same build isn't deployed twice.
	before := len(r.dep.Calls)
	r.hub.CheckBuilds(ctx)
	r.settle()
	if len(r.dep.Calls) != before {
		t.Fatalf("redeployed the same build: %v", r.dep.Calls[before:])
	}

	// S7: promote is blocked while a prod secret is missing.
	p, err := r.hub.PreviewPromote(ctx, "movies")
	if err != nil || len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "TMDB_API_KEY") {
		t.Fatalf("preview with missing secret: %+v %v", p, err)
	}
	if err := r.hub.Promote(ctx, "movies", owner); err == nil {
		t.Fatal("promote should be refused")
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k")

	// Done-when 2: promote runs the same build on prod.
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q", got)
	}
	if spec := r.dep.Specs["movies/prod"]; spec.Vars["TMDB_API_KEY"] != "k" || spec.Release.Digest != "sha256:aaaaaaa1" {
		t.Fatalf("prod spec: %+v", spec)
	}
	if spec := r.dep.Specs["movies/dev"]; spec.Vars["TMDB_API_KEY"] != "" {
		t.Fatal("dev got prod's secret")
	}

	// Write some prod data, then push and promote a second version: the data is copied first.
	prodData := filepath.Join(r.dir, "apps", "movies", "prod", "data", "db")
	if err := os.WriteFile(filepath.Join(prodData, "catalogue.db"), []byte("v1 data"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.push("bbbbbbb2")
	r.hub.CheckBuilds(ctx)
	r.settle()
	p, _ = r.hub.PreviewPromote(ctx, "movies")
	if len(p.Blockers) != 0 || p.From.Commit != "aaaaaaa1" || p.To.Commit != "bbbbbbb2" || len(p.Commits) != 1 || !p.DataCopy {
		t.Fatalf("second preview: %+v", p)
	}
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "bbbbbbb2" {
		t.Fatalf("prod runs %q after second promote", got)
	}
	hist, _ := r.store.History(ctx, "movies", core.Prod, 1)
	if hist[0].BackupPath == "" {
		t.Fatal("no data copy recorded")
	}
	if b, err := os.ReadFile(filepath.Join(hist[0].BackupPath, "db", "catalogue.db")); err != nil || string(b) != "v1 data" {
		t.Fatalf("data copy: %q %v", b, err)
	}

	// Done-when 3: rollback brings back the previous prod version.
	rp, _ := r.hub.PreviewRollback(ctx, "movies", core.Prod)
	if rp.To == nil || rp.To.Commit != "aaaaaaa1" {
		t.Fatalf("rollback preview: %+v", rp)
	}
	if err := r.hub.Rollback(ctx, "movies", core.Prod, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q after rollback", got)
	}
	if e := r.env(core.Prod); e.DeployKind != "rollback" || e.DeployedBy != owner.Login {
		t.Fatalf("prod view after rollback: %+v", e)
	}

	// Done-when 5: after a restart with every container gone, the reconciler brings it back.
	r.dep.Wipe()
	r.open()
	r.hub.Reconcile(ctx)
	if r.running(core.Dev) != "bbbbbbb2" || r.running(core.Prod) != "aaaaaaa1" {
		t.Fatalf("after restart: dev=%q prod=%q", r.running(core.Dev), r.running(core.Prod))
	}

	// Drift: someone stops prod by hand; the reconciler starts it again.
	_ = r.dep.Stop(ctx, "movies", core.Prod)
	r.hub.Reconcile(ctx)
	if r.running(core.Prod) != "aaaaaaa1" {
		t.Fatal("stopped prod wasn't restarted")
	}

	// Done-when 4: who did what is in the activity log.
	ev, _ := r.hub.Events(ctx, "movies", 0, 100)
	var actions []string
	for _, e := range ev {
		actions = append(actions, e.Action)
	}
	all := strings.Join(actions, " ")
	for _, want := range []string{"app.added", "promote.done", "rollback.done", "data.copied", "deploy.done"} {
		if !strings.Contains(all, want) {
			t.Errorf("activity log has no %s: %s", want, all)
		}
	}
}

func TestPromoteNeedsHealthyDev(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k")
	r.health.SetFailing("http://127.0.0.1:17010/healthz", true) // slot 1, dev
	r.push("aaaaaaa1")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if e := r.env(core.Dev); e.Health != core.Degraded {
		t.Fatalf("dev should be degraded: %+v", e)
	}
	if err := r.hub.Promote(ctx, "movies", owner); err == nil || !strings.Contains(err.Error(), "healthy") {
		t.Fatalf("promote of an unhealthy dev: %v", err)
	}
}

func TestFailedDeployRetries(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.dep.FailFor["sha256:aaaaaaa1"] = os.ErrPermission
	r.push("aaaaaaa1")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if e := r.env(core.Dev); e.LastError == "" {
		t.Fatalf("failure not shown: %+v", e)
	}
	delete(r.dep.FailFor, "sha256:aaaaaaa1")
	r.hub.st("movies", core.Dev).nextAttempt = time.Time{} // skip the one-minute wait
	r.hub.Reconcile(ctx)
	if r.running(core.Dev) != "aaaaaaa1" {
		t.Fatal("reconciler didn't retry")
	}
	if e := r.env(core.Dev); e.LastError != "" {
		t.Fatalf("error not cleared: %+v", e)
	}
}

// A promote whose new version doesn't become healthy, or doesn't start, puts prod back on
// the version it ran before, with the data from just before the promote.
func TestFailedPromoteGoesBack(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k")
	r.push("aaaaaaa1")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	prodData := filepath.Join(r.dir, "apps", "movies", "prod", "data", "db", "catalogue.db")
	if err := os.WriteFile(prodData, []byte("v1 data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// bbbbbbb2 starts but never answers its health check on prod.
	const prodHealth = "http://127.0.0.1:17011/healthz" // slot 1, prod
	r.health.SetFailWhen(func(url string) bool {
		if url != prodHealth || r.running(core.Prod) != "bbbbbbb2" {
			return false
		}
		_ = os.WriteFile(prodData, []byte("changed by bbbbbbb2"), 0o644) // a bad version may touch data
		return true
	})
	r.push("bbbbbbb2")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q after a failed promote", got)
	}
	if e := r.env(core.Prod); e.Version.Commit != "aaaaaaa1" || e.DeployKind != "rollback" {
		t.Fatalf("prod view after failed promote: %+v", e.Version)
	}
	if b, _ := os.ReadFile(prodData); string(b) != "v1 data" {
		t.Fatalf("prod data after going back: %q", b)
	}
	evs, _ := r.store.Events(ctx, "movies", 0, 50)
	var actions []string
	for _, e := range evs {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"promote.reverting", "data.restored", "promote.failed"} {
		if !slices.Contains(actions, want) {
			t.Fatalf("no %s event in %v", want, actions)
		}
	}

	// ccccccc3 doesn't start at all.
	r.health.SetFailWhen(nil)
	r.push("ccccccc3")
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.dep.FailFor["sha256:ccccccc3"] = os.ErrPermission // it ran on dev; on prod it won't start
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q after a promote that didn't start", got)
	}
	if e := r.env(core.Prod); e.Version.Commit != "aaaaaaa1" {
		t.Fatalf("prod wants %s after a promote that didn't start", e.Version.Commit)
	}
}
