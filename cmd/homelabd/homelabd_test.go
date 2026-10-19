package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/adapters/secretstore"
	"github.com/rupaai/homelab/internal/core"
	"golang.org/x/crypto/bcrypt"
)

func TestParseConfigDefaults(t *testing.T) {
	for _, in := range []string{"", "# only a comment\n"} {
		c, err := parseConfig([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if c.Addresses != "local" || c.Listen != "127.0.0.1:7080" || !reflect.DeepEqual(c.Tailscale.Tags, []string{"tag:homelab"}) || len(c.Owners) != 0 {
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
	viewer := func(network bool, remote string) core.Viewer {
		var got core.Viewer
		h := onThisComputer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, _ = core.ViewerFrom(r.Context()) }), network)
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		h.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}
	if v := viewer(false, "192.168.1.9:5000"); !v.Local || v.Network {
		t.Fatalf("loopback-only listener = %+v", v)
	}
	if v := viewer(true, "127.0.0.1:5000"); !v.Local || v.Network {
		t.Fatalf("ip mode from this computer = %+v", v)
	}
	if v := viewer(true, "192.168.1.9:5000"); v.Local || !v.Network {
		t.Fatalf("ip mode from another device = %+v", v)
	}
}

func TestListenWithIPAddresses(t *testing.T) {
	c, err := parseConfig([]byte("addresses: ip\n"))
	if err != nil || c.Listen != "0.0.0.0:7080" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	if err := checkListen(c); err != nil {
		t.Fatal(err)
	}
	c, _ = parseConfig([]byte("addresses: local\nlisten: 0.0.0.0:7080\n"))
	if err := checkListen(c); err == nil {
		t.Fatal("local addresses accepted a non-loopback listen")
	}
}

func TestPasswordCommand(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("addresses: ip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdPassword([]string{"--home", home}, strings.NewReader("short\nshort\n"), io.Discard); err == nil {
		t.Fatal("a short password was accepted")
	}
	if err := cmdPassword([]string{"--home", home}, strings.NewReader("long enough pw\nother one here\n"), io.Discard); err == nil {
		t.Fatal("different passwords were accepted")
	}
	if err := cmdPassword([]string{"--home", home}, strings.NewReader("long enough pw\nlong enough pw\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(passwordFile(home))
	if err != nil || bcrypt.CompareHashAndPassword([]byte(strings.TrimSpace(string(b))), []byte("long enough pw")) != nil {
		t.Fatalf("hash = %q, %v", b, err)
	}
	if st, _ := os.Stat(passwordFile(home)); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
}

func TestLaunchdPlist(t *testing.T) {
	out := launchdPlist(plistParams{Exe: "/opt/h&h/homelabd", Home: "/Users/l/.homelab", UserDir: "/Users/l"})
	if err := xml.NewDecoder(strings.NewReader(out)).Decode(new(any)); err != nil {
		t.Fatalf("not xml: %v", err)
	}
	for _, want := range []string{
		"<string>net.homelab.homelabd</string>",
		"<string>/opt/h&amp;h/homelabd</string>",
		"<string>run</string>", "<string>--home</string>", "<string>/Users/l/.homelab</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>",
		"<string>/Users/l/.homelab/logs/homelabd.log</string>",
		"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/Users/l/.orbstack/bin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "homelabd.log") != 2 {
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
	if mode(".") != 0o700 || mode("secrets") != 0o700 {
		t.Errorf("modes: %v %v", mode("."), mode("secrets"))
	}
	if _, err := os.Stat(filepath.Join(home, "secrets", "platform.env")); err == nil {
		t.Error("init made a plain secrets file; secrets are set in the companion now")
	}
	if !strings.Contains(out.String(), "Settings") {
		t.Errorf("next steps don't mention Settings: %s", out.String())
	}
	if _, err := loadConfig(home); err != nil {
		t.Fatalf("starter config: %v", err)
	}

	// Edit the config, run init again: nothing changes.
	cfg := filepath.Join(home, "config.yaml")
	_ = os.WriteFile(cfg, []byte("addresses: tailscale\n"), 0o644)
	out.Reset()
	if err := initHome(home, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "addresses: tailscale\n" {
		t.Errorf("config overwritten: %s", b)
	}
	if strings.Contains(out.String(), "created") {
		t.Errorf("second run created things: %s", out.String())
	}
}

func TestImportSecretFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "platform.env"), []byte("GITHUB_TOKEN=from-file\nTS_AUTHKEY=ts\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "movies.prod.env"), []byte("TMDB_API_KEY='k p'\n"), 0o600)
	s, err := secretstore.Open(filepath.Join(dir, "secrets.db"), filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Set("", "", "GITHUB_TOKEN", "already-set", "owner") // the store wins
	var out bytes.Buffer
	if err := importSecretFiles(dir, s, &out); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Platform("GITHUB_TOKEN"); v != "already-set" {
		t.Errorf("GITHUB_TOKEN = %q", v)
	}
	if v, _ := s.Platform("TS_AUTHKEY"); v != "ts" {
		t.Errorf("TS_AUTHKEY = %q", v)
	}
	if v, _ := s.Inject("movies", "prod", []string{"TMDB_API_KEY"}); v["TMDB_API_KEY"] != "k p" {
		t.Errorf("TMDB_API_KEY = %q", v)
	}
	for _, f := range []string{"platform.env", "movies.prod.env"} {
		if _, err := os.Stat(filepath.Join(dir, f+".imported")); err != nil {
			t.Errorf("%s not renamed: %v", f, err)
		}
	}
	if !strings.Contains(out.String(), "rm ") {
		t.Errorf("no delete command: %s", out.String())
	}
	// Running again (next start) does nothing.
	out.Reset()
	if err := importSecretFiles(dir, s, &out); err != nil || out.Len() != 0 {
		t.Fatalf("second import: %v %q", err, out.String())
	}
	// The identity key is made once and kept.
	a, err := identitySigner(s)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := identitySigner(s)
	if a.PublicKey() != b.PublicKey() {
		t.Error("identity key changed between starts")
	}
}
