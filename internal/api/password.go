package api

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// passwordSet says whether the owner has set a password for other devices.
func (s *server) passwordSet() bool {
	_, err := s.passwordHash()
	return err == nil
}

func (s *server) passwordHash() ([]byte, error) {
	if s.passwordFile == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(s.passwordFile)
	if err != nil {
		return nil, err
	}
	h := []byte(strings.TrimSpace(string(b)))
	if len(h) == 0 {
		return nil, os.ErrNotExist
	}
	return h, nil
}

// passwordSignIn signs the owner in from another device (addresses: ip). Passkeys can't work
// on a bare IP address, so the owner sets a password on the device with `homelabd password`.
func (s *server) passwordSignIn(w http.ResponseWriter, r *http.Request) {
	if v, _ := core.ViewerFrom(r.Context()); !v.Network {
		writeErr(w, http.StatusForbidden, "Password sign-in is only for other devices when Homelab uses addresses: ip.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	// Per device only: a shared limit would let other devices lock the owner out.
	if !s.allowRecoveryAttempt("password:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "Too many tries. Wait five minutes and try again.")
		return
	}
	if strings.TrimSpace(in.Email) != "" {
		s.emailSignIn(w, r, in.Email, in.Password)
		return
	}
	hash, err := s.passwordHash()
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusForbidden, "No password is set yet. Set one on the device with `homelabd password`.")
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "That password isn't right.")
		return
	}
	owner, ok, err := s.owner(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "Homelab hasn't been set up yet.")
		return
	}
	token, err := s.issueSession(r.Context(), owner.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.store.Log(r.Context(), state.Event{Actor: owner.Email, Action: "password.sign-in", Detail: "Signed in with the password from " + clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// emailSignIn signs in a person by their email and own password. The owner may also use the
// password from `homelabd password`. Every failure reads the same.
func (s *server) emailSignIn(w http.ResponseWriter, r *http.Request, email, password string) {
	fallback, err := s.passwordHash()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.fail(w, r, err)
		return
	}
	p, ok := s.hub.CheckPersonPassword(r.Context(), email, password, fallback)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "That email or password isn't right.")
		return
	}
	token, err := s.issueSession(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.store.Log(r.Context(), state.Event{Actor: p.Email, Action: "password.sign-in", Detail: "Signed in with a password from " + clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// owner is the household owner who is signed in (not still invited).
func (s *server) owner(r *http.Request) (core.Person, bool, error) {
	people, err := s.store.People(r.Context())
	if err != nil {
		return core.Person{}, false, err
	}
	for _, p := range people {
		if hasRole(p, "owner") && p.Status == "signed_in" {
			return p, true, nil
		}
	}
	return core.Person{}, false, nil
}
