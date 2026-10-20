package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/core"
)

// A device on the LAN can send Host: localhost, but it is not on this computer.
func TestLANDeviceCannotUseThisComputerEndpoints(t *testing.T) {
	r := newRig(t)
	if err := r.store.DeletePerson(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "localhost:7080"
		req.RemoteAddr = "192.168.1.66:5555"
		req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Network: true}))
		req.Header.Set("X-Homelab-Request", "1")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/setup", `{"name":"Eve","email":"eve@example.test"}`},
		{"POST", "/api/setup/passkey-link", `{}`},
		{"POST", "/api/recover", `{"code":"x"}`},
		{"POST", "/api/invites/abc/redeem", `{}`},
		{"GET", "/recover/abc", ``},
		{"GET", "/auth/start", ``},
		{"GET", "/auth/callback?state=x", ``},
		{"GET", "/auth/app?app=a&env=prod&return=http://x/", ``},
	} {
		if rec := send(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s from the LAN = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if ok, _ := r.store.OwnerExists(context.Background()); ok {
		t.Error("a LAN device created the owner")
	}
}

// An admin who sends an id, identityId or status must not overwrite anyone.
func TestInviteIgnoresIDFromClient(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.store.SavePerson(ctx, core.Person{ID: "adm", Name: "Adm", Status: "signed_in", Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	h := r.handler()
	adm := &core.Viewer{Local: true, PersonID: "adm"}
	rec := post(h, "/api/people", `{"id":"owner","name":"Pwned","roles":["member"]}`, adm)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invite with id: %d %s", rec.Code, rec.Body)
	}
	if p, err := r.store.Person(ctx, "owner"); err != nil || !hasRole(p, "owner") {
		t.Fatalf("owner changed: %+v err=%v", p, err)
	}
	rec = post(h, "/api/people", `{"name":"Sam","email":"sam@example.test","roles":["member"]}`, adm)
	var inv struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); rec.Code != http.StatusCreated || err != nil || inv.ID == "" || inv.ID == "owner" {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	// An update can't set identityId or status either.
	rec = do(h, "PUT", "/api/people/"+inv.ID, `{"name":"Sam","roles":["member"],"identityId":"other","status":"signed_in"}`, adm, "X-Homelab-Request", "1", "Content-Type", "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update with identityId/status: %d %s", rec.Code, rec.Body)
	}
}

// Another device's wrong guesses must not stop the owner's own phone.
func TestPasswordLimitIsPerDevice(t *testing.T) {
	r := newRig(t)
	file := filepath.Join(t.TempDir(), "pw")
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	_ = os.WriteFile(file, hash, 0o600)
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, PasswordFile: file})
	try := func(ip, pw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/password-sign-in", strings.NewReader(`{"password":"`+pw+`"}`))
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("X-Homelab-Request", "1")
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Network: true}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < 20; i++ {
		try(fmt.Sprintf("192.168.1.%d", 100+i/5), "guess")
	}
	if rec := try("192.168.1.20", "correct horse"); rec.Code == http.StatusTooManyRequests {
		t.Errorf("owner locked out by other devices")
	}
}

func TestMeLeavesOutTailscaleFlag(t *testing.T) {
	r := newRig(t)
	rec := do(r.handler(), "GET", "/api/me", "", &core.Viewer{Network: true, PersonID: "owner"})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "tailscale") {
		t.Errorf("me = %d %s", rec.Code, rec.Body)
	}
}

type endlessBody struct{ n int64 }

func (c *endlessBody) Read(p []byte) (int, error) {
	if c.n >= 300<<20 {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = ' '
	}
	c.n += int64(len(p))
	return len(p), nil
}

// Every endpoint that skips guard has a body limit.
func TestPublicPostsLimitTheBody(t *testing.T) {
	r := newRig(t)
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, PasswordFile: "/nonexistent"})
	for _, path := range []string{"/api/password-sign-in", "/api/invites/abc/redeem", "/api/recover", "/api/setup", "/api/setup/passkey-link", "/api/logout"} {
		body := &endlessBody{}
		req := httptest.NewRequest("POST", path, body)
		req.Host = "localhost:7080"
		req.Header.Set("X-Homelab-Request", "1")
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(core.WithViewer(req.Context(), core.Viewer{Local: true, Network: path == "/api/password-sign-in"}))
		h.ServeHTTP(httptest.NewRecorder(), req)
		if body.n > 256<<10 {
			t.Errorf("%s read %d KB of body", path, body.n>>10)
		}
	}
}
