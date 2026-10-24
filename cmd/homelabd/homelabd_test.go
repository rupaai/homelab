package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/adapters/services"
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
	for _, in := range []string{"addresses: wifi\n", "listen: [1\n", "owners: nope: x\n", "folders: {media: storage}\n"} {
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

func TestIdentityKeyMadeOnce(t *testing.T) {
	s := fake.NewSecrets()
	a, err := identitySigner(s)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := identitySigner(s)
	if a.PublicKey() != b.PublicKey() {
		t.Error("identity key changed between starts")
	}
}

// brokenSecrets can't read platform values, as when the store has a bad row or a locked file.
type brokenSecrets struct {
	core.Secrets
	set int
}

func (b *brokenSecrets) LoadPlatform(string) (string, error) { return "", errors.New("read failed") }
func (b *brokenSecrets) Set(app, env, name, value, by string) error {
	b.set++
	return nil
}

func TestStartupStopsOnSecretReadError(t *testing.T) {
	b := &brokenSecrets{}
	if _, err := ensurePlatformSecret(b, "X"); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("ensurePlatformSecret error = %v", err)
	}
	if _, err := identitySigner(b); err == nil {
		t.Fatal("identitySigner made a key although the store can't be read")
	}
	if _, _, err := pushKeys(b); err == nil {
		t.Fatal("pushKeys made keys although the store can't be read")
	}
	if b.set != 0 {
		t.Fatalf("a new secret was written %d times; it would replace the real one", b.set)
	}
}

func TestParseConfigAppAPIPort(t *testing.T) {
	if c, err := parseConfig(nil); err != nil || c.AppAPIPort != 7085 {
		t.Fatalf("default: %v %d", err, c.AppAPIPort)
	}
	if c, err := parseConfig([]byte("app_api_port: 9000\n")); err != nil || c.AppAPIPort != 9000 {
		t.Fatalf("9000: %v %d", err, c.AppAPIPort)
	}
	for _, bad := range []string{"app_api_port: 80\n", "app_api_port: 70000\n"} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseConfigServicesPort(t *testing.T) {
	if c, err := parseConfig(nil); err != nil || c.ServicesPort != 7086 {
		t.Fatalf("default: %v %d", err, c.ServicesPort)
	}
	if c, err := parseConfig([]byte("services_port: 9001\n")); err != nil || c.ServicesPort != 9001 {
		t.Fatalf("9001: %v %d", err, c.ServicesPort)
	}
	for _, bad := range []string{"services_port: 80\n", "services_port: 70000\n", "services_port: 7085\n"} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestParseConfigStorage(t *testing.T) {
	c, err := parseConfig([]byte("storage:\n  - {name: main, path: /mnt/storage}\n  - {name: usb, path: /media/usb}\nmax_upload_mb: 500\n"))
	if err != nil || len(c.Storage) != 2 || c.Storage[1].Name != "usb" || c.Storage[1].Path != "/media/usb" || c.MaxUploadMB != 500 {
		t.Fatalf("%+v %v", c, err)
	}
	if d := storageOrDefault(c, "/home/x/.homelab"); len(d) != 2 {
		t.Fatalf("configured storage replaced: %v", d)
	}
	c, _ = parseConfig(nil)
	if d := storageOrDefault(c, "/home/x/.homelab"); len(d) != 1 || d[0].Name != "main" || d[0].Path != "/home/x/.homelab/storage" || c.MaxUploadMB != 10240 {
		t.Fatalf("default: %v %d", d, c.MaxUploadMB)
	}
	for _, bad := range []string{
		"storage: [{name: Main, path: /x}]",
		"storage: [{name: a, path: relative}]",
		"storage: [{name: a, path: /x}, {name: a, path: /y}]",
		"max_upload_mb: -1",
	} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A newer Homelab can add a setting, and then an update can go back to an older version.
// The older version must still start: unknown top-level keys are skipped, not errors.
func TestParseConfigSkipsUnknownKeys(t *testing.T) {
	c, err := parseConfig([]byte("addresses: ip\nsomething_new:\n  - {name: main, path: /mnt/storage}\nfolders: {media: /mnt/storage/media}\nlater: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Unknown, ",") != "something_new,later" {
		t.Fatalf("unknown: %v", c.Unknown)
	}
	if c.Addresses != "ip" || c.Folders["media"] != "/mnt/storage/media" {
		t.Fatalf("known keys lost: %+v", c)
	}
	if c, err := parseConfig([]byte("addresses: ip\n")); err != nil || c.Unknown != nil {
		t.Fatalf("%v %v", c.Unknown, err)
	}
	// A misspelt top-level key is skipped with the same warning, not refused.
	if c, err := parseConfig([]byte("adresses: ip\n")); err != nil || c.Addresses != "local" || len(c.Unknown) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	// Inside a known key a typo is still an error.
	if _, err := parseConfig([]byte("storage: [{name: a, pth: /x}]\n")); err == nil {
		t.Fatal("a typo inside storage was accepted")
	}
}

func TestWaitForSecretsGivesUpWhenTheServiceNeverComes(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing listens here now
	svc, err := services.New(services.Options{Port: port, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = waitForSecrets(context.Background(), svc.Secrets(), svc, 300*time.Millisecond, log)
	if !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("want ErrSecretsUnavailable, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForSecrets(ctx, svc.Secrets(), svc, time.Minute, log); !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped start should say so, got %v", err)
	}
}

func TestParseConfigLogs(t *testing.T) {
	c, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if j, err := c.Logs.logsJSON(); err != nil || j != "" {
		t.Fatalf("default: %q %v", j, err)
	}
	c, err = parseConfig([]byte("logs:\n  min_level: warn\n  services:\n    files: debug\n  max_mb: 512\n"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := c.Logs.logsJSON()
	if err != nil || j != `{"max_mb":512,"min_level":"warn","services":{"files":"debug"}}` {
		t.Fatalf("json: %q %v", j, err)
	}
	for name, doc := range map[string]string{
		"bad level":         "logs:\n  min_level: loud\n",
		"bad service level": "logs:\n  services:\n    files: Debug\n",
		"bad service name":  "logs:\n  services:\n    Files: debug\n",
		"too small":         "logs:\n  max_mb: 1\n",
		"too big":           "logs:\n  max_mb: 99999999\n",
		"unknown key":       "logs:\n  keep_days: 3\n",
	} {
		if _, err := parseConfig([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
