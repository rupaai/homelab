package hub

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
)

// deployedDev puts one build on dev and returns the rig.
func deployedDev(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.push("aaaaaaa1")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if r.running(core.Dev) != "aaaaaaa1" {
		t.Fatal("dev isn't running")
	}
	return r
}

func TestRestartEnvRecreatesTheSameRelease(t *testing.T) {
	ctx := context.Background()
	r := deployedDev(t)
	before, _ := r.store.Env(ctx, "movies", core.Dev)
	r.dep.Calls = nil
	if err := r.hub.RestartEnv(ctx, "movies", core.Dev, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if !slices.Contains(r.dep.Calls, "recreate movies/dev") || !slices.Contains(r.dep.Calls, "deploy movies/dev sha256:aaaaaaa1") {
		t.Fatalf("calls: %v", r.dep.Calls)
	}
	if !r.dep.Specs["movies/dev"].Recreate {
		t.Error("the spec didn't ask for fresh containers")
	}
	if r.running(core.Dev) != "aaaaaaa1" {
		t.Error("dev runs another version")
	}
	// The wanted release is not changed, and a normal deploy doesn't recreate.
	after, _ := r.store.Env(ctx, "movies", core.Dev)
	if after.Want == nil || after.Want.Digest != before.Want.Digest {
		t.Errorf("wanted release changed: %+v", after.Want)
	}
	if err := r.hub.deploy(ctx, mustApp(t, r), core.Dev, *after.Want); err != nil {
		t.Fatal(err)
	}
	if r.dep.Specs["movies/dev"].Recreate {
		t.Error("a normal deploy recreated")
	}
	// The activity says what happened.
	ev, _ := r.hub.Events(ctx, "movies", 0, 50)
	var started, done bool
	for _, e := range ev {
		started = started || e.Action == "restart.started"
		done = done || e.Action == "restart.done"
	}
	if !started || !done {
		t.Errorf("events: %+v", ev)
	}
}

func TestRestartEnvRefusals(t *testing.T) {
	ctx := context.Background()
	r := deployedDev(t)
	// Prod runs nothing.
	if err := r.hub.RestartEnv(ctx, "movies", core.Prod, owner); err == nil || !strings.Contains(err.Error(), "Nothing is running") {
		t.Errorf("prod restart: %v", err)
	}
	if err := r.hub.RestartEnv(ctx, "movies", "staging", owner); err == nil {
		t.Error("odd environment accepted")
	}
	if err := r.hub.RestartEnv(ctx, "nope", core.Dev, owner); err == nil {
		t.Error("unknown app accepted")
	}
	// Busy: another action holds the app.
	lock := r.hub.appLock("movies")
	lock.Lock()
	if err := r.hub.RestartEnv(ctx, "movies", core.Dev, owner); err != ErrBusy {
		t.Errorf("locked app: %v", err)
	}
	lock.Unlock()
	r.hub.setBusy("movies", core.Dev, "Deploying x")
	r.dep.Calls = nil
	if err := r.hub.RestartEnv(ctx, "movies", core.Dev, owner); err != ErrBusy {
		t.Errorf("busy env: %v", err)
	}
	if len(r.dep.Calls) != 0 {
		t.Errorf("deployer called while busy: %v", r.dep.Calls)
	}
	// The failed tries left the app unlocked.
	if !lock.TryLock() {
		t.Fatal("app left locked")
	}
	lock.Unlock()
}

func TestRestartHomelabRefusals(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := r.hub.RestartHomelab(ctx, owner); err == nil {
		t.Error("restart without an updater")
	}
	up := &fake.Updater{}
	r.hub.ad.Updater = up
	if err := r.hub.RestartHomelab(ctx, owner); err == nil || !strings.Contains(err.Error(), "can't restart") {
		t.Errorf("not under systemd: %v", err)
	}
	up.Restartable = true
	r.hub.setBusy("movies", core.Prod, "Promoting abc")
	if err := r.hub.RestartHomelab(ctx, owner); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("while busy: %v", err)
	}
	if up.Restarted != 0 {
		t.Fatal("restarted while busy")
	}
	r.hub.setBusy("movies", core.Prod, "")
	if err := r.hub.RestartHomelab(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if up.Restarted != 1 {
		t.Errorf("restarted %d times", up.Restarted)
	}
	ev, _ := r.hub.Events(ctx, "", 0, 10)
	if len(ev) == 0 || ev[0].Action != "homelab.restart" {
		t.Errorf("events: %+v", ev)
	}
	v, _ := r.hub.Device(ctx, false)
	if !v.CanRestart {
		t.Error("device view hides the restart")
	}
}
