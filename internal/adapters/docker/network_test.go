package docker

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

// The Network screen and the compose file must agree on which ports open and who reaches
// them. This renders the compose file and compares it with core.PortRows.
func TestPortRowsMatchCompose(t *testing.T) {
	for _, open := range []bool{false, true} {
		spec := testSpec()
		spec.Env, spec.OpenPorts = core.Prod, open
		spec.Ports = []string{"8000:8000", "6881-6891:6881-6891/udp"}
		spec.Services = []core.Service{{Name: "jellyfin", Ports: []string{"8096:8096"}}, {Name: "prowlarr", Ports: []string{"9696:9696"}}}
		spec.Postgres = &core.Postgres{Port: 18085, DataDir: "/data/db"}
		spec.Secrets = map[string]string{core.DatabasePassword: "x"}
		b, err := composeYAML(spec, "/srv/homelab/movies/prod")
		if err != nil {
			t.Fatal(err)
		}
		var file struct {
			Services map[string]struct{ Ports []string }
		}
		if err := json.Unmarshal(b, &file); err != nil {
			t.Fatal(err)
		}
		var fromCompose []string
		for name, s := range file.Services {
			for _, p := range s.Ports {
				reach := core.ReachHome
				if rest, ok := strings.CutPrefix(p, "127.0.0.1:"); ok {
					p, reach = rest, core.ReachDevice
				}
				proto := "tcp"
				if i := strings.Index(p, "/"); i >= 0 {
					p, proto = p[:i], p[i+1:]
				}
				host, container, _ := strings.Cut(p, ":")
				fromCompose = append(fromCompose, name+" "+host+":"+container+"/"+proto+" "+reach)
			}
		}
		var fromRows []string
		for _, r := range core.PortRows(spec) {
			fromRows = append(fromRows, r.Service+" "+r.HostPort+":"+r.ContainerPort+"/"+r.Protocol+" "+r.Reach)
		}
		slices.Sort(fromCompose)
		slices.Sort(fromRows)
		if !slices.Equal(fromCompose, fromRows) {
			t.Errorf("open=%s\ncompose: %q\nrows:    %q", strconv.FormatBool(open), fromCompose, fromRows)
		}
	}
}
