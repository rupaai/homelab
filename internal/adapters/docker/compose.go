package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rupaai/homelab/internal/core"
)

// Labels Homehub puts on every container so Observe can find them again.
const (
	labelApp    = "homehub.app"
	labelEnv    = "homehub.env"
	labelCommit = "homehub.commit"
	labelDigest = "homehub.digest"

	serviceName = "app"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	varRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// project is the Docker Compose project name for one app environment.
func project(app, env string) string { return "homehub-" + app + "-" + env }

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
	Image      string            `json:"image"`
	PullPolicy string            `json:"pull_policy"`
	Restart    string            `json:"restart"`
	EnvFile    []string          `json:"env_file"`
	Ports      []string          `json:"ports"`
	Volumes    []composeMount    `json:"volumes,omitempty"`
	MemLimit   string            `json:"mem_limit,omitempty"`
	Logging    composeLogging    `json:"logging"`
	Labels     map[string]string `json:"labels"`
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
	for _, m := range spec.Mounts {
		if !filepath.IsAbs(m.HostPath) || !strings.HasPrefix(m.ContainerPath, "/") {
			return nil, fmt.Errorf("mount %q to %q: both paths must be absolute", m.HostPath, m.ContainerPath)
		}
		svc.Volumes = append(svc.Volumes, composeMount{Type: "bind", Source: dollars(m.HostPath), Target: dollars(m.ContainerPath)})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(composeFile{Services: map[string]composeService{serviceName: svc}}); err != nil {
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
