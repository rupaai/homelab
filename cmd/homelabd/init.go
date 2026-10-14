package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const configTemplate = `# Homelab settings. See docs/milestone1.md.

# Who may use the companion over Tailscale (their Tailscale login names).
# On this computer itself (127.0.0.1) the companion is always open.
# owners: [you@example.com]

# local: addresses are http://127.0.0.1:<port>, for trying things out.
# tailscale: each environment gets its own Tailscale address (needs TS_AUTHKEY).
addresses: local

tailscale:
  tags: [tag:homelab]

# The companion on this computer.
listen: 127.0.0.1:7080
`

const platformEnvTemplate = `# Platform secrets for Homelab. Keep this file private (mode 0600).
# Plain KEY=VALUE lines. Remove the leading # to use one.

# GitHub: lets Homelab read your repos, build status and ghcr.io images.
# Create a fine-grained token (repo contents and Actions read) or a classic token
# with read:packages and repo, at https://github.com/settings/tokens
# GITHUB_TOKEN=

# Tailscale: only for addresses: tailscale. In the admin console go to
# Settings > Keys > Generate auth key. Make it reusable and tagged tag:homelab.
# TS_AUTHKEY=
`

func cmdInit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	homeFlag := fs.String("home", "", "Homelab folder (default ~/.homelab)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	return initHome(home, out)
}

// initHome creates the folder, config and secrets placeholders. It never overwrites.
func initHome(home string, out io.Writer) error {
	var made, kept []string
	dir := func(p string) error {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			kept = append(kept, p)
			return nil
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
		made = append(made, p)
		return nil
	}
	file := func(p, content string, mode os.FileMode) error {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if os.IsExist(err) {
			kept = append(kept, p)
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := f.WriteString(content); err != nil {
			f.Close()
			return err
		}
		made = append(made, p)
		return f.Close()
	}
	secrets := filepath.Join(home, "secrets")
	steps := []func() error{
		func() error { return dir(home) },
		func() error { return file(filepath.Join(home, "config.yaml"), configTemplate, 0o644) },
		func() error { return dir(secrets) },
		func() error { return file(filepath.Join(secrets, "platform.env"), platformEnvTemplate, 0o600) },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	for _, p := range made {
		fmt.Fprintln(out, "created", p)
	}
	for _, p := range kept {
		fmt.Fprintln(out, "kept   ", p, "(already there)")
	}
	fmt.Fprintf(out, `
Next:
  1. Open %s and add GITHUB_TOKEN (see the comments there).
  2. Optionally edit %s.
  3. Start Homelab:  homelabd run --home %s
  4. To start it at login on a Mac:  homelabd service install --home %s
`, filepath.Join(secrets, "platform.env"), filepath.Join(home, "config.yaml"), home, home)
	return nil
}
