package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is config.yaml.
type Config struct {
	Owners    []string `yaml:"owners"`
	Addresses string   `yaml:"addresses"` // local | ip | tailscale
	Tailscale struct {
		Tags []string `yaml:"tags"`
	} `yaml:"tailscale"`
	Listen string `yaml:"listen"`
	// AppAPIPort is the app door's port: where app containers call the Homelab API with
	// their own key (platform services). Default 7085.
	AppAPIPort int `yaml:"app_api_port"`
	// Folders are shared folders on the device that apps may ask for by name, e.g.
	// media: /mnt/storage. Apps see nothing else outside their own data.
	Folders map[string]string `yaml:"folders"`
}

const (
	defaultListen = "127.0.0.1:7080"
	defaultAppAPI = 7085
	// ipListen is where the companion listens with addresses: ip, so other devices reach it.
	ipListen   = "0.0.0.0:7080"
	defaultTag = "tag:homelab"
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

// parseConfig reads config.yaml text strictly (unknown keys are errors) and fills defaults.
func parseConfig(b []byte) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("config.yaml: %w", err)
	}
	if c.Addresses == "" {
		c.Addresses = "local"
	}
	if c.Addresses != "local" && c.Addresses != "ip" && c.Addresses != "tailscale" {
		return c, fmt.Errorf("config.yaml: addresses must be local, ip or tailscale, not %q", c.Addresses)
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
	for name, dir := range c.Folders {
		if !filepath.IsAbs(dir) {
			return c, fmt.Errorf("config.yaml: folder %s must be an absolute path, not %q", name, dir)
		}
	}
	if len(c.Tailscale.Tags) == 0 {
		c.Tailscale.Tags = []string{defaultTag}
	}
	return c, nil
}

func loadConfig(home string) (Config, error) {
	path := filepath.Join(home, "config.yaml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("no config at %s. Run `homelabd init --home %s` first", path, home)
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
	return fmt.Errorf("listen %q is not a loopback address. It must listen on 127.0.0.1 (or ::1); other devices reach Homelab through Tailscale, or set addresses: ip", listen)
}
