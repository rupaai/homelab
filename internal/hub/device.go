package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// DeviceView is the device card: the device itself and Homelab's own version (milestone 6).
type DeviceView struct {
	System   *core.SystemInfo   `json:"system,omitempty"`
	Update   *core.UpdateStatus `json:"update,omitempty"`
	Problems []string           `json:"problems,omitempty"`
}

// Device describes the device and whether a newer Homelab is out. A part that can't be read
// is left out and explained.
func (h *Hub) Device(ctx context.Context) (DeviceView, error) {
	var v DeviceView
	if h.ad.System != nil {
		if s, err := h.ad.System.Info(ctx); err == nil {
			v.System = &s
		} else {
			v.Problems = append(v.Problems, "Couldn't read the device: "+err.Error())
		}
	}
	if h.ad.Updater != nil {
		if u, err := h.ad.Updater.Status(ctx); err == nil {
			v.Update = &u
		} else {
			v.Problems = append(v.Problems, "Couldn't check Homelab's version: "+err.Error())
		}
	}
	if h.ad.Services != nil {
		if p := h.ad.Services.Problem(); p != "" {
			v.Problems = append(v.Problems, p)
		}
	}
	return v, nil
}

// refuseIfBusy stops an update or test version from restarting Homelab in the middle of an
// action, such as a promote or a rollback.
func (h *Hub) refuseIfBusy() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.status {
		if s.busy != "" {
			return userErr("Homelab is busy (%s). Try again when it's finished.", s.busy)
		}
	}
	return nil
}

// WaitIdle waits until no action is working on any app, or until ctx ends. Homelab calls it
// before it exits, so a promote or rollback isn't cut off halfway.
func (h *Hub) WaitIdle(ctx context.Context) {
	for {
		h.appLocksMu.Lock()
		locks := make([]*sync.Mutex, 0, len(h.appLocks))
		for _, l := range h.appLocks {
			locks = append(locks, l)
		}
		h.appLocksMu.Unlock()
		idle := true
		for _, l := range locks {
			if l.TryLock() {
				l.Unlock()
			} else {
				idle = false
			}
		}
		if idle {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// UpdateHomelab installs the newest Homelab and restarts on it.
func (h *Hub) UpdateHomelab(ctx context.Context, by core.Viewer) (string, error) {
	if h.ad.Updater == nil {
		return "", userErr("This Homelab doesn't update itself.")
	}
	if err := h.refuseIfBusy(); err != nil {
		return "", err
	}
	version, err := h.ad.Updater.Apply(ctx)
	if err != nil {
		return "", userErr("Homelab wasn't updated: %s.", err)
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "homelab.update", Detail: "Started updating Homelab to " + version})
	return version, nil
}

// RecordUpdate notes in the activity how an update went (said by the program that ran it).
func (h *Hub) RecordUpdate(ctx context.Context, detail string) {
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, Action: "homelab.updated", Detail: detail})
}

// TestVersions lists the builds of open Homelab pull requests that can be tried here.
func (h *Hub) TestVersions(ctx context.Context) ([]core.TestVersion, error) {
	if h.ad.Updater == nil {
		return nil, userErr("This Homelab doesn't update itself.")
	}
	tests, err := h.ad.Updater.Tests(ctx)
	if err != nil {
		return nil, userErr("Couldn't list the test versions: %s.", err)
	}
	return tests, nil
}

// TryTestVersion saves Homelab's data and restarts on a pull request's build.
func (h *Hub) TryTestVersion(ctx context.Context, by core.Viewer, version string) (core.TestVersion, error) {
	if h.ad.Updater == nil {
		return core.TestVersion{}, userErr("This Homelab doesn't update itself.")
	}
	if err := h.refuseIfBusy(); err != nil {
		return core.TestVersion{}, err
	}
	t, err := h.ad.Updater.Try(ctx, version)
	if err != nil {
		return t, userErr("Homelab didn't switch to the test version: %s.", err)
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "homelab.test", Detail: fmt.Sprintf("Started trying %s (PR #%d)", t.Version, t.PR)})
	return t, nil
}

// EndTest ends a test version: back to the version (and data) from before, or keep it.
func (h *Hub) EndTest(ctx context.Context, by core.Viewer, keep bool) (string, error) {
	if h.ad.Updater == nil {
		return "", userErr("This Homelab doesn't update itself.")
	}
	if keep {
		if err := h.ad.Updater.Keep(ctx); err != nil {
			return "", userErr("Couldn't keep the test version: %s.", err)
		}
		h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "homelab.test", Detail: "Kept the test version"})
		return "", nil
	}
	from, err := h.ad.Updater.GoBack(ctx)
	if err != nil {
		return "", userErr("Couldn't go back: %s.", err)
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), Action: "homelab.test", Detail: "Going back to " + from})
	return from, nil
}
