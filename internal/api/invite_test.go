package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/core"
)

// postAt is post with a chosen Host header.
func postAt(h http.Handler, host, path, body string, v *core.Viewer) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("X-Homelab-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	if v != nil {
		req = req.WithContext(core.WithViewer(req.Context(), *v))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// networkOwner is the owner signed in from another device.
var networkOwner = core.Viewer{Network: true, PersonID: "owner", Session: true, Name: "Owner"}

const invitePerson = `{"name":"Sam","email":"sam@example.test","roles":["member"]}`

// invite makes an invitation over the API and returns its token and response body.
func invite(t *testing.T, h http.Handler, host string) (token, body string) {
	t.Helper()
	rec := postAt(h, host, "/api/people", invitePerson, &networkOwner)
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	body = rec.Body.String()
	i := strings.Index(body, "/invite/")
	if i < 0 {
		t.Fatalf("no invite link in %s", body)
	}
	rest := body[i+len("/invite/"):]
	return rest[:strings.IndexByte(rest, '"')], body
}

func TestInviteLinkUsesRequestHost(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	_, body := invite(t, h, "192.168.1.183:7080")
	if !strings.Contains(body, `"url":"http://192.168.1.183:7080/invite/`) || strings.Contains(body, "localhost") {
		t.Fatalf("link doesn't use the request host: %s", body)
	}
	rec := postAt(h, "localhost:7080", "/api/people", `{"name":"Kim","roles":["member"]}`, &core.Viewer{Local: true, PersonID: "owner", Session: true})
	if !strings.Contains(rec.Body.String(), `"url":"http://localhost:7080/invite/`) {
		t.Fatalf("localhost link: %d %s", rec.Code, rec.Body)
	}
	people, _ := r.h.People(t.Context())
	var id string
	for _, p := range people {
		if p.Name == "Sam" {
			id = p.ID
		}
	}
	rec = postAt(h, "192.168.1.183:7080", "/api/people/"+id+"/invite", ``, &networkOwner)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"url":"http://192.168.1.183:7080/invite/`) {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body)
	}
	if rec := postAt(h, "evil.test/x@y", "/api/people", invitePerson, &networkOwner); rec.Code != 400 {
		t.Fatalf("odd host = %d", rec.Code)
	}
}

func TestInviteEmailedAndFailureDoesNotFailInvite(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	token, body := invite(t, h, "192.168.1.183:7080")
	if !strings.Contains(body, `"emailed":true`) || len(r.mail.Sent) != 1 {
		t.Fatalf("not emailed: %s %+v", body, r.mail.Sent)
	}
	m := r.mail.Sent[0]
	if m.To != "sam@example.test" || m.Subject != "You're invited to Homelab" ||
		!strings.Contains(m.Body, "Owner invited you") || !strings.Contains(m.Body, "http://192.168.1.183:7080/invite/"+token) || !strings.Contains(m.Body, "7 days") {
		t.Fatalf("email = %+v", m)
	}

	r.mail.Err = errors.New("Gmail didn't accept the address and app password. Check them in Settings.")
	rec := postAt(h, "192.168.1.183:7080", "/api/people", `{"name":"Kim","email":"kim@example.test","roles":["member"]}`, &networkOwner)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"emailed":false`) ||
		!strings.Contains(rec.Body.String(), `"emailError":"Gmail didn't accept`) || !strings.Contains(rec.Body.String(), "/invite/") {
		t.Fatalf("failed email: %d %s", rec.Code, rec.Body)
	}

	r.mail.Err, r.mail.Off = nil, true
	rec = postAt(h, "192.168.1.183:7080", "/api/people", `{"name":"Lee","email":"lee@example.test","roles":["member"]}`, &networkOwner)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "Email isn't set up, so send the link yourself.") {
		t.Fatalf("email off: %d %s", rec.Code, rec.Body)
	}
}

func TestTestEmailGoesToTheSignedInPerson(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	if rec := postAt(h, "localhost:7080", "/api/email/test", ``, &local); rec.Code != 204 || len(r.mail.Sent) != 1 || r.mail.Sent[0].To != "owner@example.test" {
		t.Fatalf("test email: %d %+v", rec.Code, r.mail.Sent)
	}
	r.mail.Off = true
	if rec := postAt(h, "localhost:7080", "/api/email/test", ``, &local); rec.Code != 400 || !strings.Contains(rec.Body.String(), "Email isn't set up") {
		t.Fatalf("test email off: %d %s", rec.Code, rec.Body)
	}
	if rec := postAt(h, "localhost:7080", "/api/email/test", ``, &core.Viewer{Local: true}); rec.Code != 403 {
		t.Fatalf("test email without a sign-in = %d", rec.Code)
	}
}

func TestNetworkInviteChoosePassword(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	token, _ := invite(t, h, "192.168.1.183:7080")
	path := "/api/invites/" + token + "/password"
	net := &core.Viewer{Network: true}

	if rec := postAt(h, "192.168.1.183:7080", path, `{"password":"short"}`, net); rec.Code != 400 {
		t.Fatalf("short password = %d %s", rec.Code, rec.Body)
	}
	if rec := postAt(h, "192.168.1.183:7080", path, `{"password":"long enough password"}`, &core.Viewer{Local: true}); rec.Code != 403 {
		t.Fatalf("password invite on this computer = %d", rec.Code)
	}
	if rec := do(h, "POST", path, `{"password":"long enough password"}`, net, "Content-Type", "application/json"); rec.Code != 403 {
		t.Fatalf("without the companion header = %d", rec.Code)
	}
	rec := postAt(h, "192.168.1.183:7080", path, `{"password":"long enough password"}`, net)
	if rec.Code != 200 {
		t.Fatalf("choose password = %d %s", rec.Code, rec.Body)
	}
	session := "Bearer " + bodyToken(t, rec)
	if me := do(h, "GET", "/api/me", "", net, "Authorization", session); me.Code != 200 || !strings.Contains(me.Body.String(), "Sam") {
		t.Fatalf("me = %d %s", me.Code, me.Body)
	}
	people, _ := r.h.People(t.Context())
	for _, p := range people {
		if p.Name == "Sam" && p.Status != "signed_in" {
			t.Fatalf("status = %q", p.Status)
		}
	}
	// The link works once.
	if rec := postAt(h, "192.168.1.183:7080", path, `{"password":"another long password"}`, net); rec.Code != http.StatusGone {
		t.Fatalf("reused invite = %d %s", rec.Code, rec.Body)
	}
	if rec := postAt(h, "192.168.1.183:7080", "/api/invites/nope/password", `{"password":"long enough password"}`, net); rec.Code != http.StatusGone {
		t.Fatalf("unknown invite = %d", rec.Code)
	}
}

func TestResentInviteEndsTheOldLink(t *testing.T) {
	r := newRig(t)
	h := r.handler()
	old, _ := invite(t, h, "192.168.1.183:7080")
	people, _ := r.h.People(t.Context())
	var id string
	for _, p := range people {
		if p.Name == "Sam" {
			id = p.ID
		}
	}
	if rec := postAt(h, "192.168.1.183:7080", "/api/people/"+id+"/invite", ``, &networkOwner); rec.Code != 200 {
		t.Fatalf("resend = %d", rec.Code)
	}
	if rec := postAt(h, "192.168.1.183:7080", "/api/invites/"+old+"/password", `{"password":"long enough password"}`, &core.Viewer{Network: true}); rec.Code != http.StatusGone {
		t.Fatalf("old link = %d", rec.Code)
	}
}

func TestPasswordSignInWithEmail(t *testing.T) {
	r := newRig(t)
	file := filepath.Join(t.TempDir(), "companion-password")
	hash, _ := bcrypt.GenerateFromPassword([]byte("owner file password"), bcrypt.MinCost)
	if err := os.WriteFile(file, hash, 0o600); err != nil {
		t.Fatal(err)
	}
	// Each server keeps its own count of tries, so a new one starts the limit over.
	server := func() http.Handler {
		return New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, PasswordFile: file})
	}
	h := server()
	net := &core.Viewer{Network: true}
	token, _ := invite(t, h, "192.168.1.183:7080")
	if rec := postAt(h, "192.168.1.183:7080", "/api/invites/"+token+"/password", `{"password":"sams own password"}`, net); rec.Code != 200 {
		t.Fatalf("join = %d %s", rec.Code, rec.Body)
	}

	if rec := post(h, "/api/password-sign-in", `{"email":"Sam@Example.test","password":"sams own password"}`, net); rec.Code != 200 {
		t.Fatalf("email sign-in = %d %s", rec.Code, rec.Body)
	}
	wrong := post(h, "/api/password-sign-in", `{"email":"sam@example.test","password":"not it at all"}`, net)
	unknown := post(h, "/api/password-sign-in", `{"email":"nobody@example.test","password":"sams own password"}`, net)
	if wrong.Code != 401 || unknown.Code != 401 || wrong.Body.String() != unknown.Body.String() || !strings.Contains(wrong.Body.String(), "That email or password isn't right.") {
		t.Fatalf("wrong=%d %s unknown=%d %s", wrong.Code, wrong.Body, unknown.Code, unknown.Body)
	}
	// Sam's password is not the owner's, and the owner's file password isn't Sam's.
	if rec := post(h, "/api/password-sign-in", `{"email":"sam@example.test","password":"owner file password"}`, net); rec.Code != 401 {
		t.Fatalf("owner password for Sam = %d", rec.Code)
	}
	// The owner still signs in with the file password, with or without an email.
	h = server()
	if rec := post(h, "/api/password-sign-in", `{"password":"owner file password"}`, net); rec.Code != 200 {
		t.Fatalf("owner without email = %d %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/password-sign-in", `{"email":"owner@example.test","password":"owner file password"}`, net); rec.Code != 200 {
		t.Fatalf("owner with email = %d %s", rec.Code, rec.Body)
	}
	// An invited person who hasn't joined can't sign in.
	h = server()
	postAt(h, "192.168.1.183:7080", "/api/people", `{"name":"Kim","email":"kim@example.test","roles":["member"]}`, &networkOwner)
	if rec := post(h, "/api/password-sign-in", `{"email":"kim@example.test","password":"anything at all"}`, net); rec.Code != 401 {
		t.Fatalf("invited person = %d", rec.Code)
	}

	people, _ := r.h.People(t.Context())
	var sam core.Person
	for _, p := range people {
		if p.Name == "Sam" {
			sam = p
		}
	}
	if _, err := r.h.RemovePerson(t.Context(), sam.ID); err != nil {
		t.Fatal(err)
	}
	if rec := post(h, "/api/password-sign-in", `{"email":"sam@example.test","password":"sams own password"}`, net); rec.Code != 401 {
		t.Fatalf("removed person = %d", rec.Code)
	}
	if _, err := r.store.PasswordHash(t.Context(), sam.ID); err == nil {
		t.Fatal("removed person's password hash was kept")
	}
}

func TestBootstrapReadyOnceSomeoneHasAPassword(t *testing.T) {
	r := newRig(t)
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, PasswordFile: filepath.Join(t.TempDir(), "none")})
	net := &core.Viewer{Network: true}
	if rec := do(h, "GET", "/api/bootstrap", "", net); !strings.Contains(rec.Body.String(), `"signInReady":false`) {
		t.Fatalf("before: %s", rec.Body)
	}
	if err := r.store.SavePerson(t.Context(), core.Person{ID: "sam", Name: "Sam", Status: "invited"}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.ActivateWithPassword(t.Context(), "sam", "", "hash"); err != nil {
		t.Fatal(err)
	}
	if rec := do(h, "GET", "/api/bootstrap", "", net); !strings.Contains(rec.Body.String(), `"signInReady":true`) {
		t.Fatalf("after: %s", rec.Body)
	}
}
