// Package manifest reads an app's homelab.yaml: the small contract between an app and
// Homelab (see blocks/apps_decisions.md). Fields Homelab doesn't use yet are accepted and
// ignored, so an app can carry its full manifest from day one.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/rupaai/homelab/internal/core"
)

// FileName is where Homelab looks for the manifest in an app's repo.
const FileName = "homelab.yaml"

// Manifest is a parsed homelab.yaml.
type Manifest struct {
	ID           string                       `yaml:"id"`
	Name         string                       `yaml:"name"`
	Icon         string                       `yaml:"icon"`
	Image        string                       `yaml:"image"` // e.g. ghcr.io/rupaai/smart-recommender-player
	Tag          string                       `yaml:"tag"`   // template; "{commit}" is replaced. Default "{commit}"
	Build        Build                        `yaml:"build"`
	Port         int                          `yaml:"port"`
	Health       string                       `yaml:"health"`
	Promotion    string                       `yaml:"promotion"`
	Environments []string                     `yaml:"environments"`
	Data         []Data                       `yaml:"data"`
	Env          map[string]string            `yaml:"env"`     // settings for every environment
	EnvFor       map[string]map[string]string `yaml:"env_for"` // per-environment overrides: env_for.dev.X
	Secrets      []string                     `yaml:"secrets"`
	Memory       string                       `yaml:"memory"`

	// Folders are the device's shared folders the app sees, e.g. its media library. The
	// device names them in config.yaml (folders:), so an app can't reach anything else.
	Folders []Data `yaml:"folders"`
	// Ports are opened on the device for prod, as "host:container" or "host:container/udp",
	// e.g. for torrent peers. The app's web address doesn't need one.
	Ports []string `yaml:"ports"`
	// Services are more containers that run next to the app in each environment.
	Services []Service `yaml:"services"`

	// SmokeTest is a command run inside the app's own image against dev before an auto
	// promote (C6), e.g. ["python", "-m", "app.smoke"]. It reads HOMELAB_TARGET_URL.
	SmokeTest []string `yaml:"smoke_test"`
	// PromoteAfter is how long dev must stay healthy before an auto promote. Default 30m.
	PromoteAfter    time.Duration `yaml:"-"`
	PromoteAfterRaw string        `yaml:"promote_after"`

	// Accepted for later milestones, unused so far.
	VisibleTo []string `yaml:"visible_to"`
	Auth      string   `yaml:"auth"`
}

// Defaults fill in what an app's manifest leaves out, from where the app comes from, so a
// repo made from the template works without editing homelab.yaml.
type Defaults struct {
	Repo      string // owner/name, e.g. rupaai/homelab-hello
	ImageHost string // registry host for the default image, e.g. ghcr.io
}

// DefaultPromoteAfter is how long dev must be healthy before an auto promote (C6).
const DefaultPromoteAfter = 30 * time.Minute

// IDFromRepo turns owner/name into an app id: rupaai/homelab-hello -> rupaai-homelab-hello.
func IDFromRepo(repo string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(repo) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if id != "" && (id[0] < 'a' || id[0] > 'z') {
		id = "app-" + id
	}
	if len(id) > maxIDLen {
		id = strings.TrimRight(id[:maxIDLen], "-")
	}
	return id
}

// NameFromRepo turns owner/name into a display name: rupaai/homelab-hello -> Homelab Hello.
func NameFromRepo(repo string) string {
	name := repo[strings.LastIndex(repo, "/")+1:]
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' })
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// Build says which branch and which CI workflow produce the app's images.
type Build struct {
	Branch   string `yaml:"branch"`   // default "main"
	Workflow string `yaml:"workflow"` // e.g. "ci.yml"; empty means any workflow
}

// Data is one folder of the app's data. Each environment gets its own copy (A4, A6).
type Data struct {
	Name string `yaml:"name"` // folder name on the host, e.g. "db"
	Path string `yaml:"path"` // where the app sees it, e.g. "/data"
}

// Service is one more container, such as a media server. The app reaches it by name
// (http://jellyfin:8096). Its data names share the app's data folder, so it gets its own
// copy per environment, copied before every promote like the app's.
type Service struct {
	Name    string            `yaml:"name"`
	Image   string            `yaml:"image"` // with a fixed tag, e.g. jellyfin/jellyfin:10.11.11
	Env     map[string]string `yaml:"env"`
	Data    []Data            `yaml:"data"`
	Folders []Data            `yaml:"folders"`
	Ports   []string          `yaml:"ports"`
	Memory  string            `yaml:"memory"`
	GPU     bool              `yaml:"gpu"`
}

// maxIDLen keeps "<id>-dev" within a 63-character DNS label.
const maxIDLen = 59

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,57}[a-z0-9]$`)
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	varPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memPattern  = regexp.MustCompile(`^[0-9]+[kmg]$`)
	portPattern = regexp.MustCompile(`^([0-9]+)(?:-([0-9]+))?:([0-9]+)(?:-([0-9]+))?(/tcp|/udp)?$`)
)

// Parse reads and validates a manifest, filling in defaults.
func Parse(b []byte, d Defaults) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if m.ID == "" && d.Repo != "" {
		m.ID = IDFromRepo(d.Repo)
	}
	if m.Image == "" && d.Repo != "" && d.ImageHost != "" {
		m.Image = d.ImageHost + "/" + strings.ToLower(d.Repo)
	}
	if m.Name == "" && d.Repo != "" {
		m.Name = NameFromRepo(d.Repo)
	}
	m.PromoteAfter = DefaultPromoteAfter
	if m.PromoteAfterRaw != "" {
		pa, err := time.ParseDuration(m.PromoteAfterRaw)
		if err != nil || pa < time.Minute || pa > 7*24*time.Hour {
			return nil, fmt.Errorf("%s: promote_after %q must be a duration between 1m and 168h, e.g. 30m", FileName, m.PromoteAfterRaw)
		}
		m.PromoteAfter = pa
	}
	if m.Tag == "" {
		m.Tag = "{commit}"
	}
	if m.Build.Branch == "" {
		m.Build.Branch = "main"
	}
	if m.Health == "" {
		m.Health = "/health"
	}
	if m.Promotion == "" {
		m.Promotion = core.PromotionAutoDev
	}
	if len(m.Environments) == 0 {
		m.Environments = []string{core.Dev, core.Prod}
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	var problems []string
	add := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	if !idPattern.MatchString(m.ID) {
		add("id %q must be 2-59 lowercase letters, digits or dashes, starting with a letter", m.ID)
	}
	if strings.HasSuffix(m.ID, "-dev") {
		add("id %q can't end in -dev, because that's the dev address", m.ID)
	}
	if m.ID == "homelab" {
		add("id \"homelab\" is reserved for the companion")
	}
	last := m.Image[strings.LastIndex(m.Image, "/")+1:]
	if m.Image == "" || strings.ContainsAny(m.Image, "@ ") || strings.Contains(last, ":") {
		add("image must be an image name without a tag, e.g. ghcr.io/you/app")
	}
	if !strings.Contains(m.Tag, "{commit}") {
		add("tag %q must contain {commit}, so each build is tied to its commit", m.Tag)
	}
	if m.Port < 1 || m.Port > 65535 {
		add("port must be the port the app listens on inside its container")
	}
	if !strings.HasPrefix(m.Health, "/") {
		add("health must be a path such as /health")
	}
	switch m.Promotion {
	case core.PromotionManual, core.PromotionAutoDev, core.PromotionAuto:
	default:
		add("promotion must be manual, auto-dev or auto")
	}
	hasDev, hasProd := false, false
	for _, e := range m.Environments {
		switch e {
		case core.Dev:
			hasDev = true
		case core.Prod:
			hasProd = true
		default:
			add("environment %q isn't supported; use dev and prod", e)
		}
	}
	if !hasDev || !hasProd {
		add("environments must include dev and prod")
	}
	checkMounts(add, "", m.Data, m.Folders)
	checkPorts(add, "", m.Ports)
	seenSvc := map[string]bool{}
	for _, s := range m.Services {
		where := "services." + s.Name + ": "
		if !namePattern.MatchString(s.Name) || s.Name == "app" || seenSvc[s.Name] {
			add("service name %q must be unique, lowercase letters, digits, - or _, and not \"app\"", s.Name)
		}
		seenSvc[s.Name] = true
		last := s.Image[strings.LastIndex(s.Image, "/")+1:]
		if s.Image == "" || strings.Contains(s.Image, " ") || !(strings.Contains(last, ":") || strings.Contains(s.Image, "@")) ||
			strings.HasSuffix(s.Image, ":latest") {
			add("%simage must have a fixed tag, e.g. jellyfin/jellyfin:10.11.11", where)
		}
		for k := range s.Env {
			if !varPattern.MatchString(k) {
				add("%senv name %q isn't a valid variable name", where, k)
			}
		}
		checkMounts(add, where, s.Data, s.Folders)
		checkPorts(add, where, s.Ports)
		if s.Memory != "" && !memPattern.MatchString(strings.ToLower(s.Memory)) {
			add("%smemory %q must look like 512m or 2g", where, s.Memory)
		}
	}
	for k := range m.Env {
		if !varPattern.MatchString(k) {
			add("env name %q isn't a valid variable name", k)
		}
	}
	for env, vars := range m.EnvFor {
		if env != core.Dev && env != core.Prod {
			add("env_for.%s: only dev and prod are supported", env)
		}
		for k := range vars {
			if !varPattern.MatchString(k) {
				add("env_for.%s name %q isn't a valid variable name", env, k)
			}
		}
	}
	for _, s := range m.Secrets {
		if !varPattern.MatchString(s) {
			add("secret name %q isn't a valid variable name", s)
		}
		if strings.HasPrefix(s, "HOMELAB_") {
			add("secret name %q: names starting with HOMELAB_ are Homelab's own", s)
		}
		if _, clash := m.Env[s]; clash {
			add("%q is both a setting and a secret", s)
		}
	}
	for _, a := range m.SmokeTest {
		if a == "" {
			add("smoke_test must be a list of non-empty words, e.g. [python, -m, app.smoke]")
			break
		}
	}
	if m.Memory != "" && !memPattern.MatchString(strings.ToLower(m.Memory)) {
		add("memory %q must look like 512m or 2g", m.Memory)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// checkMounts checks one container's data and folder lists.
func checkMounts(add func(string, ...any), where string, data, folders []Data) {
	seen, paths := map[string]bool{}, map[string]bool{}
	for _, list := range []struct {
		kind  string
		items []Data
	}{{"data", data}, {"folder", folders}} {
		for _, d := range list.items {
			if !namePattern.MatchString(d.Name) || seen[list.kind+d.Name] {
				add("%s%s name %q must be unique, lowercase letters, digits, - or _", where, list.kind, d.Name)
			}
			seen[list.kind+d.Name] = true
			if !strings.HasPrefix(d.Path, "/") || paths[d.Path] {
				add("%s%s %q path must be absolute inside the container, and used once", where, list.kind, d.Name)
			}
			paths[d.Path] = true
		}
	}
}

// checkPorts checks "host:container[/udp]" ports, ranges included (6881-6891:6881-6891).
// Homelab's own ports (7000-7999) stay free for the companion and app addresses.
func checkPorts(add func(string, ...any), where string, ports []string) {
	for _, p := range ports {
		g := portPattern.FindStringSubmatch(p)
		if g == nil {
			add("%sport %q must look like 8096:8096 or 6881-6891:6881-6891/udp", where, p)
			continue
		}
		num := func(s, or string) int {
			if s == "" {
				s = or
			}
			n, _ := strconv.Atoi(s)
			return n
		}
		h1, h2, c1, c2 := num(g[1], ""), num(g[2], g[1]), num(g[3], ""), num(g[4], g[3])
		switch {
		case h1 < 1024 || h2 > 65535 || h2 < h1 || c1 < 1 || c2 > 65535 || c2 < c1 || h2-h1 != c2-c1:
			add("%sport %q must use host ports 1024-65535, with ranges of the same length", where, p)
		case h1 <= 7999 && h2 >= 7000:
			add("%sport %q: host ports 7000-7999 are kept for Homelab", where, p)
		}
	}
}

// TagFor is the image tag for a commit.
func (m *Manifest) TagFor(commit string) string {
	return strings.ReplaceAll(m.Tag, "{commit}", commit)
}

// VarsFor merges the shared settings with one environment's overrides.
func (m *Manifest) VarsFor(env string) map[string]string {
	out := map[string]string{}
	for k, v := range m.Env {
		out[k] = v
	}
	for k, v := range m.EnvFor[env] {
		out[k] = v
	}
	return out
}

// AddressName is the address of an environment: "movies" for prod, "movies-dev" for dev (D6).
func AddressName(app, env string) string {
	if env == core.Prod {
		return app
	}
	return app + "-" + env
}
