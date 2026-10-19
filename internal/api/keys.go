package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// Calls made with an API key (platform services). They never use a session cookie or the
// companion: Homelab drops cookies and any X-Homelab-* headers from them, and they reach
// only the routes keyRoutes lists.
//
//   - Admin keys work on the companion's own listeners: the tailnet address, and this
//     computer. Never on the app door.
//   - App keys work only on the app door (Door), which serves nothing else.

const (
	keyFailLimit  = 20               // failed key attempts allowed per address...
	keyFailWindow = 10 * time.Minute // ...in this long
	msgBadKey     = "That API key isn't valid."
)

type callerKey struct{}

func withCaller(ctx context.Context, c hub.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFrom(ctx context.Context) (hub.Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(hub.Caller)
	return c, ok
}

// bearer returns the token of an "Authorization: Bearer" header, and whether there is one.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// keyLimiter counts failed key attempts per address.
type keyLimiter struct {
	mu   sync.Mutex
	seen map[string]recoveryWindow
}

func (l *keyLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.seen[ip]
	if now.Sub(e.started) > keyFailWindow {
		delete(l.seen, ip)
		return false
	}
	return e.count >= keyFailLimit
}

func (l *keyLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) > 10000 { // never grows without bound
		l.seen = map[string]recoveryWindow{}
	}
	e := l.seen[ip]
	if now.Sub(e.started) > keyFailWindow {
		e = recoveryWindow{started: now}
	}
	e.count++
	l.seen[ip] = e
}

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// stripCallerHeaders removes anything a caller could use to pretend to be someone else.
func stripCallerHeaders(r *http.Request) {
	r.Header.Del("Cookie")
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-homelab-") {
			r.Header.Del(name)
		}
	}
}

// keyRoutes is everything a key may call.
func (s *server) keyRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", s.usersMe)
	mux.HandleFunc("GET /api/users", s.usersList)
	mux.HandleFunc("GET /api/users/stats", s.usersStats)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "API keys can't use that part of Homelab.")
	})
	return mux
}

// serveKey handles a request that carries an API key. door says it came in on the app door.
func (s *server) serveKey(w http.ResponseWriter, r *http.Request, door bool) {
	w.Header().Set("Cache-Control", "no-store")
	stripCallerHeaders(r)
	token, _ := bearer(r)
	r.Header.Del("Authorization")
	ip, now := remoteIP(r), time.Now()
	if s.keyFails.blocked(ip, now) {
		writeErr(w, http.StatusTooManyRequests, "Too many wrong API keys from here. Wait a few minutes.")
		return
	}
	c, err := s.hub.Authenticate(r.Context(), token)
	if err != nil {
		if !errors.Is(err, hub.ErrBadKey) {
			s.log.Error("api key check", "err", err)
		}
		s.keyFails.fail(ip, now)
		s.log.Warn("api key refused", "from", ip, "door", door)
		w.Header().Set("WWW-Authenticate", `Bearer realm="homelab"`)
		writeErr(w, http.StatusUnauthorized, msgBadKey)
		return
	}
	if door && c.Kind != hub.KeyKindApp {
		writeErr(w, http.StatusForbidden, "Only an app's own key (HOMELAB_TOKEN) works here. Use admin keys on the companion's Tailscale address.")
		return
	}
	if !door {
		if c.Kind != hub.KeyKindAdmin {
			writeErr(w, http.StatusForbidden, "An app's own key only works from inside the app, at HOMELAB_API_URL.")
			return
		}
		// Only the companion's own listeners (Tailscale, or this computer) put a viewer
		// on the request, so a key that arrives any other way is refused.
		if _, ok := core.ViewerFrom(r.Context()); !ok {
			writeErr(w, http.StatusForbidden, "Admin keys only work over Tailscale.")
			return
		}
	}
	r = r.WithContext(withCaller(core.WithViewer(r.Context(), core.Viewer{}), c))
	s.keyMux.ServeHTTP(w, r)
}

// Door is the app door: the Homelab API for app containers, with their own key only. It
// serves no companion, no sign-in and no cookies.
func Door(h *hub.Hub, opts Options) http.Handler {
	s := newServer(h, opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, ok := bearer(r); !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homelab"`)
			writeErr(w, http.StatusUnauthorized, "Send the app's key: Authorization: Bearer $HOMELAB_TOKEN.")
			return
		}
		s.serveKey(w, r, true)
	})
}

// ---- Users API ----

// usersApp works out which app a users request is about. An app key only ever sees its
// own app; anyone else may ask about any app, or (app "") everyone.
func (s *server) usersApp(w http.ResponseWriter, r *http.Request) (string, hub.Caller, bool, bool) {
	app := r.URL.Query().Get("app")
	c, isKey := callerFrom(r.Context())
	if !isKey {
		return app, c, false, true
	}
	if !c.Allows(hub.ScopeUsersRead) {
		writeErr(w, http.StatusForbidden, "This key can't read people. It needs the users:read scope.")
		return "", c, true, false
	}
	if c.Kind == hub.KeyKindApp {
		if app != "" && app != c.App {
			writeErr(w, http.StatusForbidden, "An app's key only sees the people of its own app.")
			return "", c, true, false
		}
		app = c.App
	}
	return app, c, true, true
}

func (s *server) usersMe(w http.ResponseWriter, r *http.Request) {
	c, isKey := callerFrom(r.Context())
	if !isKey {
		u, err := s.hub.UserByID(r.Context(), viewer(r).PersonID, "")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"kind": "person", "person": u})
		return
	}
	s.hub.LogKeyUse(r.Context(), c, "Asked who it is", false)
	if c.Kind == hub.KeyKindApp {
		writeJSON(w, http.StatusOK, map[string]any{"kind": "app", "app": c.App, "env": c.Env, "scopes": c.Scopes})
		return
	}
	u, err := s.hub.UserByID(r.Context(), c.PersonID, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": "key", "key": c.KeyName, "scopes": c.Scopes, "person": u})
}

func (s *server) usersList(w http.ResponseWriter, r *http.Request) {
	app, c, isKey, ok := s.usersApp(w, r)
	if !ok {
		return
	}
	list, err := s.hub.Users(r.Context(), app)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if isKey {
		what := "Listed everyone"
		if app != "" {
			what = "Listed the people of " + app
		}
		s.hub.LogKeyUse(r.Context(), c, what, false)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) usersStats(w http.ResponseWriter, r *http.Request) {
	app, c, isKey, ok := s.usersApp(w, r)
	if !ok {
		return
	}
	st, err := s.hub.Stats(r.Context(), app)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if isKey {
		s.hub.LogKeyUse(r.Context(), c, "Read people counts", false)
	}
	writeJSON(w, http.StatusOK, st)
}

// ---- Managing keys (companion, admins) ----

func (s *server) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.hub.APIKeys(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *server) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string     `json:"name"`
		Scopes  []string   `json:"scopes"`
		Expires *time.Time `json:"expires"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	token, k, err := s.hub.CreateAdminKey(actionCtx(r), in.Name, in.Scopes, in.Expires, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": token, "info": hub.KeyView{APIKey: k}})
}

func (s *server) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.RevokeKey(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}
