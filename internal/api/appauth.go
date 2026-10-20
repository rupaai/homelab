package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// appAuth starts the hand-off for an app: the person signs in with their passkey, and the
// callback then calls appHandoff. The browser has no companion cookie to check here.
func (s *server) appAuth(w http.ResponseWriter, r *http.Request) {
	if !onThisComputer(r) {
		http.Error(w, "Open Homelab at http://localhost:7080 on this computer.", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/auth/start?return="+url.QueryEscape(sanitizeReturn("/auth/app?"+r.URL.RawQuery)), http.StatusSeeOther)
}

// appHandoff issues a 60-second code after checking the person and the app grant.
func (s *server) appHandoff(w http.ResponseWriter, r *http.Request, personID string, query url.Values) {
	app, env, returnURL := query.Get("app"), query.Get("env"), query.Get("return")
	if app == "" || (env != core.Prod && env != core.Dev) {
		http.Error(w, "That app address isn't valid.", http.StatusBadRequest)
		return
	}
	p, err := s.store.Person(r.Context(), personID)
	if err != nil || p.Status != "signed_in" {
		http.Error(w, "This account isn't signed in to Homelab.", http.StatusForbidden)
		return
	}
	if !hub.Can(p, hub.ActionApps, app) || (env == core.Dev && !hub.Can(p, hub.ActionProjects, app)) {
		http.Error(w, "You don't have access to this app.", http.StatusForbidden)
		return
	}
	requested, err := url.Parse(returnURL)
	if err != nil || requested.User != nil || requested.Host == "" || requested.Path == "" {
		http.Error(w, "That app address isn't valid.", http.StatusBadRequest)
		return
	}
	view, err := s.hub.App(r.Context(), app)
	if err != nil {
		http.Error(w, "That app isn't available.", http.StatusNotFound)
		return
	}
	var base string
	for _, e := range view.Envs {
		if e.Env == env {
			base = e.Address
			break
		}
	}
	allowed, err := url.Parse(base)
	if err != nil || allowed.Host == "" || !strings.EqualFold(requested.Host, allowed.Host) || !strings.EqualFold(requested.Scheme, allowed.Scheme) {
		http.Error(w, "That app address doesn't match Homelab.", http.StatusBadRequest)
		return
	}
	code := make([]byte, 32)
	if _, err := rand.Read(code); err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	value := base64.RawURLEncoding.EncodeToString(code)
	digest := sha256.Sum256(code)
	returnPath := requested.EscapedPath()
	if requested.RawQuery != "" {
		returnPath += "?" + requested.RawQuery
	}
	if err := s.store.PutAppCode(r.Context(), digest[:], p.ID, app, env, returnPath, time.Now().Add(60*time.Second)); err != nil {
		s.fail(w, r, err)
		return
	}
	callback := strings.TrimRight(base, "/") + "/_homelab/callback"
	q := url.Values{"code": {value}}
	http.Redirect(w, r, callback+"?"+q.Encode(), http.StatusSeeOther)
}

func sanitizeReturn(value string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\r\n\\") {
		return "/"
	}
	return value
}
