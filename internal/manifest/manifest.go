// Package manifest reads an app's homelab.yaml: the small contract between an app and
// Homelab (see blocks/apps_decisions.md). Fields Homelab doesn't use yet are accepted and
// ignored, so an app can carry its full manifest from day one.
package manifest

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

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

	// Accepted for later milestones, unused in milestone 1.
	VisibleTo []string `yaml:"visible_to"`
	Auth      string   `yaml:"auth"`
	SmokeTest string   `yaml:"smoke_test"`
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

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	varPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memPattern  = regexp.MustCompile(`^[0-9]+[kmg]$`)
)

// Parse reads and validates a manifest, filling in defaults.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
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
		add("id %q must be 2-32 lowercase letters, digits or dashes, starting with a letter", m.ID)
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
	case core.PromotionManual, core.PromotionAutoDev:
	case core.PromotionAuto:
		add("promotion \"auto\" arrives in milestone 2; use auto-dev or manual for now")
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
	seenData := map[string]bool{}
	for _, d := range m.Data {
		if !namePattern.MatchString(d.Name) || seenData[d.Name] {
			add("data name %q must be unique, lowercase letters, digits, - or _", d.Name)
		}
		seenData[d.Name] = true
		if !strings.HasPrefix(d.Path, "/") {
			add("data %q path must be absolute inside the container", d.Name)
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
		if _, clash := m.Env[s]; clash {
			add("%q is both a setting and a secret", s)
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
