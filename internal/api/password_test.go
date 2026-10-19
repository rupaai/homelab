package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/core"
)

func TestPasswordSignIn(t *testing.T) {
	r := newRig(t)
	file := filepath.Join(t.TempDir(), "companion-password")
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, PasswordFile: file})
	network := &core.Viewer{Network: true}
	local := &core.Viewer{Local: true}

	rec := do(h, "GET", "/api/bootstrap", "", network)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"password":true`) || !strings.Contains(rec.Body.String(), `"signInReady":false`) {
		t.Fatalf("bootstrap without a password = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/password-sign-in", `{"password":"anything at all"}`, network); rec.Code != 403 {
		t.Fatalf("sign-in without a password set = %d", rec.Code)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err := os.WriteFile(file, hash, 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := do(h, "GET", "/api/apps", "", network); rec.Code != 403 {
		t.Fatalf("apps before sign-in = %d", rec.Code)
	}
	if rec := post(h, "/api/password-sign-in", `{"password":"correct horse"}`, local); rec.Code != 403 {
		t.Fatalf("password sign-in on this computer = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/password-sign-in", `{"password":"correct horse"}`, network, "Content-Type", "application/json"); rec.Code != 403 {
		t.Fatalf("sign-in without the companion header = %d", rec.Code)
	}
	if rec := post(h, "/api/password-sign-in", `{"password":"wrong"}`, network); rec.Code != 401 {
		t.Fatalf("wrong password = %d", rec.Code)
	}
	rec = post(h, "/api/password-sign-in", `{"password":"correct horse"}`, network)
	if rec.Code != 204 {
		t.Fatalf("sign-in = %d %s", rec.Code, rec.Body)
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "homelab_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie")
	}
	rec = do(h, "GET", "/api/me", "", network, "Cookie", "homelab_session="+session.Value)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "owner") {
		t.Fatalf("me after sign-in = %d %s", rec.Code, rec.Body)
	}
	req := do(h, "GET", "/api/apps", "", network, "Cookie", "homelab_session="+session.Value)
	if req.Code != 200 {
		t.Fatalf("apps after sign-in = %d %s", req.Code, req.Body)
	}
	for i := 0; i < 5; i++ {
		post(h, "/api/password-sign-in", `{"password":"wrong"}`, network)
	}
	if rec := post(h, "/api/password-sign-in", `{"password":"correct horse"}`, network); rec.Code != 429 {
		t.Fatalf("after many tries = %d", rec.Code)
	}
}
