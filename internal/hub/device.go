package hub

import (
	"context"

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
	return v, nil
}

// UpdateHomelab installs the newest Homelab and restarts on it.
func (h *Hub) UpdateHomelab(ctx context.Context, by core.Viewer) (string, error) {
	if h.ad.Updater == nil {
		return "", userErr("This Homelab doesn't update itself.")
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
