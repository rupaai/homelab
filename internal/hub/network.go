package hub

import (
	"context"
	"net/url"
	"slices"
	"strconv"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
)

// Network lists every port Homelab and its app environments open on the device, for
// Settings → Network. It reads the stored manifests, not Docker, so it only ever names
// containers Homelab made. An environment that is turned off or has nothing deployed opens
// nothing and is left out. It changes nothing.
func (h *Hub) Network(ctx context.Context) ([]core.PortRow, error) {
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return nil, err
	}
	addrs := h.ad.Addresses.Observe()
	rows := []core.PortRow{}
	for _, a := range apps {
		for _, env := range []string{core.Prod, core.Dev} {
			e, err := h.store.Env(ctx, a.ID, env)
			if err != nil || e.Want == nil || e.Off {
				continue
			}
			m, err := h.parse(e.Want.Manifest, a.Repo)
			if err != nil {
				continue
			}
			for _, r := range core.PortRows(h.portSpec(a.ID, a.Slot, env, m)) {
				r.AppName = a.Name
				rows = append(rows, r)
			}
			if r, ok := h.addressRow(addrs[manifest.AddressName(a.ID, env)]); ok {
				r.App, r.AppName, r.Env = a.ID, a.Name, env
				rows = append(rows, r)
			}
		}
	}
	return append(rows, h.cfg.OwnPorts...), nil
}

// portSpec is the part of an environment's deploy spec that decides which ports open. It
// has no secrets and makes no folders, so it can be built any time.
func (h *Hub) portSpec(app string, slot int, env string, m *manifest.Manifest) core.EnvSpec {
	spec := core.EnvSpec{App: app, Env: env, Port: m.Port, HostPort: h.hostPort(slot, env),
		Vars: m.VarsFor(env), OpenPorts: h.cfg.OpenApps, Ports: prodPorts(env, m.Ports)}
	// The same rule as hardware(): only a trusted prod app gets the host network.
	spec.HostNetwork = env == core.Prod && m.Network == manifest.NetworkHost && slices.Contains(h.cfg.HardwareApps, app)
	if m.Database == core.DatabasePostgres {
		spec.Postgres = &core.Postgres{Port: h.dbPort(slot, env)}
	}
	for _, s := range m.Services {
		spec.Services = append(spec.Services, core.Service{Name: s.Name, Ports: prodPorts(env, s.Ports)})
	}
	return spec
}

// addressRow is the port Homelab serves an environment's address on (7100 and up). With
// addresses: ip it is open to the home network, and anyone who reaches it may use the app.
func (h *Hub) addressRow(ad core.Address) (core.PortRow, bool) {
	u, err := url.Parse(ad.URL)
	if err != nil || !ad.Ready {
		return core.PortRow{}, false
	}
	port := u.Port()
	if _, err := strconv.Atoi(port); err != nil {
		return core.PortRow{}, false
	}
	r := core.PortRow{Service: "address", HostPort: port, Protocol: "tcp", Reach: core.ReachOf(h.cfg.OpenApps)}
	if h.cfg.OpenApps {
		r.Note = "Anyone on the home network can open this address without signing in."
	}
	return r, true
}
