// Health checks for running apps.

package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

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
		running, err := h.ad.Deployer.ObserveApp(ctx, a.ID)
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
