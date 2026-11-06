package core

import (
	"slices"
	"testing"
)

// rowKey is the part of a row a test compares: who, what and how open.
func rowKey(r PortRow) string {
	return r.Env + "/" + r.Service + " " + r.HostPort + ":" + r.ContainerPort + "/" + r.Protocol + " " + r.Reach
}

func keys(rows []PortRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, rowKey(r))
	}
	return out
}

func TestPortRows(t *testing.T) {
	nl := EnvSpec{App: "nl", Env: Prod, Port: 8000, HostPort: 7101,
		Ports: []string{"8000:8000"},
		Services: []Service{
			{Name: "jellyfin", Ports: []string{"8096:8096", "6881-6891:6881-6891", "6881-6891:6881-6891/udp"}},
			{Name: "prowlarr", Ports: []string{"9696:9696"}},
		},
		Postgres: &Postgres{Port: 7106},
	}
	cases := []struct {
		name string
		mod  func(s *EnvSpec)
		want []string
	}{
		{"prod with open ports", func(s *EnvSpec) { s.OpenPorts = true }, []string{
			"prod/app 7101:8000/tcp this device only",
			"prod/app 8000:8000/tcp home network",
			"prod/jellyfin 8096:8096/tcp home network",
			"prod/jellyfin 6881-6891:6881-6891/tcp home network",
			"prod/jellyfin 6881-6891:6881-6891/udp home network",
			"prod/prowlarr 9696:9696/tcp home network",
			"prod/db 7106:5432/tcp this device only",
		}},
		{"prod on this device only", func(s *EnvSpec) {}, []string{
			"prod/app 7101:8000/tcp this device only",
			"prod/app 8000:8000/tcp this device only",
			"prod/jellyfin 8096:8096/tcp this device only",
			"prod/jellyfin 6881-6891:6881-6891/tcp this device only",
			"prod/jellyfin 6881-6891:6881-6891/udp this device only",
			"prod/prowlarr 9696:9696/tcp this device only",
			"prod/db 7106:5432/tcp this device only",
		}},
		{"dev opens only its own port and the database", func(s *EnvSpec) {
			s.Env, s.OpenPorts, s.Ports, s.Services = Dev, true, nil, []Service{{Name: "jellyfin"}, {Name: "prowlarr"}}
		}, []string{
			"dev/app 7101:8000/tcp this device only",
			"dev/db 7106:5432/tcp this device only",
		}},
		{"postgres stays local when ports are open", func(s *EnvSpec) {
			s.OpenPorts, s.Ports, s.Services = true, nil, nil
		}, []string{
			"prod/app 7101:8000/tcp this device only",
			"prod/db 7106:5432/tcp this device only",
		}},
		{"no database", func(s *EnvSpec) { s.Postgres, s.Ports, s.Services = nil, nil, nil }, []string{
			"prod/app 7101:8000/tcp this device only",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := nl
			c.mod(&s)
			if got := keys(PortRows(s)); !slices.Equal(got, c.want) {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestPortRowsHostNetwork(t *testing.T) {
	rows := PortRows(EnvSpec{App: "ha", Env: Prod, Port: 8123, HostNetwork: true})
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	if got := rowKey(rows[0]); got != "prod/app 8123:8123/tcp home network" {
		t.Errorf("row: %s", got)
	}
	if rows[0].Note == "" {
		t.Error("the row should say it uses the device's network")
	}
}

func TestPortRowsHint(t *testing.T) {
	prowlarr := Service{Name: "prowlarr", Ports: []string{"9696:9696"}}
	jellyfin := Service{Name: "jellyfin", Ports: []string{"8096:8096"}}
	cases := []struct {
		name string
		open bool
		vars map[string]string
		want map[string]bool // service -> has a hint
	}{
		{"app reaches prowlarr inside", true, map[string]string{"PROWLARR_URL": "http://prowlarr:9696"},
			map[string]bool{"prowlarr": true, "jellyfin": false}},
		{"with a path", true, map[string]string{"PROWLARR_URL": "http://prowlarr:9696/api/v1"},
			map[string]bool{"prowlarr": true, "jellyfin": false}},
		{"nothing refers to the services", true, map[string]string{"LOG": "debug"},
			map[string]bool{"prowlarr": false, "jellyfin": false}},
		{"another port of the same name", true, map[string]string{"URL": "http://prowlarr:96960"},
			map[string]bool{"prowlarr": false, "jellyfin": false}},
		{"a longer name is not the same service", true, map[string]string{"URL": "http://myprowlarr:9696"},
			map[string]bool{"prowlarr": false, "jellyfin": false}},
		{"no hint when the port is closed to the home network", false, map[string]string{"PROWLARR_URL": "http://prowlarr:9696"},
			map[string]bool{"prowlarr": false, "jellyfin": false}},
		{"both are reached", true, map[string]string{"A": "http://prowlarr:9696", "B": "http://jellyfin:8096"},
			map[string]bool{"prowlarr": true, "jellyfin": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := PortRows(EnvSpec{App: "nl", Env: Prod, Port: 8000, HostPort: 7101, OpenPorts: c.open,
				Vars: c.vars, Services: []Service{prowlarr, jellyfin}})
			for _, r := range rows {
				if r.Service == "app" {
					if r.Hint != "" {
						t.Errorf("the app row has a hint: %q", r.Hint)
					}
					continue
				}
				if (r.Hint != "") != c.want[r.Service] {
					t.Errorf("%s: hint %q, want hint: %v", r.Service, r.Hint, c.want[r.Service])
				}
			}
		})
	}
	rows := PortRows(EnvSpec{App: "nl", Env: Prod, Port: 8000, HostPort: 7101, OpenPorts: true,
		Vars: map[string]string{"U": "http://prowlarr:9696"}, Services: []Service{prowlarr}})
	want := "The app reaches prowlarr inside its own network. This port may not need to be open."
	if rows[1].Hint != want {
		t.Errorf("hint = %q", rows[1].Hint)
	}
}

func TestPortRowsRangeIsNeverHinted(t *testing.T) {
	rows := PortRows(EnvSpec{App: "nl", Env: Prod, Port: 8000, HostPort: 7101, OpenPorts: true,
		Vars:     map[string]string{"PEERS": "torrent:6881-6891"},
		Services: []Service{{Name: "torrent", Ports: []string{"6881-6891:6881-6891"}}}})
	for _, r := range rows {
		if r.Hint != "" {
			t.Errorf("hint on a range: %+v", r)
		}
	}
}
