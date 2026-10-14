package main

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestParseConfigDefaults(t *testing.T) {
	for _, in := range []string{"", "# only a comment\n"} {
		c, err := parseConfig([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if c.Addresses != "local" || c.Listen != "127.0.0.1:7080" || !reflect.DeepEqual(c.Tailscale.Tags, []string{"tag:homehub"}) || len(c.Owners) != 0 {
			t.Fatalf("defaults: %+v", c)
		}
	}
}

func TestParseConfigFull(t *testing.T) {
	c, err := parseConfig([]byte("owners: [a@b.c]\naddresses: tailscale\ntailscale:\n  tags: [tag:x]\nlisten: 127.0.0.1:9000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addresses != "tailscale" || c.Listen != "127.0.0.1:9000" || c.Owners[0] != "a@b.c" || c.Tailscale.Tags[0] != "tag:x" {
		t.Fatalf("%+v", c)
	}
}

func TestParseConfigStrict(t *testing.T) {
	for _, in := range []string{"adresses: local\n", "addresses: wifi\n", "listen: [1\n", "owners: nope: x\n"} {
		if _, err := parseConfig([]byte(in)); err == nil {
			t.Errorf("%q should be refused", in)
		}
	}
}

func TestTemplateConfigParses(t *testing.T) {
	c, err := parseConfig([]byte(configTemplate))
	if err != nil || c.Addresses != "local" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestCheckLoopback(t *testing.T) {
	ok := []string{"127.0.0.1:7080", "localhost:7080", "[::1]:7080", "127.0.0.2:1"}
	bad := []string{"0.0.0.0:7080", ":7080", "192.168.1.5:7080", "[::]:7080", "example.com:80", "7080", "100.64.0.1:7080"}
	for _, a := range ok {
		if err := checkLoopback(a); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	for _, a := range bad {
		if err := checkLoopback(a); err == nil {
			t.Errorf("%s should be refused", a)
		}
	}
}

func TestOnThisComputer(t *testing.T) {
	var got core.Viewer
	h := onThisComputer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, _ = core.ViewerFrom(r.Context()) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !got.Local {
		t.Fatal("not local")
	}
}

func TestLaunchdPlist(t *testing.T) {
	out := launchdPlist(plistParams{Exe: "/opt/h&h/homehubd", Home: "/Users/l/.homehub", UserDir: "/Users/l"})
	if err := xml.NewDecoder(strings.NewReader(out)).Decode(new(any)); err != nil {
		t.Fatalf("not xml: %v", err)
	}
	for _, want := range []string{
		"<string>net.homehub.homehubd</string>",
		"<string>/opt/h&amp;h/homehubd</string>",
		"<string>run</string>", "<string>--home</string>", "<string>/Users/l/.homehub</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>",
		"<string>/Users/l/.homehub/logs/homehubd.log</string>",
		"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/Users/l/.orbstack/bin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "homehubd.log") != 2 {
		t.Error("stdout and stderr should both be logged")
	}
}

func TestInitDoesNotOverwrite(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hh")
	var out bytes.Buffer
	if err := initHome(home, &out); err != nil {
		t.Fatal(err)
	}
	mode := func(p string) os.FileMode {
		st, err := os.Stat(filepath.Join(home, p))
		if err != nil {
			t.Fatal(err)
		}
		return st.Mode().Perm()
	}
	if mode(".") != 0o700 || mode("secrets") != 0o700 || mode("secrets/platform.env") != 0o600 {
		t.Errorf("modes: %v %v %v", mode("."), mode("secrets"), mode("secrets/platform.env"))
	}
	b, _ := os.ReadFile(filepath.Join(home, "secrets", "platform.env"))
	if !strings.Contains(string(b), "# GITHUB_TOKEN=") || !strings.Contains(string(b), "# TS_AUTHKEY=") {
		t.Errorf("placeholders: %s", b)
	}
	if _, err := loadConfig(home); err != nil {
		t.Fatalf("starter config: %v", err)
	}

	// Edit both files, run init again: nothing changes.
	cfg := filepath.Join(home, "config.yaml")
	sec := filepath.Join(home, "secrets", "platform.env")
	_ = os.WriteFile(cfg, []byte("addresses: tailscale\n"), 0o644)
	_ = os.WriteFile(sec, []byte("GITHUB_TOKEN=abc\n"), 0o600)
	out.Reset()
	if err := initHome(home, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "addresses: tailscale\n" {
		t.Errorf("config overwritten: %s", b)
	}
	if b, _ := os.ReadFile(sec); string(b) != "GITHUB_TOKEN=abc\n" {
		t.Errorf("secrets overwritten: %s", b)
	}
	if strings.Contains(out.String(), "created") {
		t.Errorf("second run created things: %s", out.String())
	}
}
