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
	Addresses string   `yaml:"addresses"` // local | tailscale
	Tailscale struct {
		Tags []string `yaml:"tags"`
	} `yaml:"tailscale"`
	Listen string `yaml:"listen"`
}

const (
	defaultListen = "127.0.0.1:7080"
	defaultTag    = "tag:homelab"
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
	if c.Addresses != "local" && c.Addresses != "tailscale" {
		return c, fmt.Errorf("config.yaml: addresses must be local or tailscale, not %q", c.Addresses)
	}
	if c.Listen == "" {
		c.Listen = defaultListen
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

// checkLoopback refuses a listen address that other computers could reach: the local
// listener treats everyone as the owner.
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
	return fmt.Errorf("listen %q is not a loopback address. The companion on this computer is open to everyone who can reach it, so it must listen on 127.0.0.1 (or ::1); other devices reach Homelab through Tailscale", listen)
}
