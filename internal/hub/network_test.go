package hub

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

const nlDoc = `
id: nl
name: Northern Lights
image: ghcr.io/rupaai/nl
port: 8000
database: postgres
env: {PROWLARR_URL: "http://prowlarr:9696"}
ports: ["8000:8000"]
services:
  - {name: jellyfin, image: "jellyfin/jellyfin:10.11.11", ports: ["8096:8096"]}
  - {name: prowlarr, image: "lscr.io/linuxserver/prowlarr:1.0", ports: ["9696:9696"]}
`

// portAddrs is the fake addresses with ports in their URLs, like the local adapter's.
type portAddrs struct {
	*fake.Addresses
	urls map[string]string
}

func (a portAddrs) Observe() map[string]core.Address {
	out := map[string]core.Address{}
	for n, u := range a.urls {
		out[n] = core.Address{Name: n, URL: u, Ready: true}
	}
	return out
}

func networkRig(t *testing.T, doc string) *rig {
	t.Helper()
	r := newRig(t)
	a := state.App{ID: "nl", Name: "Northern Lights", Repo: "rupaai/nl", Manifest: []byte(doc), Promotion: "manual"}
	if _, err := r.store.AddApp(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{core.Dev, core.Prod} {
		rel := core.Release{Image: "ghcr.io/rupaai/nl", Commit: "abc", Manifest: []byte(doc)}
		if _, err := r.store.SetWant(context.Background(), state.Deployment{App: "nl", Env: env, Release: rel}); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func netKeys(rows []core.PortRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Env+"/"+r.Service+" "+r.HostPort+" "+r.Protocol+" "+r.Reach)
	}
	return out
}

func TestNetworkIPMode(t *testing.T) {
	r := networkRig(t, nlDoc)
	r.hub.cfg.OpenApps = true
	r.hub.ad.Addresses = portAddrs{r.addr, map[string]string{"nl": "http://localhost:7100", "nl-dev": "http://localhost:7101"}}
	r.hub.cfg.OwnPorts = []core.PortRow{{App: "homelab", AppName: "Homelab", Service: "companion", HostPort: "7080", Protocol: "tcp", Reach: core.ReachHome}}
	rows, err := r.hub.Network(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slot := r.hub.hostPort(1, core.Prod)
	dev := r.hub.hostPort(1, core.Dev)
	got := netKeys(rows)
	for _, want := range []string{
		"prod/app " + itoa(slot) + " tcp this device only",
		"prod/app 8000 tcp home network",
		"prod/jellyfin 8096 tcp home network",
		"prod/prowlarr 9696 tcp home network",
		"prod/db " + itoa(slot+5) + " tcp this device only",
		"prod/address 7100 tcp home network",
		"dev/app " + itoa(dev) + " tcp this device only",
		"dev/db " + itoa(dev+5) + " tcp this device only",
		"dev/address 7101 tcp home network",
		"/companion 7080 tcp home network",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in\n%q", want, got)
		}
	}
	for _, g := range got {
		if strings.HasPrefix(g, "dev/jellyfin") || strings.HasPrefix(g, "dev/prowlarr") || strings.Contains(g, "dev/app 8000") {
			t.Errorf("dev opens extra ports: %s", g)
		}
	}
	for _, row := range rows {
		hinted := row.Hint != ""
		if hinted != (row.Service == "prowlarr") {
			t.Errorf("hint on %s/%s: %q", row.Env, row.Service, row.Hint)
		}
		if row.Service != "homelab" && row.App != "homelab" && row.AppName != "Northern Lights" {
			t.Errorf("app name: %+v", row)
		}
		if row.Service == "address" && row.Note == "" {
			t.Errorf("an open address should say it has no sign-in: %+v", row)
		}
	}
}

func TestNetworkLocalMode(t *testing.T) {
	r := networkRig(t, nlDoc)
	r.hub.ad.Addresses = portAddrs{r.addr, map[string]string{"nl": "http://localhost:7100"}}
	rows, err := r.hub.Network(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Reach != core.ReachDevice || row.Hint != "" {
			t.Errorf("local mode opens nothing to the network: %+v", row)
		}
	}
}

func TestNetworkSkipsTurnedOffAndEmptyEnvs(t *testing.T) {
	ctx := context.Background()
	r := networkRig(t, nlDoc)
	r.hub.cfg.OpenApps = true
	if _, err := r.store.SetOff(ctx, "nl", core.Dev, true); err != nil {
		t.Fatal(err)
	}
	rows, _ := r.hub.Network(ctx)
	for _, row := range rows {
		if row.Env == core.Dev {
			t.Errorf("a turned-off dev opens nothing, but is listed: %+v", row)
		}
	}
	if len(rows) == 0 {
		t.Fatal("prod should still be listed")
	}
	// Nothing deployed: nothing listed.
	r2 := newRig(t)
	if _, err := r2.store.AddApp(ctx, state.App{ID: "nl", Name: "NL", Repo: "rupaai/nl", Manifest: []byte(nlDoc), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := r2.hub.Network(ctx); len(rows) != 0 {
		t.Errorf("rows for an app that isn't deployed: %+v", rows)
	}
}

func TestNetworkHostNetworkNeedsTrust(t *testing.T) {
	ctx := context.Background()
	r := networkRig(t, strings.Replace(haDoc, "id: movies", "id: nl", 1))
	// Not in hardware_apps: deploy is refused, so no host network is claimed.
	rows, _ := r.hub.Network(ctx)
	for _, row := range rows {
		if row.Reach == core.ReachHome {
			t.Errorf("untrusted app on the host network: %+v", row)
		}
	}
	r.hub.cfg.HardwareApps = []string{"nl"}
	rows, _ = r.hub.Network(ctx)
	var prod []core.PortRow
	for _, row := range rows {
		if row.Env == core.Prod && row.Service == "app" {
			prod = append(prod, row)
		}
		if row.Env == core.Dev && row.Reach == core.ReachHome {
			t.Errorf("dev never uses the host network: %+v", row)
		}
	}
	if len(prod) != 1 || prod[0].Reach != core.ReachHome || prod[0].HostPort != "8123" || prod[0].Note == "" {
		t.Errorf("host network rows: %+v", prod)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
