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
	Addresses string `yaml:"addresses"` // local | ip
	// Old keys from when Homelab used Tailscale. They are read and ignored, so an old config still starts.
	Owners    any    `yaml:"owners"`
	Tailscale any    `yaml:"tailscale"`
	Listen    string `yaml:"listen"`
	// Folders are shared folders on the device that apps may ask for by name, e.g.
	// media: /mnt/storage. Apps see nothing else outside their own data.
	Folders map[string]string `yaml:"folders"`
}

const (
	defaultListen = "127.0.0.1:7080"
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
	for name, dir := range c.Folders {
		if !filepath.IsAbs(dir) {
			return c, fmt.Errorf("config.yaml: folder %s must be an absolute path, not %q", name, dir)
		}
	}
	return c, nil
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
