package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveRoundTrip(t *testing.T) {
	home := t.TempDir()
	files := map[string]string{
		"config.yaml":                        "addresses: local\n",
		"homelab.db":                         "db",
		"secrets/master.key":                 "key",
		"apps/movies/prod/data/movies.db":    "movies",
		"tailscale/homelab/tailscaled.state": "node",
		"mirrors/x.git/HEAD":                 "ref",
		"logs/homelabd.log":                  "log",
		"secrets/platform.env.imported":      "old",
		"MOVED":                              "moved",
	}
	for name, content := range files {
		p := filepath.Join(home, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(content), 0o600)
	}
	dest := filepath.Join(t.TempDir(), "move.tar.gz")
	n, err := writeArchive(home, dest)
	if err != nil || n != 4 {
		t.Fatalf("wrote %d files, %v", n, err)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o600 {
		t.Fatalf("move file mode %v", st.Mode().Perm())
	}
	if _, err := writeArchive(home, dest); err == nil {
		t.Fatal("overwrote an existing move file")
	}
	to := t.TempDir()
	if n, err := extractArchive(dest, to); err != nil || n != 4 {
		t.Fatalf("extracted %d, %v", n, err)
	}
	for _, name := range []string{"config.yaml", "homelab.db", "secrets/master.key", "apps/movies/prod/data/movies.db"} {
		b, err := os.ReadFile(filepath.Join(to, name))
		if err != nil || string(b) != files[name] {
			t.Errorf("%s = %q, %v", name, b, err)
		}
	}
	for _, name := range []string{"tailscale", "mirrors", "logs", "MOVED", "secrets/platform.env.imported"} {
		if _, err := os.Stat(filepath.Join(to, name)); err == nil {
			t.Errorf("%s was moved", name)
		}
	}
}

func TestExtractRefusesUnsafePaths(t *testing.T) {
	for _, name := range []string{"../evil", "/etc/evil", "a/../../evil"} {
		dest := filepath.Join(t.TempDir(), "bad.tar.gz")
		f, _ := os.Create(dest)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
		tw.Close()
		gz.Close()
		f.Close()
		if _, err := extractArchive(dest, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func TestMovedBlocksRun(t *testing.T) {
	home := t.TempDir()
	if err := checkNotMoved(home); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, movedFile), []byte("Homelab moved to another computer.\n"), 0o644)
	if err := checkNotMoved(home); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("got %v", err)
	}
}

func TestUseTailscaleAddresses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("owners: [a@b.c]\naddresses: local\nlisten: 127.0.0.1:7080\n"), 0o600)
	if err := useTailscaleAddresses(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	cfg, err := parseConfig(b)
	if err != nil || cfg.Addresses != "tailscale" || cfg.Owners[0] != "a@b.c" || cfg.Listen != "127.0.0.1:7080" {
		t.Fatalf("config = %+v, %v\n%s", cfg, err, b)
	}
}

func TestUnits(t *testing.T) {
	p := unitParams{Home: "/var/lib/homelab", User: "homelab", Helper: "/usr/local/lib/homelab/homelabd"}
	u := homelabUnit(p)
	for _, want := range []string{
		"User=homelab", "ExecStartPre=-/usr/local/lib/homelab/homelabd update-guard --home /var/lib/homelab",
		"ExecStart=/var/lib/homelab/bin/homelabd run --home /var/lib/homelab", "Restart=always",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	if !strings.Contains(reportService(p), "ExecStart=/usr/local/lib/homelab/homelabd system-report") {
		t.Error("report service")
	}
	if _, err := parseConfig([]byte(deviceConfig)); err != nil {
		t.Errorf("device config: %v", err)
	}
}
