package hub

import (
	"context"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// These types are the JSON the companion reads (GET /api/apps). Keep them stable; add
// fields rather than renaming them.

// AppView is one app in the builder view.
type AppView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Icon        string     `json:"icon"`
	Repo        string     `json:"repo"`
	RepoURL     string     `json:"repoUrl"`
	Promotion   string     `json:"promotion"`
	Envs        []EnvView  `json:"envs"` // dev first, then prod
	LatestBuild *BuildView `json:"latestBuild"`

	PromoteAfter string             `json:"promoteAfter"` // e.g. "30m"
	AutoPromote  *autoState         `json:"autoPromote"`  // only for apps on auto
	Held         []HoldView         `json:"held"`         // commits auto-promote leaves alone
	Mirror       *core.MirrorStatus `json:"mirror"`       // nil when there's no mirror
}

// HoldView is a commit auto-promote leaves alone.
type HoldView struct {
	Commit string    `json:"commit"`
	Short  string    `json:"short"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// BuildView is the newest build Homelab has seen for an app.
type BuildView struct {
	Commit   string `json:"commit"`
	Short    string `json:"short"`
	Status   string `json:"status"` // passed | failed | running
	URL      string `json:"url"`
	Deployed bool   `json:"deployed"` // already on dev, or already handled (e.g. skipped, see the activity log)
}

// ReleaseView is a release as people see it.
type ReleaseView struct {
	core.Release
	Short     string `json:"short"`
	CommitURL string `json:"commitUrl"`
}

// EnvView is one environment of an app.
type EnvView struct {
	Env          string       `json:"env"` // dev | prod
	Address      string       `json:"address"`
	AddressName  string       `json:"addressName"`
	AddressReady bool         `json:"addressReady"`
	Version      *ReleaseView `json:"version"` // what should run; nil when nothing is deployed
	State        string       `json:"state"`   // container state, "" when there's no container
	Health       string       `json:"health"`  // healthy | degraded | down | unknown
	HealthDetail string       `json:"healthDetail"`
	HealthSince  *time.Time   `json:"healthSince"`
	CheckedAt    *time.Time   `json:"checkedAt"`
	Busy         string       `json:"busy"`      // e.g. "Promoting 9f3c2ab"; "" when idle
	LastError    string       `json:"lastError"` // the last failed attempt to start it
	DeployedBy   string       `json:"deployedBy"`
	DeployedAt   *time.Time   `json:"deployedAt"`
	DeployKind   string       `json:"deployKind"` // deploy | promote | rollback
	CanRollback  bool         `json:"canRollback"`
	// MissingSecrets are secrets homelab.yaml declares that have no value here.
	MissingSecrets []string `json:"missingSecrets"`
}

func (h *Hub) releaseView(a state.App, r *core.Release) *ReleaseView {
	if r == nil {
		return nil
	}
	return &ReleaseView{Release: *r, Short: r.Short(), CommitURL: h.ad.Source.WebURL(a.Repo, "", r.Commit)}
}

// Overview is every app with its environments, for the builder view.
func (h *Hub) Overview(ctx context.Context) ([]AppView, error) {
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return nil, err
	}
	addrs := h.ad.Addresses.Observe()
	out := []AppView{}
	for _, a := range apps {
		v, err := h.appView(ctx, a, addrs)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// App is one app's view, or a UserError when it doesn't exist.
func (h *Hub) App(ctx context.Context, id string) (AppView, error) {
	a, err := h.store.App(ctx, id)
	if err != nil {
		return AppView{}, userErr("There's no app %q.", id)
	}
	return h.appView(ctx, a, h.ad.Addresses.Observe())
}

func (h *Hub) appView(ctx context.Context, a state.App, addrs map[string]core.Address) (AppView, error) {
	v := AppView{ID: a.ID, Name: a.Name, Repo: a.Repo, RepoURL: h.ad.Source.WebURL(a.Repo, "", ""), Promotion: a.Promotion, Held: []HoldView{}}
	var declared []string
	if m, err := h.parse(a.Manifest, a.Repo); err == nil {
		v.Icon = m.Icon
		v.PromoteAfter = shortDuration(m.PromoteAfter)
		declared = m.Secrets
	}
	if holds, err := h.store.Holds(ctx, a.ID); err == nil {
		for _, hd := range holds {
			v.Held = append(v.Held, HoldView{Commit: hd.Commit, Short: short(hd.Commit), Reason: hd.Reason, At: hd.At})
		}
	}
	if h.ad.Mirror != nil {
		ms := h.ad.Mirror.Status(a.Repo)
		v.Mirror = &ms
	}
	var devCommit, devLastBuild string
	for _, env := range []string{core.Dev, core.Prod} {
		e, err := h.store.Env(ctx, a.ID, env)
		if err != nil {
			return v, err
		}
		name := manifest.AddressName(a.ID, env)
		ev := EnvView{Env: env, AddressName: name, Version: h.releaseView(a, e.Want), MissingSecrets: []string{}}
		if miss, err := h.ad.Secrets.Missing(a.ID, env, declared); err == nil && miss != nil {
			ev.MissingSecrets = miss
		}
		if ad, ok := addrs[name]; ok {
			ev.Address, ev.AddressReady = ad.URL, ad.Ready
		}
		if e.Want != nil {
			if env == core.Dev {
				devCommit = e.Want.Commit
			}
		}
		if env == core.Dev {
			devLastBuild = e.LastBuild
		}
		if e.Want != nil {
			if hist, err := h.store.History(ctx, a.ID, env, 1); err == nil && len(hist) > 0 {
				t := hist[0].At
				ev.DeployedBy, ev.DeployedAt, ev.DeployKind = hist[0].By, &t, hist[0].Kind
			}
			if _, prev, err := h.previous(ctx, a.ID, env); err == nil && prev != nil {
				ev.CanRollback = true
			}
		}
		h.mu.Lock()
		s := h.st(a.ID, env)
		ev.Health, ev.HealthDetail, ev.Busy, ev.LastError = s.health, s.detail, s.busy, s.lastErr
		if e.Want == nil {
			ev.Health, ev.HealthDetail = core.Unknown, "Nothing deployed yet"
		}
		if !s.since.IsZero() {
			t := s.since
			ev.HealthSince = &t
		}
		if !s.checkedAt.IsZero() {
			t := s.checkedAt
			ev.CheckedAt = &t
		}
		if s.running != nil {
			ev.State = s.running.State
		}
		h.mu.Unlock()
		v.Envs = append(v.Envs, ev)
	}
	h.mu.Lock()
	if a.Promotion == core.PromotionAuto {
		if n, ok := h.autoNote[a.ID]; ok {
			v.AutoPromote = &n
		} else {
			v.AutoPromote = &autoState{State: "waiting", Detail: "Checking whether dev is ready to promote."}
		}
	}
	if b, ok := h.builds[a.ID]; ok {
		v.LatestBuild = &BuildView{Commit: b.Commit, Short: short(b.Commit), Status: b.Status, URL: b.URL, Deployed: b.Commit == devCommit || b.Commit == devLastBuild}
	}
	h.mu.Unlock()
	return v, nil
}

// shortDuration writes 30m0s as 30m and 1h30m0s as 1h30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
