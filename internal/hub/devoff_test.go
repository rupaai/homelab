package hub

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// bothEnvs puts bbbbbbb2 on prod and ccccccc3 on dev, and returns the rig.
func bothEnvs(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.push("ccccccc3")
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	if r.running(core.Dev) != "ccccccc3" || r.running(core.Prod) != "bbbbbbb2" {
		t.Fatalf("dev %q prod %q", r.running(core.Dev), r.running(core.Prod))
	}
	return r
}

func TestDevOffStopsDevAndNeverRestartsIt(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	r.dep.Calls = nil
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	if r.running(core.Dev) != "" {
		t.Fatal("dev still runs")
	}
	if !slices.Contains(r.dep.Calls, "stop movies/dev") || slices.Contains(r.dep.Calls, "stop movies/prod") {
		t.Fatalf("calls: %v", r.dep.Calls)
	}
	if r.running(core.Prod) != "bbbbbbb2" {
		t.Fatal("prod was touched")
	}
	if _, ok := r.addr.Observe()["movies-dev"]; ok {
		t.Error("dev still has an address")
	}
	if _, ok := r.addr.Observe()["movies"]; !ok {
		t.Error("prod lost its address")
	}
	// The wanted release and the database are kept.
	if e, _ := r.store.Env(ctx, "movies", core.Dev); e.Want == nil || e.Want.Commit != "ccccccc3" || !e.Off {
		t.Fatalf("dev env: %+v", e)
	}
	// Many rounds later it is still stopped, and nothing starts it or stops it again.
	r.dep.Calls = nil
	for range 3 {
		r.advance(2 * time.Minute)
		r.hub.Reconcile(ctx)
		r.hub.CheckHealth(ctx)
	}
	if r.running(core.Dev) != "" || len(r.dep.Calls) != 0 {
		t.Fatalf("calls while off: %v", r.dep.Calls)
	}
	// The view says so, and health is not down.
	if ev := r.env(core.Dev); !ev.Off || ev.State != "off" || ev.Health != core.Unknown || ev.HealthDetail != "Turned off" || ev.Version == nil {
		t.Fatalf("dev view: %+v", ev)
	}
	if ev := r.env(core.Prod); ev.Off || ev.Health != core.Healthy {
		t.Fatalf("prod view: %+v", ev)
	}
	if len(r.events("health.down")) != 0 || len(r.events("health.degraded")) != 0 {
		t.Fatal("health events while off")
	}
	if got := r.events("env.turned_off"); len(got) != 1 || !strings.HasPrefix(got[0], "labiba@example.com") {
		t.Fatalf("events: %v", got)
	}
	// Turning it off again changes nothing and logs nothing.
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil || len(r.events("env.turned_off")) != 1 {
		t.Fatalf("second off: %v", err)
	}
}

func TestDevOffStopsDevThatWasLeftIncomplete(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	r.dep.SetServiceDown("movies", core.Dev, "jellyfin exited")
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.dep.Calls = nil
	r.hub.Reconcile(ctx)
	if !slices.Contains(r.dep.Calls, "stop movies/dev") || slices.Contains(r.dep.Calls, "deploy movies/dev sha256:ccccccc3") {
		t.Fatalf("calls: %v", r.dep.Calls)
	}
}

func TestNewBuildWhileDevIsOffWaitsAndStartsOnTurnOn(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	r.dep.Calls = nil
	r.push("ddddddd4")
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.Reconcile(ctx)
	if e, _ := r.store.Env(ctx, "movies", core.Dev); e.Want == nil || e.Want.Commit != "ddddddd4" || e.LastBuild != "ddddddd4" {
		t.Fatalf("dev env: %+v", e)
	}
	if slices.ContainsFunc(r.dep.Calls, func(c string) bool { return strings.HasPrefix(c, "deploy ") }) || r.running(core.Dev) != "" {
		t.Fatalf("deployed while off: %v", r.dep.Calls)
	}
	if len(r.events("deploy.waiting")) != 1 {
		t.Fatalf("events: %v", r.events("deploy.waiting"))
	}
	// A hand deploy waits too.
	if err := r.hub.DeployDev(ctx, "movies", "", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.running(core.Dev) != "" {
		t.Fatal("hand deploy started dev while off")
	}
	// Turning it on starts the newest release.
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, false, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	if r.running(core.Dev) != "ddddddd4" {
		t.Fatalf("dev runs %q", r.running(core.Dev))
	}
	if r.running(core.Prod) != "bbbbbbb2" {
		t.Fatal("prod changed")
	}
	r.hub.CheckHealth(ctx)
	if ev := r.env(core.Dev); ev.Off || ev.State != core.StateRunning || ev.Health != core.Healthy {
		t.Fatalf("dev view: %+v", ev)
	}
	if len(r.events("env.turned_on")) != 1 {
		t.Fatalf("events: %v", r.events("env.turned_on"))
	}
	if _, ok := r.addr.Observe()["movies-dev"]; !ok {
		t.Error("dev has no address after turning on")
	}
}

func TestPromoteWhileDevIsOffWarnsButIsAllowed(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	// Healthy dev: no warning.
	if p, _ := r.hub.PreviewPromote(ctx, "movies"); p.DevOff || len(p.Warnings) != 0 || len(p.Blockers) != 0 {
		t.Fatalf("preview with dev on: %+v", p)
	}
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	p, err := r.hub.PreviewPromote(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	if !p.DevOff || len(p.Blockers) != 0 || len(p.Warnings) != 1 || p.Warnings[0] != "Dev is off, so this build wasn't tested in dev." {
		t.Fatalf("preview: %+v", p)
	}
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.running(core.Prod) != "ccccccc3" || r.running(core.Dev) != "" {
		t.Fatalf("prod %q dev %q", r.running(core.Prod), r.running(core.Dev))
	}
}

func TestPromoteWhileDevIsOffStillNeedsADevBuild(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "bbbbbbb2", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	p, _ := r.hub.PreviewPromote(ctx, "movies")
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "Nothing is deployed to dev") {
		t.Fatalf("preview: %+v", p)
	}
	if err := r.hub.Promote(ctx, "movies", owner); err == nil {
		t.Fatal("promoted with nothing on dev")
	}
}

func TestAutoPromoteWaitsWhileDevIsOff(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.advance(time.Second)
	r.src.Push(repo, "main", "bbbbbbb2", "first", []byte(autoManifest))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.pushWith("bbbbbbb2", autoManifest)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	r.advance(2 * time.Hour)
	r.hub.AutoPromote(ctx)
	r.settle()
	if a := r.auto(); a == nil || a.State != "waiting" || !strings.Contains(a.Detail, "Dev is off") {
		t.Fatalf("auto = %+v", a)
	}
	if r.running(core.Prod) != "" || len(r.smoke.Runs) != 0 {
		t.Fatal("auto-promoted while dev was off")
	}
	// Once dev is on it has to earn the promote again.
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, false, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	r.hub.CheckHealth(ctx)
	r.hub.AutoPromote(ctx)
	if a := r.auto(); a.State != "waiting" || a.At == nil {
		t.Fatalf("auto after turning on = %+v", a)
	}
	if r.running(core.Prod) != "" {
		t.Fatal("promoted right after turning dev on")
	}
}

func TestDevOffRefusesActionsThatNeedDevRunning(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	const msg = "Dev is off. Turn it on first."
	r.dep.Calls = nil
	if err := r.hub.RestartEnv(ctx, "movies", core.Dev, owner); err == nil || err.Error() != msg || !isUserErr(err) {
		t.Errorf("restart: %v", err)
	}
	if err := r.hub.Rollback(ctx, "movies", core.Dev, owner); err == nil || err.Error() != msg {
		t.Errorf("rollback: %v", err)
	}
	if _, err := r.hub.PreviewRollback(ctx, "movies", core.Dev); err == nil || err.Error() != msg {
		t.Errorf("rollback preview: %v", err)
	}
	if len(r.dep.Calls) != 0 {
		t.Errorf("deployer called: %v", r.dep.Calls)
	}
	// Prod can still be restarted.
	if err := r.hub.RestartEnv(ctx, "movies", core.Prod, owner); err != nil {
		t.Errorf("prod restart: %v", err)
	}
	r.settle()
	// Logs and secrets keep working.
	if _, err := r.hub.Logs(ctx, "movies", core.Dev, 10); err != nil {
		t.Errorf("logs: %v", err)
	}
}

func TestDevOffRefusesDevDatabaseActions(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.dbApp(postgresDoc)
	r.promoteDB()
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	const msg = "Dev is off. Turn it on first."
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Dev, owner); err == nil || err.Error() != msg {
		t.Errorf("copy: %v", err)
	}
	if err := r.hub.CopyProdToDev(ctx, "movies", owner); err == nil || err.Error() != msg {
		t.Errorf("prod to dev: %v", err)
	}
	if _, err := r.hub.ImportDB(ctx, "movies", core.Dev, strings.NewReader("SELECT 1;"), owner); err == nil || err.Error() != msg {
		t.Errorf("import: %v", err)
	}
	if err := r.hub.RestoreDBCopy(ctx, "movies", core.Dev, "x", owner); err == nil || err.Error() != msg {
		t.Errorf("restore: %v", err)
	}
	// Prod's database actions still work.
	if _, err := r.hub.MakeDBCopy(ctx, "movies", core.Prod, owner); err != nil {
		t.Errorf("prod copy: %v", err)
	}
}

func TestOnlyDevCanBeTurnedOff(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	if err := r.hub.SetEnvOff(ctx, "movies", core.Prod, true, owner); err == nil || !isUserErr(err) {
		t.Errorf("prod off: %v", err)
	}
	if err := r.hub.SetEnvOff(ctx, "movies", "staging", true, owner); !isUserErr(err) {
		t.Errorf("odd env: %v", err)
	}
	if err := r.hub.SetEnvOff(ctx, "nope", core.Dev, true, owner); !isUserErr(err) {
		t.Errorf("unknown app: %v", err)
	}
	if e, _ := r.store.Env(ctx, "movies", core.Prod); e.Off {
		t.Fatal("prod is off")
	}
	// Busy apps are refused and left alone.
	r.hub.setBusy("movies", core.Dev, "Deploying x")
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != ErrBusy {
		t.Errorf("busy: %v", err)
	}
	r.hub.setBusy("movies", core.Dev, "")
	lock := r.hub.appLock("movies")
	lock.Lock()
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != ErrBusy {
		t.Errorf("locked: %v", err)
	}
	lock.Unlock()
	if e, _ := r.store.Env(ctx, "movies", core.Dev); e.Off {
		t.Fatal("dev turned off while busy")
	}
}

func TestDevOffSurvivesARestartOfHomelab(t *testing.T) {
	ctx := context.Background()
	r := bothEnvs(t)
	if err := r.hub.SetEnvOff(ctx, "movies", core.Dev, true, owner); err != nil {
		t.Fatal(err)
	}
	r.hub.Reconcile(ctx)
	r.open()
	r.dep.Calls = nil
	r.hub.Reconcile(ctx)
	if r.running(core.Dev) != "" || slices.ContainsFunc(r.dep.Calls, func(c string) bool { return strings.HasPrefix(c, "deploy ") }) {
		t.Fatalf("dev started after a restart: %v", r.dep.Calls)
	}
}
