// Package api is the HTTP JSON API the companion uses, and it serves the companion's
// static files.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
	"github.com/rupaai/homelab/internal/state"
)

// Options configure the API.
type Options struct {
	Owners           []string // Tailscale logins allowed in; empty means any signed-in person
	Version          string
	Static           fs.FS // the companion site root (index.html at the top)
	Logger           *slog.Logger
	Store            *state.Store
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	HTTPClient       *http.Client
}

const (
	maxBody       = 64 << 10
	msgNotOwner   = "This Homelab only lets its owner in. Ask them to add your Tailscale login to owners in config.yaml."
	msgInternal   = "Something went wrong inside Homelab. The details are in its log."
	csrfHeader    = "X-Homelab-Request"
	noAppPrefix   = "There's no app"
	msgBadRequest = "That request wasn't understood."
)

type server struct {
	hub              *hub.Hub
	owners           map[string]bool
	ver              string
	log              *slog.Logger
	static           *static
	store            *state.Store
	oidcIssuer       string
	oidcClientID     string
	oidcClientSecret string
	httpClient       *http.Client
	authMu           sync.Mutex
	recoveryAttempts map[string]recoveryWindow
}

type recoveryWindow struct {
	started time.Time
	count   int
}

// New returns the handler for everything Homelab serves: /api, /healthz and the companion.
func New(h *hub.Hub, opts Options) http.Handler {
	s := &server{hub: h, owners: map[string]bool{}, ver: opts.Version, log: opts.Logger, store: opts.Store,
		oidcIssuer: strings.TrimRight(opts.OIDCIssuer, "/"), oidcClientID: opts.OIDCClientID, oidcClientSecret: opts.OIDCClientSecret, httpClient: opts.HTTPClient}
	s.recoveryAttempts = map[string]recoveryWindow{}
	if s.log == nil {
		s.log = slog.Default()
	}
	for _, o := range opts.Owners {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			s.owners[o] = true
		}
	}
	s.static = newStatic(opts.Static)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.ver})
	})
	mux.HandleFunc("GET /auth/start", s.authStart)
	mux.HandleFunc("GET /auth/callback", s.authCallback)
	mux.HandleFunc("GET /auth/app", s.appAuth)
	mux.HandleFunc("GET /api/bootstrap", s.bootstrapStatus)
	mux.HandleFunc("POST /api/setup", s.setupOwner)
	mux.HandleFunc("POST /api/recover", s.recoverCode)
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/people", s.people)
	mux.HandleFunc("POST /api/people", s.invitePerson)
	mux.HandleFunc("PUT /api/people/{id}", s.updatePerson)
	mux.HandleFunc("DELETE /api/people/{id}", s.removePerson)
	mux.HandleFunc("POST /api/people/{id}/invite", s.resendInvite)
	mux.HandleFunc("GET /invite/{token}", s.inviteLanding)
	mux.HandleFunc("GET /recover/{token}", s.recoverLink)
	mux.HandleFunc("GET /api/apps", s.listApps)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/apps", s.addApp)
	mux.HandleFunc("GET /api/apps/{id}", s.getApp)
	mux.HandleFunc("POST /api/apps/{id}/deploy", s.deploy)
	mux.HandleFunc("GET /api/apps/{id}/promote", s.previewPromote)
	mux.HandleFunc("POST /api/apps/{id}/promote", s.promote)
	mux.HandleFunc("GET /api/apps/{id}/envs/{env}/rollback", s.previewRollback)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/rollback", s.rollback)
	mux.HandleFunc("GET /api/apps/{id}/envs/{env}/logs", s.logs)
	mux.HandleFunc("GET /api/activity", s.activity)
	mux.HandleFunc("PUT /api/apps/{id}/promotion", s.setPromotion)
	mux.HandleFunc("DELETE /api/apps/{id}", s.removeApp)
	mux.HandleFunc("GET /api/apps/{id}/secrets", s.appSecrets)
	mux.HandleFunc("PUT /api/apps/{id}/envs/{env}/secrets/{name}", s.setSecret)
	mux.HandleFunc("DELETE /api/apps/{id}/envs/{env}/secrets/{name}", s.deleteSecret)
	mux.HandleFunc("GET /api/settings/secrets", s.settings)
	mux.HandleFunc("PUT /api/settings/secrets/{name}", s.setSetting)
	mux.HandleFunc("DELETE /api/settings/secrets/{name}", s.deleteSetting)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "There's nothing at that address.")
	})
	mux.Handle("/", s.static)

	csp := s.static.csp()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Content-Security-Policy", csp)
		if c, err := r.Cookie("homelab_session"); err == nil && s.store != nil {
			if raw, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil && len(raw) == 32 {
				digest := sha256.Sum256(raw)
				if p, err := s.store.SessionPerson(r.Context(), digest[:], time.Now(), 7*24*time.Hour); err == nil {
					v, _ := core.ViewerFrom(r.Context())
					v.PersonID, v.Login, v.Name = p.ID, p.Email, p.Name
					r = r.WithContext(core.WithViewer(r.Context(), v))
				}
			}
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			hd.Set("Cache-Control", "no-store")
			if r.URL.Path == "/api/bootstrap" || (r.URL.Path == "/api/setup" && r.Method == http.MethodPost) || (r.URL.Path == "/api/recover" && r.Method == http.MethodPost) || (r.URL.Path == "/api/logout" && r.Method == http.MethodPost) {
				mux.ServeHTTP(w, r)
				return
			}
			if !s.guard(w, r) {
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// allowed says whether the viewer may use Homelab.
func (s *server) allowed(v core.Viewer) bool {
	return !v.Tagged && v.PersonID != "" && s.store != nil
}

// loopbackHost says whether a request's Host header names this computer. Requests on the
// local listener must, so a web page can't reach the API through DNS rebinding.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guard applies the owner check and, for changes, the CSRF header and body rules.
func (s *server) guard(w http.ResponseWriter, r *http.Request) bool {
	v, ok := core.ViewerFrom(r.Context())
	if !ok || !s.allowed(v) {
		writeErr(w, http.StatusForbidden, "Sign in to use Homelab.")
		return false
	}
	p, err := s.store.Person(r.Context(), v.PersonID)
	if err != nil || p.Status != "signed_in" {
		writeErr(w, http.StatusForbidden, "Sign in to use Homelab.")
		return false
	}
	action, app := apiAction(r)
	if action == hub.ActionRemoveUser {
		if target, err := s.store.Person(r.Context(), app); err == nil && hasRole(target, "owner") {
			app = "owner"
		}
	}
	if !hub.Can(p, action, app) {
		writeErr(w, http.StatusForbidden, "You don't have access to that part of Homelab.")
		return false
	}
	if v.Local && !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, "Open the companion at http://localhost:7080 on this computer.")
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Header.Get(csrfHeader) != "1" {
			writeErr(w, http.StatusForbidden, "That request didn't come from the Homelab companion.")
			return false
		}
		if r.ContentLength != 0 {
			ct := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
			if !strings.EqualFold(ct, "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "Send JSON with Content-Type: application/json.")
				return false
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	}
	return true
}

func apiAction(r *http.Request) (string, string) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		return hub.ActionManage, ""
	}
	if parts[1] == "apps" {
		if len(parts) == 2 {
			if r.Method == http.MethodGet {
				return hub.ActionApps, ""
			}
			return hub.ActionManage, ""
		}
		app := parts[2]
		for _, part := range parts[3:] {
			switch part {
			case "logs":
				return hub.ActionLogs, app
			case "secrets":
				return hub.ActionSecrets, app
			case "deploy", "promote", "rollback":
				return hub.ActionProjects, app
			}
		}
		if r.Method == http.MethodGet {
			return hub.ActionApps, app
		}
		return hub.ActionManage, app
	}
	if parts[1] == "projects" && r.Method == http.MethodGet {
		return hub.ActionProjects, ""
	}
	if parts[1] == "people" && len(parts) == 3 && r.Method == http.MethodDelete {
		return hub.ActionRemoveUser, parts[2]
	}
	if parts[1] == "me" && r.Method == http.MethodGet {
		return hub.ActionApps, ""
	}
	if parts[1] == "logout" {
		return hub.ActionApps, ""
	}
	return hub.ActionManage, ""
}

func viewer(r *http.Request) core.Viewer {
	v, _ := core.ViewerFrom(r.Context())
	return v
}

// actionCtx keeps the viewer but not the request's cancellation, so an action a person
// started isn't abandoned half way when their browser goes away.
func actionCtx(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps an error from the hub to a response.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ue *hub.UserError
	switch {
	case errors.Is(err, hub.ErrBusy):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.As(err, &ue):
		if strings.HasPrefix(ue.Msg, noAppPrefix) {
			writeErr(w, http.StatusNotFound, ue.Msg)
			return
		}
		writeErr(w, http.StatusBadRequest, ue.Msg)
	default:
		s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeErr(w, http.StatusInternalServerError, msgInternal)
	}
}

// decode reads an optional JSON body into dst, rejecting unknown fields.
func decode(w http.ResponseWriter, r *http.Request, dst any, required bool) bool {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			writeErr(w, http.StatusRequestEntityTooLarge, "That request is too big.")
		} else {
			writeErr(w, http.StatusBadRequest, msgBadRequest)
		}
		return false
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		if required {
			writeErr(w, http.StatusBadRequest, msgBadRequest+" Send a JSON body.")
			return false
		}
		return true
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, http.StatusBadRequest, msgBadRequest+" Check the JSON you sent.")
		return false
	}
	return true
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	v := viewer(r)
	p, err := s.store.Person(r.Context(), v.PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": p.Email, "name": p.Name, "local": v.Local, "roles": p.Roles})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, "Open the companion at http://localhost:7080 on this computer.")
		return
	}
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, http.StatusForbidden, "That request didn't come from the Homelab companion.")
		return
	}
	if c, err := r.Cookie("homelab_session"); err == nil {
		if raw, e := base64.RawURLEncoding.DecodeString(c.Value); e == nil {
			digest := sha256.Sum256(raw)
			_ = s.store.DeleteSession(r.Context(), digest[:])
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "homelab_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
func (s *server) people(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.People(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *server) invitePerson(w http.ResponseWriter, r *http.Request) {
	var p core.Person
	if !decode(w, r, &p, true) {
		return
	}
	link, err := s.hub.InvitePerson(actionCtx(r), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": link})
}
func (s *server) updatePerson(w http.ResponseWriter, r *http.Request) {
	var p core.Person
	if !decode(w, r, &p, true) {
		return
	}
	p.ID = r.PathValue("id")
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
	link, err := s.hub.ResendInvite(actionCtx(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": link})
}
func (s *server) inviteLanding(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		http.Error(w, "Open this invite on the Homelab computer.", http.StatusForbidden)
		return
	}
	link, err := s.hub.RedeemInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusGone)
		return
	}
	http.Redirect(w, r, link, http.StatusSeeOther)
}

func (s *server) recoverLink(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
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
	var session [32]byte
	if _, err = rand.Read(session[:]); err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	raw := base64.RawURLEncoding.EncodeToString(session[:])
	hash := sha256.Sum256(session[:])
	now := time.Now()
	if err = s.store.PutSession(r.Context(), hash[:], id, now, now.Add(30*24*time.Hour)); err != nil {
		http.Error(w, msgInternal, http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "homelab_session", Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || strings.EqualFold(r.URL.Scheme, "https"), MaxAge: 30 * 24 * 60 * 60})
	_ = s.store.Log(r.Context(), state.Event{Actor: p.Email, Action: "recovery.link.used", Detail: "Signed in with a one-time recovery link"})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.hub.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if apps == nil {
		apps = []hub.AppView{}
	}
	p, err := s.store.Person(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !hasRole(p, "owner") && !hasRole(p, "admin") {
		filtered := make([]hub.AppView, 0, len(apps))
		for _, a := range apps {
			level, ok := p.Grants[a.ID]
			if !ok {
				continue
			}
			if level != "build" {
				a = productionOnly(a)
			}
			filtered = append(filtered, a)
		}
		apps = filtered
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	apps, err := s.hub.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.store.Person(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !hasRole(p, "owner") && !hasRole(p, "admin") {
		filtered := make([]hub.AppView, 0, len(apps))
		for _, app := range apps {
			if p.Grants[app.ID] == "build" {
				filtered = append(filtered, app)
			}
		}
		apps = filtered
	}
	if apps == nil {
		apps = []hub.AppView{}
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *server) addApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	ctx := actionCtx(r)
	a, err := s.hub.AddApp(ctx, in.Repo, in.Branch, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.hub.App(ctx, a.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.App(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.store.Person(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !hasRole(p, "owner") && !hasRole(p, "admin") && p.Grants[v.ID] != "build" {
		v = productionOnly(v)
	}
	writeJSON(w, http.StatusOK, v)
}

func hasRole(p core.Person, role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}
func productionOnly(a hub.AppView) hub.AppView {
	for _, e := range a.Envs {
		if e.Env == core.Prod {
			a.Envs = []hub.EnvView{e}
			break
		}
	}
	a.LatestBuild = nil
	a.Held = []hub.HoldView{}
	a.AutoPromote = nil
	a.Mirror = nil
	return a
}

func accepted(w http.ResponseWriter) { writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true}) }

func (s *server) deploy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Commit string `json:"commit"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if err := s.hub.DeployDev(actionCtx(r), r.PathValue("id"), in.Commit, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) previewPromote(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.PreviewPromote(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) promote(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.Promote(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) previewRollback(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.PreviewRollback(r.Context(), r.PathValue("id"), r.PathValue("env"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) rollback(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.Rollback(actionCtx(r), r.PathValue("id"), r.PathValue("env"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	text, err := s.hub.Logs(r.Context(), r.PathValue("id"), r.PathValue("env"), lines)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"lines": text})
}

func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	ev, err := s.hub.Events(r.Context(), q.Get("app"), before, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func (s *server) setPromotion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	v, err := s.hub.SetPromotion(actionCtx(r), r.PathValue("id"), in.Mode, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) removeApp(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.RemoveApp(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) appSecrets(w http.ResponseWriter, r *http.Request) {
	envs, err := s.hub.AppSecrets(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envs": envs})
}

type valueIn struct {
	Value string `json:"value"`
}

func (s *server) setSecret(w http.ResponseWriter, r *http.Request) {
	var in valueIn
	if !decode(w, r, &in, true) {
		return
	}
	if err := s.hub.SetSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), in.Value, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.DeleteSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	list, err := s.hub.PlatformSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) setSetting(w http.ResponseWriter, r *http.Request) {
	var in valueIn
	if !decode(w, r, &in, true) {
		return
	}
	if err := s.hub.SetPlatformSetting(actionCtx(r), r.PathValue("name"), in.Value, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) deleteSetting(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.DeletePlatformSetting(actionCtx(r), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}
