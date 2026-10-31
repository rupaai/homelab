// People, invites, roles and password-link handlers.

package api

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
	"github.com/rupaai/homelab/internal/state"
)

func (s *server) people(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.People(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// personInput is what a client may set on a person. The id, identity id and status are
// never taken from the client.
type personInput struct {
	Name   string            `json:"name"`
	Email  string            `json:"email"`
	Roles  []string          `json:"roles"`
	Grants map[string]string `json:"grants"`
}

func (in personInput) person(id string) core.Person {
	return core.Person{ID: id, Name: in.Name, Email: in.Email, Roles: in.Roles, Grants: in.Grants}
}

func (s *server) invitePerson(w http.ResponseWriter, r *http.Request) {
	var in personInput
	if !decode(w, r, &in, true) {
		return
	}
	base, ok := baseURL(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "Homelab couldn't tell what address you're using.")
		return
	}
	inv, err := s.hub.InvitePerson(actionCtx(r), in.person(""), base)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (s *server) updatePerson(w http.ResponseWriter, r *http.Request) {
	var in personInput
	if !decode(w, r, &in, true) {
		return
	}
	p := in.person(r.PathValue("id"))
	if err := s.hub.UpdatePerson(actionCtx(r), p); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) removePerson(w http.ResponseWriter, r *http.Request) {
	steps, err := s.hub.RemovePerson(actionCtx(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, steps)
}

func (s *server) resendInvite(w http.ResponseWriter, r *http.Request) {
	base, ok := baseURL(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "Homelab couldn't tell what address you're using.")
		return
	}
	inv, err := s.hub.ResendInvite(actionCtx(r), r.PathValue("id"), base)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// baseURL is the address the person making this request used to reach Homelab, for links
// they will send to someone else. Only the owner or an admin gets here, and the Host header
// must be a plain host or host:port.
func baseURL(r *http.Request) (string, bool) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	u, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || r.Host == "" || u.Host != r.Host || u.User != nil || u.Hostname() == "" {
		return "", false
	}
	return scheme + "://" + r.Host, true
}

// invitePassword lets an invited person choose a password when they open their invite on
// another device (addresses: ip), where a passkey can't be made.
func (s *server) invitePassword(w http.ResponseWriter, r *http.Request) {
	if v, _ := core.ViewerFrom(r.Context()); !v.Network {
		writeErr(w, http.StatusForbidden, "Choosing a password is only for other devices. On the Homelab computer, set up a passkey.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	// Same per-device limit as password sign-in.
	if !s.allowRecoveryAttempt("password:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "Too many tries. Wait five minutes and try again.")
		return
	}
	p, err := s.hub.SetInvitePassword(r.Context(), r.PathValue("token"), in.Password)
	if err != nil {
		var ue *hub.UserError
		if errors.As(err, &ue) {
			status := http.StatusBadRequest
			if strings.HasPrefix(ue.Msg, "This invite") {
				status = http.StatusGone
			}
			writeErr(w, status, ue.Msg)
			return
		}
		s.fail(w, r, err)
		return
	}
	token, err := s.issueSession(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.store.Log(r.Context(), state.Event{Actor: p.Email, Action: "password.sign-in", Detail: "Joined with a password from " + clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *server) testEmail(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.SendTestEmail(actionCtx(r), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) redeemInvite(w http.ResponseWriter, r *http.Request) {
	if !onThisComputer(r) {
		writeErr(w, http.StatusForbidden, "Open this invite on the Homelab computer.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	var body struct{}
	if !decode(w, r, &body, true) {
		return
	}
	link, err := s.hub.RedeemInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		writeErr(w, http.StatusGone, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": link})
}

func (s *server) recoverLink(w http.ResponseWriter, r *http.Request) {
	if !onThisComputer(r) {
		http.Error(w, "Open this recovery link on the Homelab computer.", http.StatusForbidden)
		return
	}
	value := r.PathValue("token")
	digest := sha256.Sum256([]byte(value))
	id, err := s.store.ConsumeOneTimeCode(r.Context(), digest[:], "recovery-link")
	if err != nil {
		http.Error(w, "This recovery link has expired or was already used. Run homelabd recover again.", http.StatusGone)
		return
	}
	p, err := s.store.Person(r.Context(), id)
	if err != nil || !hasRole(p, "owner") {
		http.Error(w, "This recovery link is no longer valid.", http.StatusGone)
		return
	}
	token, err := s.issueSession(r.Context(), id)
	if err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	_ = s.store.Log(r.Context(), state.Event{Actor: p.Email, Action: "recovery.link.used", Detail: "Signed in with a one-time recovery link"})
	http.Redirect(w, r, "/"+sessionFragment+token, http.StatusSeeOther)
}

func hasRole(p core.Person, role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}
