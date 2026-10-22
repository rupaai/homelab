package api

import (
	"crypto/rand"
	"encoding/base32"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

func (s *server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	if v, ok := core.ViewerFrom(r.Context()); ok && v.Network {
		// addresses: ip: other devices sign in with the owner's password.
		ready := s.passwordSet()
		if !ready {
			ready, _ = s.store.AnyPassword(r.Context())
		}
		writeJSON(w, http.StatusOK, map[string]any{"needsSetup": false, "signInReady": ready, "password": true})
		return
	}
	if !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, "Open Homelab at http://localhost:7080 on this computer.")
		return
	}
	exists, err := s.store.OwnerExists(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, client, secret := s.oidc.values()
	ownerNeedsPasskey := false
	if exists {
		people, err := s.store.People(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, person := range people {
			if hasRole(person, "owner") && person.Status == "invited" {
				ownerNeedsPasskey = true
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needsSetup": !exists, "signInReady": client != "" && secret != "", "ownerNeedsPasskey": ownerNeedsPasskey})
}

func (s *server) initialOwnerPasskeyLink(w http.ResponseWriter, r *http.Request) {
	if !onThisComputer(r) {
		writeErr(w, http.StatusForbidden, "Open Homelab at http://localhost:7080 on this computer.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	link, err := s.hub.InitialOwnerPasskeyLink(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": link})
}

func (s *server) setupOwner(w http.ResponseWriter, r *http.Request) {
	if !onThisComputer(r) {
		writeErr(w, http.StatusForbidden, "Open Homelab at http://localhost:7080 on this computer.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	code, err := newRecoveryCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, msgInternal)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, msgInternal)
		return
	}
	link, err := s.hub.SetupOwner(r.Context(), core.Person{Name: in.Name, Email: in.Email}, string(hash))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"loginUrl": link, "recoveryCode": code})
}

func newRecoveryCode() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	var groups []string
	for len(encoded) > 5 {
		groups = append(groups, encoded[:5])
		encoded = encoded[5:]
	}
	groups = append(groups, encoded)
	return strings.Join(groups, "-"), nil
}

func (s *server) recoverCode(w http.ResponseWriter, r *http.Request) {
	if !onThisComputer(r) {
		writeErr(w, http.StatusForbidden, "Use the recovery code on the Homelab computer.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	if !s.allowRecoveryAttempt(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "Too many recovery attempts. Wait five minutes and try again.")
		return
	}
	exists, err := s.store.OwnerExists(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		writeErr(w, http.StatusForbidden, "Homelab hasn't been set up yet.")
		return
	}
	people, err := s.store.People(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, p := range people {
		if !hasRole(p, "owner") {
			continue
		}
		hash, used, e := s.store.RecoveryHash(r.Context(), p.ID)
		if e != nil || used || bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(in.Code))) != nil {
			break
		}
		if err := s.store.UseRecovery(r.Context(), p.ID); err != nil {
			break
		}
		if err := s.store.RecordSignIn(r.Context(), p.ID, p.IdentityID); err != nil {
			s.fail(w, r, err)
			return
		}
		token, err := s.issueSession(r.Context(), p.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.store.Log(r.Context(), state.Event{Actor: p.Email, Action: "recovery.code.used", Detail: "Signed in with the recovery code"})
		writeJSON(w, http.StatusOK, map[string]string{"token": token})
		return
	}
	writeErr(w, http.StatusUnauthorized, "That recovery code isn't valid. Check it and try again.")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *server) allowRecoveryAttempt(ip string) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.countAttempt(ip, 5)
}

// countAttempt counts one try for key in its five-minute window; false once max is reached.
// The caller holds authMu.
func (s *server) countAttempt(key string, max int) bool {
	now := time.Now()
	entry := s.recoveryAttempts[key]
	if now.Sub(entry.started) > 5*time.Minute {
		entry = recoveryWindow{started: now}
	}
	if entry.count >= max {
		s.recoveryAttempts[key] = entry
		return false
	}
	entry.count++
	s.recoveryAttempts[key] = entry
	return true
}
