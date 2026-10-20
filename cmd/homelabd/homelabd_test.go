package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		if c.Addresses != "local" || c.Listen != "127.0.0.1:7080" {
			t.Fatalf("defaults: %+v", c)
		}
	}
}

func TestParseConfigFull(t *testing.T) {
	c, err := parseConfig([]byte("addresses: ip\nlisten: 127.0.0.1:9000\nfolders: {media: /mnt/storage}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addresses != "ip" || c.Listen != "127.0.0.1:9000" || c.Folders["media"] != "/mnt/storage" {
		t.Fatalf("%+v", c)
	}
}

func TestParseConfigStrict(t *testing.T) {
	for _, in := range []string{"adresses: local\n", "addresses: wifi\n", "listen: [1\n", "owners: nope: x\n", "folders: {media: storage}\n"} {
		if _, err := parseConfig([]byte(in)); err == nil {
			t.Errorf("%q should be refused", in)
		}
	}
}

func TestDeviceConfigIsIP(t *testing.T) {
	c, err := parseConfig([]byte(deviceConfig))
	if err != nil || c.Addresses != "ip" || c.Listen != "0.0.0.0:7080" {
		t.Fatalf("%v %+v", err, c)
	}
	if err := checkListen(c); err != nil {
		t.Fatalf("homelabd run would refuse to start: %v", err)
	}
	if strings.Contains(deviceConfig, "listen:") {
		t.Error("setup should not pin the listen address")
	}
}

func TestTailscaleAddressesIsRefusedClearly(t *testing.T) {
	_, err := parseConfig([]byte("addresses: tailscale\n"))
	if err == nil || !strings.Contains(err.Error(), "addresses: ip") {
		t.Fatalf("err = %v", err)
	}
}

func TestOldConfigKeysStillParse(t *testing.T) {
	if _, err := parseConfig([]byte("owners: [a@b.c]\ntailscale:\n  tags: [tag:homelab]\n")); err != nil {
		t.Fatal(err)
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
