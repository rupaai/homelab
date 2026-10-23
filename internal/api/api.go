// Package api is the HTTP JSON API the companion uses, and it serves the companion's
// static files.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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
	Version          string
	Static           fs.FS // the companion site root (index.html at the top)
	Logger           *slog.Logger
	Store            *state.Store
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCConfig       *OIDCConfig
	HTTPClient       *http.Client
	// PasswordFile holds the bcrypt hash of the owner's password, for signing in from other
	// devices with addresses: ip. Empty or missing means no password sign-in.
	PasswordFile string
	// Services serves files, databases and data (platform services). nil: those calls get 503.
	Services core.Services
}

// OIDCConfig lets startup publish sign-in settings after Pocket ID is ready.
type OIDCConfig struct {
	mu     sync.RWMutex
	issuer string
	client string
	secret string
}

// NewOIDCConfig creates settings that can be populated while the companion is serving.
func NewOIDCConfig(issuer, client, secret string) *OIDCConfig {
	c := &OIDCConfig{}
	c.Set(issuer, client, secret)
	return c
}

// Set publishes the current Pocket ID sign-in settings.
func (c *OIDCConfig) Set(issuer, client, secret string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.issuer = strings.TrimRight(issuer, "/")
	c.client = client
	c.secret = secret
}

func (c *OIDCConfig) values() (string, string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.issuer, c.client, c.secret
}

const (
	maxBody       = 64 << 10
	msgInternal   = "Something went wrong inside Homelab. The details are in its log."
	csrfHeader    = "X-Homelab-Request"
	noAppPrefix   = "There's no app"
	msgBadRequest = "That request wasn't understood."
)

type server struct {
	hub              *hub.Hub
	ver              string
	log              *slog.Logger
	static           *static
	store            *state.Store
	oidc             *OIDCConfig
	httpClient       *http.Client
	authMu           sync.Mutex
	recoveryAttempts map[string]recoveryWindow
	keyMux           *http.ServeMux
	passwordFile     string
	services         core.Services
}

type recoveryWindow struct {
	started time.Time
	count   int
}

// New returns the handler for everything Homelab serves: /api, /healthz and the companion.
func New(h *hub.Hub, opts Options) http.Handler {
	s := newServer(h, opts)
	return s.handler()
}

func newServer(h *hub.Hub, opts Options) *server {
	oidc := opts.OIDCConfig
	if oidc == nil {
		oidc = NewOIDCConfig(opts.OIDCIssuer, opts.OIDCClientID, opts.OIDCClientSecret)
	}
	s := &server{hub: h, ver: opts.Version, log: opts.Logger, store: opts.Store,
		oidc: oidc, httpClient: opts.HTTPClient, passwordFile: opts.PasswordFile, services: opts.Services}
	s.recoveryAttempts = map[string]recoveryWindow{}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.static = newStatic(opts.Static)
	s.keyMux = s.keyRoutes()
	return s
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.ver})
	})
	mux.HandleFunc("GET /auth/start", s.authStart)
	mux.HandleFunc("GET /auth/callback", s.authCallback)
	mux.HandleFunc("GET /auth/app", s.appAuth)
	mux.HandleFunc("GET /api/bootstrap", s.bootstrapStatus)
	mux.HandleFunc("POST /api/setup", s.setupOwner)
	mux.HandleFunc("POST /api/setup/passkey-link", s.initialOwnerPasskeyLink)
	mux.HandleFunc("POST /api/recover", s.recoverCode)
	mux.HandleFunc("POST /api/password-sign-in", s.passwordSignIn)
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("POST /api/me/passkey-link", s.passkeyLink)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/people", s.people)
	mux.HandleFunc("POST /api/people", s.invitePerson)
	mux.HandleFunc("PUT /api/people/{id}", s.updatePerson)
	mux.HandleFunc("DELETE /api/people/{id}", s.removePerson)
	mux.HandleFunc("POST /api/people/{id}/invite", s.resendInvite)
	mux.HandleFunc("POST /api/invites/{token}/redeem", s.redeemInvite)
	mux.HandleFunc("POST /api/invites/{token}/password", s.invitePassword)
	mux.HandleFunc("POST /api/email/test", s.testEmail)
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
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/secrets/{name}/regenerate", s.regenerateSecret)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/secrets/{name}/reveal", s.revealSecret)
	mux.HandleFunc("GET /api/apps/{id}/envs/{env}/secrets/{name}/versions", s.secretVersions)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/secrets/{name}/versions/{n}/restore", s.restoreSecret)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/saved/{name}/reveal", s.revealSavedSecret)
	mux.HandleFunc("DELETE /api/apps/{id}/envs/{env}/saved/{name}", s.deleteSavedSecret)
	mux.HandleFunc("GET /api/apps/{id}/db", s.appDatabase)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/db/copies", s.makeDBCopy)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/db/copies/{name}/restore", s.restoreDBCopy)
	mux.HandleFunc("POST /api/apps/{id}/db/copy-prod-to-dev", s.copyProdToDev)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/db/import", s.importDB)
	mux.HandleFunc("GET /api/settings/secrets", s.settings)
	mux.HandleFunc("PUT /api/settings/secrets/{name}", s.setSetting)
	mux.HandleFunc("DELETE /api/settings/secrets/{name}", s.deleteSetting)
	mux.HandleFunc("POST /api/settings/secrets/{name}/reveal", s.revealSetting)
	mux.HandleFunc("GET /api/settings/secrets/{name}/versions", s.settingVersions)
	mux.HandleFunc("POST /api/settings/secrets/{name}/versions/{n}/restore", s.restoreSetting)
	mux.HandleFunc("GET /api/push/key", s.pushKey)
	mux.HandleFunc("POST /api/push/subscriptions", s.subscribePush)
	mux.HandleFunc("DELETE /api/push/subscriptions", s.unsubscribePush)
	mux.HandleFunc("POST /api/push/test", s.testPush)
	mux.HandleFunc("GET /api/resources", s.resources)
	mux.HandleFunc("GET /api/device", s.device)
	mux.HandleFunc("POST /api/device/update", s.updateHomelab)
	mux.HandleFunc("GET /api/storage", s.storage)
	mux.HandleFunc("GET /api/keys", s.listKeys)
	mux.HandleFunc("POST /api/keys", s.createKey)
	mux.HandleFunc("DELETE /api/keys/{id}", s.revokeKey)
	mux.HandleFunc("GET /api/device/tests", s.testVersions)
	mux.HandleFunc("POST /api/device/test", s.tryTestVersion)
	mux.HandleFunc("POST /api/device/test/back", s.endTest(false))
	mux.HandleFunc("POST /api/device/test/keep", s.endTest(true))
	for _, p := range servicePaths {
		mux.HandleFunc(p, s.personToServices)
		mux.HandleFunc(p+"/", s.personToServices)
	}
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
		// A call with an API key never uses the session or the companion (platform services).
		// A companion session token is also sent as a Bearer token, but starts with sessionPrefix.
		token, hasToken := bearer(r)
		if hasToken && !strings.HasPrefix(token, sessionPrefix) && (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) {
			s.serveKey(w, r, false)
			return
		}
		if digest, ok := sessionDigest(token); ok && s.store != nil {
			if p, err := s.store.SessionPerson(r.Context(), digest, time.Now(), 7*24*time.Hour); err == nil {
				v, _ := core.ViewerFrom(r.Context())
				v.PersonID, v.Login, v.Name, v.Session = p.ID, p.Email, p.Name, true
				r = r.WithContext(core.WithViewer(r.Context(), v))
			}
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			hd.Set("Cache-Control", "no-store")
			if r.URL.Path == "/api/bootstrap" || (r.URL.Path == "/api/setup" && r.Method == http.MethodPost) || (r.URL.Path == "/api/setup/passkey-link" && r.Method == http.MethodPost) || (r.URL.Path == "/api/recover" && r.Method == http.MethodPost) || (r.URL.Path == "/api/password-sign-in" && r.Method == http.MethodPost) || (r.URL.Path == "/api/logout" && r.Method == http.MethodPost) || (strings.HasPrefix(r.URL.Path, "/api/invites/") && (strings.HasSuffix(r.URL.Path, "/redeem") || strings.HasSuffix(r.URL.Path, "/password")) && r.Method == http.MethodPost) {
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
	return v.PersonID != "" && s.store != nil
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

// onThisComputer says whether a "this computer only" request really is: it must arrive over
// a loopback connection (the viewer's Local flag, set from RemoteAddr) and name this computer
// in its Host header. The Host header alone is chosen by the client.
func onThisComputer(r *http.Request) bool {
	v, _ := core.ViewerFrom(r.Context())
	return v.Local && loopbackHost(r.Host)
}

// publicPost holds the rules for every POST that skips guard: the companion's header, a JSON
// body and a size limit. Keep them in one place so those endpoints can't drift apart.
func publicPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, http.StatusForbidden, "That request didn't come from the Homelab companion.")
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "Send JSON with Content-Type: application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	return true
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
	// Secret values need a real sign-in, never an API key.
	if action == hub.ActionSecretValues && !v.Session {
		writeErr(w, http.StatusForbidden, msgSecretSession)
		return false
	}
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
		// homelab-services takes file uploads and sets its own limits.
		if isServicePath(r.URL.Path) {
			return true
		}
		// A database import is the raw file, which the hub streams to disk and limits.
		if isDBImport(r) {
			if ct := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); !strings.EqualFold(ct, "application/octet-stream") {
				writeErr(w, http.StatusUnsupportedMediaType, "Send the file with Content-Type: application/octet-stream.")
				return false
			}
			return true
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

// msgSecretSession is what a call gets when it reaches for a secret value without a sign-in.
const msgSecretSession = "Secret values can only be seen or changed by signing in to the Homelab companion. API keys can't do it."

// secretValueRoute says whether path parts (of /api/...) name an endpoint that shows, sets,
// replaces, regenerates, deletes or restores a secret value, or lists its versions:
//
//	/api/apps/{id}/envs/{env}/secrets/{name}[/...]
//	/api/apps/{id}/envs/{env}/saved/{name}[/...]   (secrets the app saved itself)
//	/api/settings/secrets/{name}[/...]
//
// The plain list of an app's secrets (names only) is not one.
func secretValueRoute(parts []string) bool {
	if len(parts) >= 7 && parts[1] == "apps" && parts[3] == "envs" && (parts[5] == "secrets" || parts[5] == "saved") {
		return true
	}
	return len(parts) >= 4 && parts[1] == "settings" && parts[2] == "secrets"
}

// isSecretValueRoute is secretValueRoute for a request.
func isSecretValueRoute(r *http.Request) bool {
	return secretValueRoute(strings.Split(strings.Trim(r.URL.Path, "/"), "/"))
}

func apiAction(r *http.Request) (string, string) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		return hub.ActionManage, ""
	}
	if secretValueRoute(parts) {
		if parts[1] == "apps" {
			return hub.ActionSecretValues, parts[2]
		}
		return hub.ActionSecretValues, ""
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
			case "db":
				return hub.ActionManage, app // copies and restores of an app's database: admins
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
	if parts[1] == "me" || parts[1] == "push" {
		return hub.ActionSelf, ""
	}
	if parts[1] == "activity" && r.Method == http.MethodGet {
		if app := r.URL.Query().Get("app"); app != "" {
			return hub.ActionLogs, app // builders see their own apps' activity (O3)
		}
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
	case errors.Is(err, core.ErrSecretsUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
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
	writeJSON(w, http.StatusOK, map[string]any{"login": p.Email, "name": p.Name, "local": v.Local, "roles": p.Roles,
		"canSeeSecrets": hub.Can(p, hub.ActionSecretValues, "")})
}

func (s *server) passkeyLink(w http.ResponseWriter, r *http.Request) {
	link, err := s.hub.PasskeyLink(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": link})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if v, _ := core.ViewerFrom(r.Context()); !v.Network && !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, "Open the companion at http://localhost:7080 on this computer.")
		return
	}
	if !publicPost(w, r) {
		return
	}
	if token, _ := bearer(r); token != "" {
		if digest, ok := sessionDigest(token); ok {
			_ = s.store.DeleteSession(r.Context(), digest)
		}
	}
	// Older versions kept the session in a cookie. Clear it so those browsers drop it.
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

// appDatabase describes the app's database and its copies.
func (s *server) appDatabase(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.Database(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) makeDBCopy(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	c, err := s.hub.MakeDBCopy(actionCtx(r), r.PathValue("id"), r.PathValue("env"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) restoreDBCopy(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.RestoreDBCopy(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) copyProdToDev(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.CopyProdToDev(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

// isDBImport says whether a request uploads a file to import into an app's database.
func isDBImport(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return r.Method == http.MethodPost && len(parts) == 7 && parts[1] == "apps" && parts[3] == "envs" && parts[5] == "db" && parts[6] == "import"
}

// importDB loads the uploaded file (the whole body) into an environment's database.
func (s *server) importDB(w http.ResponseWriter, r *http.Request) {
	kind, err := s.hub.ImportDB(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.Body, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "kind": kind})
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

func (s *server) pushKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": s.hub.PushPublicKey()})
}

type pushInput struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
	Device   string `json:"device"`
}

func (s *server) subscribePush(w http.ResponseWriter, r *http.Request) {
	var in pushInput
	if !decode(w, r, &in, true) {
		return
	}
	sub := core.PushSubscription{Endpoint: in.Endpoint, P256dh: in.P256dh, Auth: in.Auth}
	if err := s.hub.SubscribePush(r.Context(), viewer(r).PersonID, in.Device, sub); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	var in pushInput
	if !decode(w, r, &in, true) {
		return
	}
	if err := s.hub.UnsubscribePush(r.Context(), viewer(r).PersonID, in.Endpoint); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) testPush(w http.ResponseWriter, r *http.Request) {
	n, err := s.hub.TestPush(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": n})
}

func (s *server) resources(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.Resources(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) device(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.Device(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) updateHomelab(w http.ResponseWriter, r *http.Request) {
	version, err := s.hub.UpdateHomelab(actionCtx(r), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"version": version})
}

func (s *server) testVersions(w http.ResponseWriter, r *http.Request) {
	tests, err := s.hub.TestVersions(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tests)
}

func (s *server) tryTestVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	t, err := s.hub.TryTestVersion(actionCtx(r), viewer(r), in.Version)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

func (s *server) endTest(keep bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		version, err := s.hub.EndTest(actionCtx(r), viewer(r), keep)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"version": version})
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"envs": envs, "canSeeValues": s.hub.CanSeeSecretValues(r.Context(), viewer(r).PersonID)})
}

func (s *server) revealSecret(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	v, err := s.hub.RevealSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *server) revealSavedSecret(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	v, err := s.hub.RevealSavedSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *server) deleteSavedSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.DeleteSavedSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) secretVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.hub.SecretVersions(r.Context(), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": list})
}

func (s *server) restoreSecret(w http.ResponseWriter, r *http.Request) {
	n, ok := versionParam(w, r)
	if !ok || !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.RestoreSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), n, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func versionParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeErr(w, http.StatusBadRequest, "That isn't a version number.")
		return 0, false
	}
	return n, true
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

func (s *server) regenerateSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.RegenerateSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
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

func (s *server) revealSetting(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	v, err := s.hub.RevealPlatformSetting(actionCtx(r), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *server) settingVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.hub.PlatformSettingVersions(r.Context(), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": list})
}

func (s *server) restoreSetting(w http.ResponseWriter, r *http.Request) {
	n, ok := versionParam(w, r)
	if !ok || !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.RestorePlatformSetting(actionCtx(r), r.PathValue("name"), n, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}
