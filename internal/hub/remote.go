package hub

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// Remote access (milestone 4): a request that arrives over the tailnet carries the Tailscale
// login of whoever sent it. Homelab matches that login to a household person, so being
// signed in to Tailscale is being signed in to Homelab. Homelab also writes its own hl-...
// access rules from its people and apps, next to any rules already in the tailnet.

// Group and tag names in the tailnet policy.
const (
	groupEveryone = "hl-everyone"
	groupAdmins   = "hl-admins"
	companionTag  = "tag:hl-homelab"
)

// policyEvery is how often Homelab re-checks its part of the policy even when nothing
// changed here, which undoes hand edits to its hl-... rules. Other rules are never touched.
const policyEvery = 15 * time.Minute

// AddressTag is the Tailscale tag of an address, e.g. tag:hl-movies-dev.
func AddressTag(address string) string { return "tag:hl-" + address }

// TailnetPerson is the household person behind a viewer that came over the tailnet. A
// person's first visit counts as their sign-in.
func (h *Hub) TailnetPerson(ctx context.Context, v core.Viewer) (core.Person, bool) {
	if v.Local || v.Tagged || strings.TrimSpace(v.Login) == "" {
		return core.Person{}, false
	}
	p, err := h.store.PersonByTailscaleLogin(ctx, v.Login)
	if err != nil {
		return core.Person{}, false
	}
	if p.Status != "signed_in" {
		now := h.cfg.Now()
		if err := h.store.MarkTailnetSignIn(ctx, p.ID, now); err != nil {
			return core.Person{}, false
		}
		_ = h.store.RevokeInvites(ctx, p.ID)
		p.Status, p.LastSignIn = "signed_in", &now
		h.logEvent(ctx, state.Event{Actor: v.Login, Action: "person.signed_in", Detail: p.Name + " signed in through Tailscale"})
	}
	return p, true
}

// RemoteEnabled says whether people can join from elsewhere (a Tailscale API client is set up).
func (h *Hub) RemoteEnabled() bool { return h.ad.Remote != nil }

// inviteRemote sends p a tailnet invite. It returns the invite link, or why there is none;
// Homelab's own invite still works on this computer either way.
func (h *Hub) inviteRemote(ctx context.Context, p core.Person) (string, string) {
	login := strings.ToLower(strings.TrimSpace(p.Email))
	if login != "" {
		if err := h.store.SetTailscaleLogin(ctx, p.ID, login); err != nil {
			h.log.Warn("remote invite: save login", "person", p.ID, "err", err)
		}
	}
	if h.ad.Remote == nil {
		return "", ""
	}
	if login == "" {
		return "", "Add their email to invite them to Tailscale."
	}
	inv, err := h.ad.Remote.Invite(ctx, login)
	if err != nil {
		h.log.Warn("remote invite", "person", p.ID, "err", err)
		return "", "Tailscale didn't create an invite: " + err.Error()
	}
	if err := h.store.SetRemoteInvite(ctx, p.ID, inv.ID, inv.URL); err != nil {
		return "", "Homelab couldn't save the Tailscale invite. Try again."
	}
	h.syncPolicy(ctx, true)
	return inv.URL, ""
}

// accessPolicy is the tailnet rules Homelab wants: everyone reaches the companion, people
// reach the prod address of each app they use and the dev address of each app they build,
// and admins reach everything.
func (h *Hub) accessPolicy(ctx context.Context) (core.AccessPolicy, error) {
	people, err := h.store.People(ctx)
	if err != nil {
		return core.AccessPolicy{}, err
	}
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return core.AccessPolicy{}, err
	}
	pol := core.AccessPolicy{
		Groups:    map[string][]string{groupEveryone: {}, groupAdmins: {}},
		TagOwners: []string{companionTag},
	}
	all := []string{companionTag}
	for _, a := range apps {
		prod := AddressTag(manifest.AddressName(a.ID, core.Prod))
		dev := AddressTag(manifest.AddressName(a.ID, core.Dev))
		use, build := "hl-app-"+a.ID, "hl-build-"+a.ID
		pol.Groups[use], pol.Groups[build] = []string{}, []string{}
		pol.TagOwners = append(pol.TagOwners, prod, dev)
		all = append(all, prod, dev)
		pol.Grants = append(pol.Grants,
			core.AccessGrant{Groups: []string{use}, Tags: []string{prod}},
			core.AccessGrant{Groups: []string{build}, Tags: []string{prod, dev}})
	}
	for _, p := range people {
		login := strings.TrimSpace(p.TailscaleLogin)
		if login == "" {
			continue
		}
		pol.Groups[groupEveryone] = append(pol.Groups[groupEveryone], login)
		if contains(p.Roles, "owner") || contains(p.Roles, "admin") {
			pol.Groups[groupAdmins] = append(pol.Groups[groupAdmins], login)
		}
		for app, level := range p.Grants {
			if _, ok := pol.Groups["hl-app-"+app]; !ok {
				continue
			}
			pol.Groups["hl-app-"+app] = append(pol.Groups["hl-app-"+app], login)
			if level == "build" {
				pol.Groups["hl-build-"+app] = append(pol.Groups["hl-build-"+app], login)
			}
		}
	}
	pol.Grants = append([]core.AccessGrant{
		{Groups: []string{groupEveryone}, Tags: []string{companionTag}},
		{Groups: []string{groupAdmins}, Tags: all},
	}, pol.Grants...)
	return pol, nil
}

// syncPolicy sends the tailnet rules when they changed, or every policyEvery anyway.
func (h *Hub) syncPolicy(ctx context.Context, force bool) error {
	if h.ad.Remote == nil {
		return nil
	}
	pol, err := h.accessPolicy(ctx)
	if err != nil {
		return err
	}
	key := policyKey(pol)
	h.mu.Lock()
	same := key == h.remotePolicy && h.cfg.Now().Sub(h.remotePolicyAt) < policyEvery
	h.mu.Unlock()
	if same && !force {
		return nil
	}
	if err := h.ad.Remote.SetPolicy(ctx, pol); err != nil {
		h.log.Warn("remote: update access rules", "err", err)
		return err
	}
	h.mu.Lock()
	h.remotePolicy, h.remotePolicyAt = key, h.cfg.Now()
	h.mu.Unlock()
	return nil
}

func policyKey(p core.AccessPolicy) string {
	var b strings.Builder
	names := make([]string, 0, len(p.Groups))
	for n := range p.Groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		members := append([]string{}, p.Groups[n]...)
		sort.Strings(members)
		b.WriteString(n + "=" + strings.Join(members, ",") + ";")
	}
	tags := append([]string{}, p.TagOwners...)
	sort.Strings(tags)
	b.WriteString("|" + strings.Join(tags, ",") + "|")
	for _, g := range p.Grants {
		b.WriteString(strings.Join(g.Groups, ",") + ">" + strings.Join(g.Tags, ",") + ";")
	}
	return b.String()
}

// reconcileRemote keeps the tailnet's rules current and notices people's devices coming online.
func (h *Hub) reconcileRemote(ctx context.Context) {
	if h.ad.Remote == nil {
		return
	}
	_ = h.syncPolicy(ctx, false)
	h.mu.Lock()
	due := h.cfg.Now().Sub(h.remoteObservedAt) >= time.Minute
	if due {
		h.remoteObservedAt = h.cfg.Now()
	}
	h.mu.Unlock()
	if !due {
		return
	}
	st, err := h.ad.Remote.Observe(ctx)
	if err != nil {
		h.log.Warn("remote: observe tailnet", "err", err)
		return
	}
	people, err := h.store.People(ctx)
	if err != nil {
		return
	}
	seen := map[string]time.Time{}
	for _, d := range st.Devices {
		if d.Login == "" || len(d.Tags) > 0 || (!d.Online && d.LastSeen == nil) {
			continue
		}
		at := h.cfg.Now()
		if !d.Online {
			at = *d.LastSeen
		}
		key := strings.ToLower(d.Login)
		if prev, ok := seen[key]; !ok || at.Before(prev) {
			seen[key] = at
		}
	}
	for _, p := range people {
		at, ok := seen[strings.ToLower(p.TailscaleLogin)]
		if !ok || p.ConnectedAt != nil {
			continue
		}
		if err := h.store.MarkConnected(ctx, p.ID, at); err == nil {
			h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, Action: "person.connected", Detail: p.Name + " connected a device through Tailscale"})
		}
	}
}

// removeRemote takes a person off the tailnet: their invite, devices and account.
func (h *Hub) removeRemote(ctx context.Context, p core.Person) []RemovalStep {
	if h.ad.Remote == nil {
		return nil
	}
	var steps []RemovalStep
	if p.RemoteInviteID != "" {
		err := h.ad.Remote.CancelInvite(ctx, p.RemoteInviteID)
		steps = append(steps, RemovalStep{Name: "Cancel their Tailscale invite", Done: err == nil, Detail: remoteErrorText(err)})
	}
	if p.TailscaleLogin != "" {
		_, err := h.ad.Remote.RemoveDevices(ctx, p.TailscaleLogin)
		steps = append(steps, RemovalStep{Name: "Remove their devices from Tailscale", Done: err == nil, Detail: remoteErrorText(err)})
		err = h.ad.Remote.RemoveUser(ctx, p.TailscaleLogin)
		if errors.Is(err, core.ErrNotFound) {
			err = nil
		}
		steps = append(steps, RemovalStep{Name: "Remove them from the tailnet", Done: err == nil, Detail: remoteErrorText(err)})
	}
	return steps
}

func remoteErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error() + " You can also do this in the Tailscale admin console."
}
