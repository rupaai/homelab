// Package api is the HTTP JSON API the companion uses, and it serves the companion's
// static files.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
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
func accepted(w http.ResponseWriter) { writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true}) }

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }
