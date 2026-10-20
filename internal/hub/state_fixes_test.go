package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
)

func (r *rig) countCalls(call string) int {
	n := 0
	for _, c := range r.dep.Calls {
		if c == call {
			n++
		}
	}
	return n
}

// A stopped service next to the app is not the app being down: no rollback, no alert, and
// the reconciler starts the environment again.
func TestStoppedServiceDoesNotMarkAppDown(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.dep.SetServiceDown("movies", core.Prod, "prowlarr exited with code 1")
	r.hub.CheckHealth(ctx)
	r.hub.CheckHealth(ctx)
	r.settle()
	if got := r.running(core.Prod); got != "bbbbbbb2" {
		t.Errorf("prod runs %q, want bbbbbbb2; events: %v", got, r.events("rollback.auto"))
	}
	e := r.env(core.Prod)
	if e.Health != core.Healthy || !strings.Contains(e.HealthDetail, "prowlarr exited") {
		t.Errorf("prod health = %s %q, want healthy with the service as a detail", e.Health, e.HealthDetail)
	}
	before := r.countCalls("deploy movies/prod sha256:bbbbbbb2")
	r.hub.Reconcile(ctx)
	r.settle()
	if got := r.countCalls("deploy movies/prod sha256:bbbbbbb2"); got != before+1 {
		t.Errorf("reconciler should start the environment again once (deploys %d -> %d)", before, got)
	}
}

// A removed app's containers are stopped once, not on every reconcile.
func TestRemovedAppStoppedOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	if err := r.hub.RemoveApp(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	stops := r.countCalls("stop movies/prod")
	for i := 0; i < 5; i++ {
		r.hub.Reconcile(ctx)
	}
	if got := r.countCalls("stop movies/prod"); got != stops {
		t.Errorf("stop calls grew from %d to %d", stops, got)
	}
	if n := len(r.events("env.stopped")); n != 0 {
		t.Errorf("%d env.stopped events", n)
	}
}

// Containers of an app that is no longer registered, and still running, are stopped once.
func TestUnregisteredRunningEnvStopped(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	if err := r.store.RemoveApp(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r.hub.Reconcile(ctx)
	}
	if n := len(r.events("env.stopped")); n != 2 {
		t.Errorf("%d env.stopped events, want 2 (dev and prod once)", n)
	}
}

// After a failed promote went back, and after two rollbacks, Rollback goes to the last good
// version and never to a version that failed or toggles between two.
func TestRollbackSkipsFailedAndKeepsGoingBack(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	promote := func(sha string) {
		t.Helper()
		r.push(sha)
		r.hub.CheckBuilds(ctx)
		r.settle()
		r.hub.CheckHealth(ctx)
		if err := r.hub.Promote(ctx, "movies", owner); err != nil {
			t.Fatal(err)
		}
		r.settle()
	}
	promote("aaaaaaa1")
	promote("bbbbbbb2")
	promote("ccccccc3")
	r.health.SetFailWhen(func(url string) bool {
		return url == "http://127.0.0.1:17011/healthz" && r.running(core.Prod) == "ddddddd4"
	})
	promote("ddddddd4") // fails and goes back to ccccccc3
	if got := r.running(core.Prod); got != "ccccccc3" {
		t.Fatalf("prod runs %q", got)
	}
	back := func(want string) {
		t.Helper()
		p, err := r.hub.PreviewRollback(ctx, "movies", core.Prod)
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if p.To != nil {
				t.Fatalf("rollback offers %s, want nothing", p.To.Commit)
			}
			return
		}
		if p.To == nil || p.To.Commit != want {
			t.Fatalf("rollback offers %+v, want %s", p.To, want)
		}
		if err := r.hub.Rollback(ctx, "movies", core.Prod, owner); err != nil {
			t.Fatal(err)
		}
		r.settle()
		if got := r.running(core.Prod); got != want {
			t.Fatalf("prod runs %q, want %s", got, want)
		}
	}
	back("bbbbbbb2")
	back("aaaaaaa1")
	back("")
}

// An update or test version is refused while an environment is busy.
func TestUpdateRefusedWhileBusy(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	up := &fake.Updater{State: core.UpdateStatus{Available: true, Latest: "v2"}, TestList: []core.TestVersion{{Version: "t1"}}}
	r.hub.ad.Updater = up
	r.hub.setBusy("movies", core.Prod, "Promoting abc")
	if _, err := r.hub.UpdateHomelab(ctx, owner); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("update while busy: %v", err)
	}
	if _, err := r.hub.TryTestVersion(ctx, owner, "t1"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("test version while busy: %v", err)
	}
	if up.Applied != 0 || up.State.Testing != nil {
		t.Error("the updater was called")
	}
	r.hub.setBusy("movies", core.Prod, "")
	if _, err := r.hub.UpdateHomelab(ctx, owner); err != nil {
		t.Errorf("update when idle: %v", err)
	}
}

// WaitIdle returns when the running action finishes, or when its time runs out.
func TestWaitIdle(t *testing.T) {
	r := newRig(t)
	lock := r.hub.appLock("movies")
	lock.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r.hub.WaitIdle(ctx)
	if time.Since(start) < 40*time.Millisecond {
		t.Error("WaitIdle returned while an action was running")
	}
	lock.Unlock()
	done := make(chan struct{})
	go func() { r.hub.WaitIdle(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("WaitIdle didn't return when idle")
	}
}

// Health isn't checked while an action works on the app: it would see a deploy half done.
func TestCheckHealthSkipsBusyApp(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.hub.CheckHealth(ctx)
	r.dep.SetState("movies", core.Prod, core.StateExited, "exited")
	lock := r.hub.appLock("movies")
	lock.Lock()
	r.hub.CheckHealth(ctx)
	lock.Unlock()
	if e := r.env(core.Prod); e.Health != core.Healthy {
		t.Errorf("health = %s, want it unchanged while busy", e.Health)
	}
}

// A service that stays down is restarted at most once a minute, and the log says which.
func TestStoppedServiceRestartBackoff(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	down := func() { r.dep.SetServiceDown("movies", core.Prod, "prowlarr exited with code 1") }
	deploys := func() int { return r.countCalls("deploy movies/prod sha256:bbbbbbb2") }
	before := deploys()
	for i := 0; i < 4; i++ {
		down() // the deploy "succeeds", but the service stops again
		r.hub.Reconcile(ctx)
		r.settle()
		r.advance(10 * time.Second)
	}
	if got := deploys() - before; got != 1 {
		t.Errorf("%d restarts within a minute, want 1", got)
	}
	r.advance(time.Minute)
	down()
	r.hub.Reconcile(ctx)
	if got := deploys() - before; got != 2 {
		t.Errorf("%d restarts after a minute, want 2", got)
	}
	if ev := r.events("deploy.started"); len(ev) == 0 || !strings.Contains(ev[0], "because prowlarr exited with code 1") {
		t.Errorf("deploy.started events: %v", ev)
	}
}
