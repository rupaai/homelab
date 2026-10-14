package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const configTemplate = `# Homelab settings. See docs/milestone1.md and docs/milestone2.md.

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
  1. Optionally edit %s.
  2. Start Homelab:  homelabd run --home %s
  3. Open the companion at http://127.0.0.1:7080, go to Settings and set your GitHub token.
     Secrets are kept encrypted in %s; keep a copy of master.key there somewhere safe.
  4. To start it at login on a Mac:  homelabd service install --home %s
`, filepath.Join(home, "config.yaml"), home, secrets, home)
	return nil
}
