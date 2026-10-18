package hub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// autoState is what auto-promote is doing for one app, for people (AppView.AutoPromote).
type autoState struct {
	State  string     `json:"state"` // waiting | smoke-testing | held | blocked | done
	At     *time.Time `json:"at,omitempty"`
	Detail string     `json:"detail"`
}

// SetPromotion changes how an app moves between environments (C5). It's a Homelab
// setting: a later homelab.yaml doesn't change it back.
func (h *Hub) SetPromotion(ctx context.Context, appID, mode string, by core.Viewer) (AppView, error) {
	switch mode {
	case core.PromotionManual, core.PromotionAutoDev, core.PromotionAuto:
	default:
		return AppView{}, userErr("Promotion must be manual, auto-dev or auto.")
	}
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return AppView{}, userErr("There's no app %q.", appID)
	}
	if a.Promotion != mode {
		if err := h.store.SetPromotion(ctx, appID, mode); err != nil {
			return AppView{}, err
		}
		h.logEvent(ctx, state.Event{Actor: by.Label(), App: appID, Action: "app.promotion",
			Detail: fmt.Sprintf("Changed promotion from %s to %s", a.Promotion, mode)})
	}
	if mode != core.PromotionAuto {
		h.mu.Lock()
		delete(h.autoNote, appID)
		h.mu.Unlock()
	}
	return h.App(ctx, appID)
}

func (h *Hub) note(app string, st autoState) {
	h.mu.Lock()
	h.autoNote[app] = st
	h.mu.Unlock()
}

// AutoPromote promotes, once, every app on auto whose dev build has earned it (C6): healthy
// for promote_after, smoke test passed, prod secrets set, not held.
func (h *Hub) AutoPromote(ctx context.Context) {
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return
	}
	for _, a := range apps {
		if a.Promotion != core.PromotionAuto {
			continue
		}
		h.autoPromoteApp(ctx, a)
	}
}

func (h *Hub) autoPromoteApp(ctx context.Context, a state.App) {
	dev, err := h.store.Env(ctx, a.ID, core.Dev)
	if err != nil {
		return
	}
	prod, err := h.store.Env(ctx, a.ID, core.Prod)
	if err != nil {
		return
	}
	if dev.Want == nil {
		h.note(a.ID, autoState{State: "waiting", Detail: "Waiting for a build on dev."})
		return
	}
	rel := *dev.Want
	if prod.Want != nil && prod.Want.Digest == rel.Digest {
		h.note(a.ID, autoState{State: "done", Detail: fmt.Sprintf("Prod already runs %s.", rel.Short())})
		return
	}
	holds, err := h.store.Holds(ctx, a.ID)
	if err != nil {
		return
	}
	for _, hd := range holds {
		if hd.Commit == rel.Commit {
			h.note(a.ID, autoState{State: "held", Detail: fmt.Sprintf("%s won't be promoted by itself: %s", rel.Short(), hd.Reason)})
			return
		}
	}
	m, err := h.parse(rel.Manifest, a.Repo)
	if err != nil {
		h.note(a.ID, autoState{State: "blocked", Detail: err.Error()})
		return
	}
	missing, err := h.ad.Secrets.Missing(a.ID, core.Prod, m.Secrets)
	if err != nil {
		return
	}
	if len(missing) > 0 {
		h.note(a.ID, autoState{State: "blocked", Detail: fmt.Sprintf("Set %s for prod first.", strings.Join(missing, ", "))})
		return
	}

	h.mu.Lock()
	s := h.st(a.ID, core.Dev)
	health, since, busy := s.health, s.since, s.busy
	smoked := h.smoked[a.ID] == rel.Commit
	h.mu.Unlock()
	if busy != "" || health != core.Healthy || since.IsZero() {
		h.note(a.ID, autoState{State: "waiting", Detail: fmt.Sprintf("Waiting for %s to be healthy on dev.", rel.Short())})
		return
	}
	at := since.Add(m.PromoteAfter)
	if h.cfg.Now().Before(at) {
		h.note(a.ID, autoState{State: "waiting", At: &at,
			Detail: fmt.Sprintf("Dev must stay healthy for %s before %s goes to prod.", shortDuration(m.PromoteAfter), rel.Short())})
		return
	}

	if len(m.SmokeTest) > 0 && !smoked {
		if !h.smokeTest(ctx, a, rel, m.SmokeTest) {
			return
		}
	}
	err = h.promoteAs(ctx, a.ID, core.ActorHomelab, "Auto-promote")
	switch {
	case err == nil:
		h.note(a.ID, autoState{State: "done", Detail: fmt.Sprintf("Promoting %s.", rel.Short())})
	case err == ErrBusy:
		// someone else is working on the app; next round
	default:
		h.note(a.ID, autoState{State: "blocked", Detail: err.Error()})
	}
}

// smokeTest runs the app's smoke test against dev and records the outcome. It returns
// true when the test passed.
func (h *Hub) smokeTest(ctx context.Context, a state.App, rel core.Release, cmd []string) bool {
	if h.ad.Smoke == nil {
		h.note(a.ID, autoState{State: "blocked", Detail: "This Homelab can't run smoke tests."})
		return false
	}
	lock := h.appLock(a.ID)
	if !lock.TryLock() {
		return false
	}
	defer lock.Unlock()
	h.note(a.ID, autoState{State: "smoke-testing", Detail: fmt.Sprintf("Running the smoke test of %s against dev.", rel.Short())})
	auth, err := h.ad.Registry.PullAuth(ctx, rel.Image)
	if err != nil {
		h.note(a.ID, autoState{State: "blocked", Detail: "Couldn't sign in to the image registry: " + err.Error()})
		return false
	}
	m, _ := h.parse(rel.Manifest, a.Repo)
	passed, out, err := h.ad.Smoke.Smoke(ctx, core.SmokeTest{
		App: a.ID, Env: core.Dev, Release: rel, Command: cmd, Port: m.Port, Auth: auth, Timeout: h.cfg.SmokeTimeout,
	})
	if err != nil {
		h.note(a.ID, autoState{State: "blocked", Detail: "The smoke test couldn't run, will try again: " + err.Error()})
		return false
	}
	if !passed {
		reason := "Its smoke test failed: " + lastLine(out)
		_ = h.store.AddHold(ctx, a.ID, rel.Commit, reason)
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Dev, Action: "smoke.failed",
			Detail: fmt.Sprintf("The smoke test of %s failed, so it won't be promoted by itself.\n%s", rel.Short(), tail(out, 1500))})
		h.note(a.ID, autoState{State: "held", Detail: fmt.Sprintf("%s won't be promoted by itself: %s", rel.Short(), reason)})
		return false
	}
	h.mu.Lock()
	h.smoked[a.ID] = rel.Commit
	h.mu.Unlock()
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Dev, Action: "smoke.passed",
		Detail: fmt.Sprintf("The smoke test of %s passed", rel.Short())})
	return true
}

// ---- Watching prod after a promote (C7) ----

func (h *Hub) startWatch(app string, rel core.Release, prev *core.Release) {
	h.mu.Lock()
	h.watches[app] = &watch{until: h.cfg.Now().Add(h.cfg.WatchFor), rel: rel, prev: prev}
	h.mu.Unlock()
}

// watchStep looks at one health result of prod while it's being watched. Two unhealthy
// checks in a row put back the version prod ran before the promote.
func (h *Hub) watchStep(ctx context.Context, a state.App, want *core.Release, status string) {
	h.mu.Lock()
	w := h.watches[a.ID]
	if w == nil {
		h.mu.Unlock()
		return
	}
	if want == nil || want.Digest != w.rel.Digest || h.cfg.Now().After(w.until) {
		delete(h.watches, a.ID)
		h.mu.Unlock()
		return
	}
	if status == core.Healthy {
		w.fails = 0
		h.mu.Unlock()
		return
	}
	w.fails++
	if w.fails < 2 {
		h.mu.Unlock()
		return
	}
	delete(h.watches, a.ID)
	detail := h.st(a.ID, core.Prod).detail
	h.mu.Unlock()
	h.autoRollback(ctx, a, *w, detail)
}

// autoRollback puts back prod's previous version, keeping the data as it is: the new
// version ran for a while, and its writes since then are worth keeping. The copy from
// before the promote stays with the other data copies.
func (h *Hub) autoRollback(ctx context.Context, a state.App, w watch, detail string) {
	_ = h.store.AddHold(ctx, a.ID, w.rel.Commit, "Prod turned unhealthy after it was promoted.")
	if w.prev == nil {
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Prod, Action: "prod.unhealthy",
			Detail: fmt.Sprintf("%s turned unhealthy after it was promoted (%s). There's no earlier version to go back to.", w.rel.Short(), detail)})
		return
	}
	lock := h.appLock(a.ID)
	if !lock.TryLock() {
		return // a person is already acting on this app
	}
	prev := *w.prev
	if _, err := h.store.SetWant(ctx, state.Deployment{App: a.ID, Env: core.Prod, Release: prev, By: core.ActorHomelab, Kind: "rollback"}); err != nil {
		lock.Unlock()
		h.log.Error("auto rollback", "app", a.ID, "err", err)
		return
	}
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Prod, Action: "rollback.auto",
		Detail: fmt.Sprintf("%s turned unhealthy after it was promoted (%s), so prod is going back to %s. The data stays as it is; the copy from before the promote is kept.",
			w.rel.Short(), detail, prev.Short())})
	h.alertRollback(ctx, a, w.rel, prev)
	h.setBusy(a.ID, core.Prod, "Going back to "+prev.Short())
	go func() {
		defer h.Kick()
		defer lock.Unlock()
		defer h.setBusy(a.ID, core.Prod, "")
		h.applyAndWait(context.WithoutCancel(ctx), a, core.Prod, prev, "rollback", core.ActorHomelab)
	}()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if l == "" {
		return "no output"
	}
	if len(l) > 160 {
		l = l[:157] + "..."
	}
	return l
}

func tail(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}
