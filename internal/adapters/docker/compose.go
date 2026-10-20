package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/rupaai/homelab/internal/core"
)

// Labels Homelab puts on every container so Observe can find them again.
const (
	labelApp    = "homelab.app"
	labelEnv    = "homelab.env"
	labelCommit = "homelab.commit"
	labelDigest = "homelab.digest"
	// labelService names an extra service's container; the app's own has none.
	labelService = "homelab.service"

	serviceName = "app"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	varRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// project is the Docker Compose project name for one app environment.
func project(app, env string) string { return "homelab-" + app + "-" + env }

// checkNames makes sure app and env are safe to use in folder names and project names.
func checkNames(app, env string) error {
	if !nameRe.MatchString(app) {
		return fmt.Errorf("app name %q must be lowercase letters, digits, - or _", app)
	}
	if !nameRe.MatchString(env) {
		return fmt.Errorf("environment name %q must be lowercase letters, digits, - or _", env)
	}
	return nil
}

// dollars doubles "$" so Compose doesn't treat it as a variable in the compose file.
func dollars(s string) string { return strings.ReplaceAll(s, "$", "$$") }

type composeFile struct {
	Services map[string]composeService `json:"services"`
}

type composeService struct {
	Image       string            `json:"image"`
	PullPolicy  string            `json:"pull_policy"`
	Restart     string            `json:"restart"`
	EnvFile     []string          `json:"env_file,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Ports       []string          `json:"ports"`
	Volumes     []composeMount    `json:"volumes,omitempty"`
	Devices     []string          `json:"devices,omitempty"`
	GroupAdd    []string          `json:"group_add,omitempty"`
	MemLimit    string            `json:"mem_limit,omitempty"`
	ExtraHosts  []string          `json:"extra_hosts,omitempty"`
	Logging     composeLogging    `json:"logging"`
	Labels      map[string]string `json:"labels"`
}

// addHostGateway gives the app container the name host.docker.internal for Docker's bridge
// address, where Homelab's app door listens. Linux needs it; on the Mac Docker already has
// that name. A variable so tests can pretend.
var addHostGateway = runtime.GOOS == "linux"

// renderGroup is the group that may use the graphics card, as a number, or "" when the
// device has none. A variable so tests can pretend.
var renderGroup = func() string {
	st, err := os.Stat(renderNode)
	if err != nil {
		return ""
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return strconv.Itoa(int(s.Gid))
	}
	return ""
}

const renderNode = "/dev/dri/renderD128"

// publish turns "host:container[/udp]" into a compose port on 127.0.0.1, or on every
// network when open.
func publish(p string, open bool) string {
	if open {
		return p
	}
	return "127.0.0.1:" + p
}

func mounts(ms []core.Mount) ([]composeMount, error) {
	var out []composeMount
	for _, m := range ms {
		if !filepath.IsAbs(m.HostPath) || !strings.HasPrefix(m.ContainerPath, "/") {
			return nil, fmt.Errorf("mount %q to %q: both paths must be absolute", m.HostPath, m.ContainerPath)
		}
		out = append(out, composeMount{Type: "bind", Source: dollars(m.HostPath), Target: dollars(m.ContainerPath)})
	}
	return out, nil
}

type composeMount struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Target string `json:"target"`
}

type composeLogging struct {
	Driver  string            `json:"driver"`
	Options map[string]string `json:"options"`
}

// composeYAML renders the compose project for spec. The output is JSON, which is valid
// YAML, so quoting is always right and the result is the same every time.
func composeYAML(spec core.EnvSpec) ([]byte, error) {
	if err := checkNames(spec.App, spec.Env); err != nil {
		return nil, err
	}
	if spec.Release.Image == "" {
		return nil, fmt.Errorf("%s/%s has no image to run", spec.App, spec.Env)
	}
	if spec.Port <= 0 || spec.HostPort <= 0 {
		return nil, fmt.Errorf("%s/%s needs a port and a host port", spec.App, spec.Env)
	}
	svc := composeService{
		Image:      dollars(spec.Release.ImageRef()),
		PullPolicy: "never",
		Restart:    "unless-stopped",
		EnvFile:    []string{".env"},
		Ports:      []string{"127.0.0.1:" + strconv.Itoa(spec.HostPort) + ":" + strconv.Itoa(spec.Port)},
		MemLimit:   spec.Memory,
		Logging: composeLogging{
			Driver:  "json-file",
			Options: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		Labels: map[string]string{
			labelApp:    dollars(spec.App),
			labelEnv:    dollars(spec.Env),
			labelCommit: dollars(spec.Release.Commit),
			labelDigest: dollars(spec.Release.Digest),
		},
	}
	if addHostGateway {
		svc.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	}
	var err error
	if svc.Volumes, err = mounts(spec.Mounts); err != nil {
		return nil, err
	}
	for _, p := range spec.Ports {
		svc.Ports = append(svc.Ports, publish(p, spec.OpenPorts))
	}
	file := composeFile{Services: map[string]composeService{serviceName: svc}}
	for _, s := range spec.Services {
		if !nameRe.MatchString(s.Name) || s.Name == serviceName {
			return nil, fmt.Errorf("service name %q must be lowercase letters, digits, - or _, and not %q", s.Name, serviceName)
		}
		if _, dup := file.Services[s.Name]; dup {
			return nil, fmt.Errorf("there are two services named %q", s.Name)
		}
		x := composeService{
			Image:      dollars(s.Image),
			PullPolicy: "never",
			Restart:    "unless-stopped",
			Ports:      []string{},
			MemLimit:   s.Memory,
			Logging:    svc.Logging,
			Labels:     map[string]string{labelApp: dollars(spec.App), labelEnv: dollars(spec.Env), labelService: dollars(s.Name)},
		}
		if len(s.Vars) > 0 {
			x.Environment = map[string]string{}
			for k, v := range s.Vars {
				if !varRe.MatchString(k) {
					return nil, fmt.Errorf("%s: setting name %q must be letters, digits and _", s.Name, k)
				}
				x.Environment[k] = dollars(v)
			}
		}
		if x.Volumes, err = mounts(s.Mounts); err != nil {
			return nil, err
		}
		for _, p := range s.Ports {
			x.Ports = append(x.Ports, publish(p, spec.OpenPorts))
		}
		if s.GPU {
			// Without a graphics card the service still runs, just without it (e.g. Jellyfin
			// transcodes on the processor).
			if gid := renderGroup(); gid != "" {
				x.Devices = []string{"/dev/dri:/dev/dri"}
				x.GroupAdd = []string{gid}
			}
		}
		file.Services[s.Name] = x
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// envFile renders vars in Compose's env_file format, sorted by name. Values are quoted so
// spaces, #, quotes and $ come through exactly as given. Newlines can't be stored safely
// and are rejected.
func envFile(vars map[string]string) ([]byte, error) {
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, k := range names {
		v := vars[k]
		if !varRe.MatchString(k) {
			return nil, fmt.Errorf("setting name %q must be letters, digits and _ and not start with a digit", k)
		}
		if strings.HasPrefix(k, "COMPOSE_") {
			return nil, fmt.Errorf("setting name %q is reserved for Docker Compose", k)
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return nil, fmt.Errorf("the value of %s has a line break, which can't be passed to the app", k)
		}
		buf.WriteString(k + "=" + quoteEnv(v) + "\n")
	}
	return buf.Bytes(), nil
}

// quoteEnv single-quotes v (nothing inside is special), or double-quotes it when v itself
// contains a single quote.
func quoteEnv(v string) string {
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	return `"` + r.Replace(v) + `"`
}
