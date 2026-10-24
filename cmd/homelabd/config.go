package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rupaai/homelab/internal/core"
)

// Config is config.yaml.
type Config struct {
	Addresses string `yaml:"addresses"` // local | ip
	// Old keys from when Homelab used Tailscale. They are read and ignored, so an old config still starts.
	Owners    any    `yaml:"owners"`
	Tailscale any    `yaml:"tailscale"`
	Listen    string `yaml:"listen"`
	// AppAPIPort is the app door's port: where app containers call the Homelab API with
	// their own key (platform services). Default 7085.
	AppAPIPort int `yaml:"app_api_port"`
	// ServicesPort is where homelab-services listens, on 127.0.0.1 only (platform
	// services). Default 7086.
	ServicesPort int `yaml:"services_port"`
	// Folders are shared folders on the device that apps may ask for by name, e.g.
	// media: /mnt/storage. Apps see nothing else outside their own data.
	Folders map[string]string `yaml:"folders"`
	// Storage is where Homelab keeps files (platform services), e.g.
	// [{name: main, path: /mnt/storage}]. Default: one disk, main, at <home>/storage.
	Storage []core.Disk `yaml:"storage"`
	// MaxUploadMB is the largest file Homelab accepts, in MB. Default 10240 (10 GB).
	MaxUploadMB int64 `yaml:"max_upload_mb"`
	// Logs is how much of the logs Homelab keeps (platform services).
	Logs LogsConfig `yaml:"logs"`
	// Unknown are top-level keys this version doesn't know, e.g. a setting added by a newer
	// Homelab after an update went back. They are skipped with a warning, not an error, so
	// Homelab still starts.
	Unknown []string `yaml:"-"`
}

// LogsConfig is the logs: part of config.yaml. All of it is optional.
type LogsConfig struct {
	// MinLevel keeps that level and above for Homelab's own services. Default info.
	MinLevel string `yaml:"min_level"`
	// Services sets the level for single services, e.g. {files: debug}.
	Services map[string]string `yaml:"services"`
	// MaxMB is the most disk the logs may take. Default 2048.
	MaxMB int64 `yaml:"max_mb"`
}

// logsJSON checks the logs settings and gives them as the JSON homelab-services reads
// (HOMELAB_SERVICES_LOGS). Nothing set gives "".
func (l LogsConfig) logsJSON() (string, error) {
	if l.MinLevel == "" && len(l.Services) == 0 && l.MaxMB == 0 {
		return "", nil
	}
	out := map[string]any{}
	if l.MinLevel != "" {
		if !core.ValidLogLevel(l.MinLevel) {
			return "", fmt.Errorf("config.yaml: logs: min_level must be one of %s, not %q", strings.Join(core.LogLevels, ", "), l.MinLevel)
		}
		out["min_level"] = l.MinLevel
	}
	if len(l.Services) > 0 {
		for name, lvl := range l.Services {
			if !logServiceName.MatchString(name) {
				return "", fmt.Errorf("config.yaml: logs: services names use lowercase letters, numbers, - and _, not %q", name)
			}
			if !core.ValidLogLevel(lvl) {
				return "", fmt.Errorf("config.yaml: logs: services: %s must be one of %s, not %q", name, strings.Join(core.LogLevels, ", "), lvl)
			}
		}
		out["services"] = l.Services
	}
	if l.MaxMB != 0 {
		if l.MaxMB < 16 || l.MaxMB > 1<<20 {
			return "", fmt.Errorf("config.yaml: logs: max_mb must be between 16 and 1048576, not %d", l.MaxMB)
		}
		out["max_mb"] = l.MaxMB
	}
	b, err := json.Marshal(out)
	return string(b), err
}

var logServiceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

const (
	defaultListen      = "127.0.0.1:7080"
	defaultAppAPI      = 7085
	defaultServices    = 7086
	defaultMaxUploadMB = 10240
	// ipListen is where the companion listens with addresses: ip, so other devices reach it.
	ipListen = "0.0.0.0:7080"
)

// defaultHome is ~/.homelab.
func defaultHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ".homelab"
	}
	return filepath.Join(h, ".homelab")
}

// resolveHome makes --home absolute and expands a leading ~.
func resolveHome(home string) (string, error) {
	if home == "" {
		home = defaultHome()
	}
	if home == "~" || strings.HasPrefix(home, "~/") {
		uh, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(uh, strings.TrimPrefix(home, "~"))
	}
	return filepath.Abs(home)
}

// parseConfig reads config.yaml text and fills defaults. Unknown top-level keys are left
// out and listed in Unknown; inside a known key, unknown fields are still errors.
func parseConfig(b []byte) (Config, error) {
	var c Config
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return c, fmt.Errorf("config.yaml: %w", err)
	}
	unknown := dropUnknownKeys(&doc)
	if len(unknown) > 0 {
		var err error
		if b, err = yaml.Marshal(&doc); err != nil {
			return c, fmt.Errorf("config.yaml: %w", err)
		}
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("config.yaml: %w", err)
	}
	c.Unknown = unknown
	if c.Addresses == "" {
		c.Addresses = "local"
	}
	if c.Addresses == "tailscale" {
		return c, errors.New("config.yaml: addresses: tailscale isn't supported any more. Change it to addresses: ip (other devices use the device's IP address and sign in with the password)")
	}
	if c.Addresses != "local" && c.Addresses != "ip" {
		return c, fmt.Errorf("config.yaml: addresses must be local or ip, not %q", c.Addresses)
	}
	if c.Listen == "" {
		c.Listen = defaultListen
		if c.Addresses == "ip" {
			c.Listen = ipListen
		}
	}
	if c.AppAPIPort == 0 {
		c.AppAPIPort = defaultAppAPI
	}
	if c.AppAPIPort < 1024 || c.AppAPIPort > 65535 {
		return c, fmt.Errorf("config.yaml: app_api_port must be between 1024 and 65535, not %d", c.AppAPIPort)
	}
	if c.ServicesPort == 0 {
		c.ServicesPort = defaultServices
	}
	if c.ServicesPort < 1024 || c.ServicesPort > 65535 || c.ServicesPort == c.AppAPIPort {
		return c, fmt.Errorf("config.yaml: services_port must be between 1024 and 65535 and not app_api_port, not %d", c.ServicesPort)
	}
	for name, dir := range c.Folders {
		if !filepath.IsAbs(dir) {
			return c, fmt.Errorf("config.yaml: folder %s must be an absolute path, not %q", name, dir)
		}
	}
	seen := map[string]bool{}
	for _, d := range c.Storage {
		if !diskName.MatchString(d.Name) || seen[d.Name] {
			return c, fmt.Errorf("config.yaml: storage names use lowercase letters, numbers, - and _, and each is used once, not %q", d.Name)
		}
		seen[d.Name] = true
		if !filepath.IsAbs(d.Path) {
			return c, fmt.Errorf("config.yaml: storage %s must be an absolute path, not %q", d.Name, d.Path)
		}
	}
	if c.MaxUploadMB == 0 {
		c.MaxUploadMB = defaultMaxUploadMB
	}
	if c.MaxUploadMB < 1 || c.MaxUploadMB > 1<<20 {
		return c, fmt.Errorf("config.yaml: max_upload_mb must be between 1 and 1048576, not %d", c.MaxUploadMB)
	}
	if _, err := c.Logs.logsJSON(); err != nil {
		return c, err
	}
	return c, nil
}

// dropUnknownKeys removes the top-level keys Config doesn't have from doc and returns them.
func dropUnknownKeys(doc *yaml.Node) []string {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	known := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			known[name] = true
		}
	}
	m := doc.Content[0]
	var unknown []string
	kept := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !known[m.Content[i].Value] {
			unknown = append(unknown, m.Content[i].Value)
			continue
		}
		kept = append(kept, m.Content[i], m.Content[i+1])
	}
	m.Content = kept
	return unknown
}

var diskName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// storageOrDefault is the configured storage, or main at <home>/storage.
func storageOrDefault(c Config, home string) []core.Disk {
	if len(c.Storage) > 0 {
		return c.Storage
	}
	return []core.Disk{{Name: "main", Path: filepath.Join(home, "storage")}}
}

func loadConfig(home string) (Config, error) {
	path := filepath.Join(home, "config.yaml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("no config at %s. Make an empty file there (it means addresses: local), or write addresses: ip", path)
	}
	if err != nil {
		return Config{}, err
	}
	return parseConfig(b)
}

// checkListen refuses a listen address that other computers could reach, unless addresses
// is ip: then other devices reach the companion directly and sign in with the password.
func checkListen(cfg Config) error {
	if cfg.Addresses == "ip" {
		if _, port, err := net.SplitHostPort(cfg.Listen); err != nil || port == "" {
			return fmt.Errorf("listen %q should look like 0.0.0.0:7080", cfg.Listen)
		}
		return nil
	}
	return checkLoopback(cfg.Listen)
}

// checkLoopback refuses a listen address that other computers could reach.
func checkLoopback(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen %q should look like 127.0.0.1:7080", listen)
	}
	if port == "" {
		return fmt.Errorf("listen %q needs a port", listen)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("listen %q is not a loopback address. It must listen on 127.0.0.1 (or ::1); to reach Homelab from other devices set addresses: ip", listen)
}
