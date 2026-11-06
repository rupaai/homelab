package core

import (
	"regexp"
	"strconv"
	"strings"
)

// Who can reach a port (see PortRow.Reach).
const (
	ReachHome   = "home network"
	ReachDevice = "this device only"
)

// PortRow is one port Homelab or an app environment opens on the device. It is the row
// that Settings → Network shows.
type PortRow struct {
	App     string `json:"app"`     // the app id; "homelab" for Homelab's own ports
	AppName string `json:"appName"` // the name to show
	Env     string `json:"env"`     // dev | prod; empty for Homelab's own ports
	Service string `json:"service"` // "app", an extra service's name, "db", "address", or a Homelab part
	// HostPort is the port on the device, or a range like 6881-6891.
	HostPort string `json:"hostPort"`
	// ContainerPort is the port inside the container; empty when there is none.
	ContainerPort string `json:"containerPort"`
	Protocol      string `json:"protocol"` // tcp | udp
	Reach         string `json:"reach"`    // ReachHome | ReachDevice
	// Note says more about the row, for example that the host network is used.
	Note string `json:"note,omitempty"`
	// Hint says the port may not need to be open. It is a hint only: nothing changes.
	Hint string `json:"hint,omitempty"`
}

// ReachOf says who can reach a port that is published with open set or not. It is the same
// rule as the deploy adapter's: open ports are on every address, the rest on 127.0.0.1.
func ReachOf(open bool) string {
	if open {
		return ReachHome
	}
	return ReachDevice
}

// splitPort turns "host:container[/udp]" into its parts. Ranges stay as written.
func splitPort(p string) (host, container, proto string) {
	proto = "tcp"
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p, proto = p[:i], p[i+1:]
	}
	host, container, _ = strings.Cut(p, ":")
	return host, container, proto
}

// PortRows lists the ports one environment opens on the device, from the same spec that
// the deploy adapter turns into Docker ports. The app's own port and the database are
// published on 127.0.0.1 whatever OpenPorts says; extra ports follow OpenPorts. An app on
// the host network gets one row, because every port it listens on is open.
func PortRows(spec EnvSpec) []PortRow {
	var rows []PortRow
	add := func(service, host, container, proto, reach string) {
		rows = append(rows, PortRow{App: spec.App, AppName: spec.App, Env: spec.Env, Service: service,
			HostPort: host, ContainerPort: container, Protocol: proto, Reach: reach})
	}
	if spec.HostNetwork {
		p := strconv.Itoa(spec.Port)
		add("app", p, p, "tcp", ReachHome)
		rows[0].Note = "Uses the device's network, so every port the app listens on is open to the home network."
	} else {
		add("app", strconv.Itoa(spec.HostPort), strconv.Itoa(spec.Port), "tcp", ReachDevice)
		for _, p := range spec.Ports {
			host, container, proto := splitPort(p)
			add("app", host, container, proto, ReachOf(spec.OpenPorts))
		}
	}
	for _, s := range spec.Services {
		for _, p := range s.Ports {
			host, container, proto := splitPort(p)
			add(s.Name, host, container, proto, ReachOf(spec.OpenPorts))
			if r := &rows[len(rows)-1]; r.Reach == ReachHome && reachedInside(spec.Vars, s.Name, container) {
				r.Hint = "The app reaches " + s.Name + " inside its own network. This port may not need to be open."
			}
		}
	}
	if spec.Postgres != nil {
		add(DatabaseService, strconv.Itoa(spec.Postgres.Port), "5432", "tcp", ReachDevice)
	}
	return rows
}

// reachedInside says whether one of the app's settings points at name:port, which is how
// an app reaches another container in its own network (http://prowlarr:9696). Only a single
// port counts; a range is never checked.
func reachedInside(vars map[string]string, name, port string) bool {
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(name) + `:` + port + `($|[^0-9])`)
	for _, v := range vars {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}
