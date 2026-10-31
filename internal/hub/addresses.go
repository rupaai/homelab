// Addresses: the names apps are served on, and the proxy that fronts them.

package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/identity"
)

func (h *Hub) ensureAddress(ctx context.Context, name string, port int, app, appName, env string) {
	h.mu.Lock()
	cur, ok := h.addresses[name]
	h.mu.Unlock()
	if ok && cur == port {
		return
	}
	if err := h.ad.Addresses.Ensure(ctx, name, h.appProxy(port, app, appName, env)); err != nil {
		h.log.Error("address", "name", name, "err", err)
		return
	}
	h.mu.Lock()
	h.addresses[name] = port
	h.mu.Unlock()
}

func (h *Hub) removeAddress(ctx context.Context, name string) {
	h.mu.Lock()
	_, ok := h.addresses[name]
	delete(h.addresses, name)
	h.mu.Unlock()
	if ok {
		_ = h.ad.Addresses.Remove(ctx, name)
	}
}

// appProxy forwards an address's traffic to the app's published port, with signed headers
// saying who is asking (A2).
func (h *Hub) appProxy(port int, app, appName, env string) http.Handler {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	p := httputil.NewSingleHostReverseProxy(target)
	base := p.Director
	p.Director = func(r *http.Request) {
		v, ok := core.ViewerFrom(r.Context())
		person, err := h.store.Person(r.Context(), v.PersonID)
		if !h.cfg.OpenApps && (!ok || err != nil || !Can(person, ActionApps, app) || (env == core.Dev && !Can(person, ActionProjects, app))) {
			return
		}
		// Only Homelab may set its identity headers, so drop any a client sent.
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-homelab-") {
				r.Header.Del(k)
			}
		}
		base(r)
		proto := "http"
		if r.TLS != nil {
			proto = "https"
		}
		r.Header.Set("X-Forwarded-Proto", proto)
		if h.cfg.Identity != nil {
			v, _ := core.ViewerFrom(r.Context())
			if err := h.cfg.Identity.Apply(r, app, env, h.person(v), h.cfg.Now()); err != nil {
				h.log.Warn("identity headers", "app", app, "err", err)
				_ = h.cfg.Identity.Apply(r, app, env, nil, h.cfg.Now())
			}
		}
	}
	serve := p.ServeHTTP
	label := appName
	if env != core.Prod {
		label += " (" + env + ")"
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `<!doctype html><meta name="viewport" content="width=device-width"><title>Not running</title>`+
			`<body style="font-family:system-ui;padding:2rem;max-width:30rem;margin:auto"><h1>%s isn't answering right now</h1>`+
			`<p>It may be starting up. Try again in a moment, or check it in the Homelab companion.</p>`, htmlEscape(label))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.OpenApps {
			// addresses: ip: the app is open to whoever reaches its port, and gets no person.
			// Homelab's own cookies reach this port too (same host), so they are dropped.
			h.serveAs(w, r, core.Person{}, serve)
			return
		}
		if r.URL.Path == "/_homelab/callback" {
			h.appCallback(w, r, app, env)
			return
		}
		person, ok := h.appSessionPerson(r, app, env)
		if !ok {
			// The companion's sign-in is not visible on this port. Send the person through the
			// companion, which checks the app grant and hands them back with a code.
			returnURL := "http://" + r.Host + r.URL.RequestURI()
			query := url.Values{"app": {app}, "env": {env}, "return": {returnURL}}
			http.Redirect(w, r, "http://localhost:7080/auth/app?"+query.Encode(), http.StatusSeeOther)
			return
		}
		if !Can(person, ActionApps, app) || (env == core.Dev && !Can(person, ActionProjects, app)) {
			h.proxyForbidden(w)
			return
		}
		h.serveAs(w, r, person, serve)
	})
}

// serveAs forwards r to the app as person, without Homelab's own headers and cookies.
func (h *Hub) serveAs(w http.ResponseWriter, r *http.Request, person core.Person, serve http.HandlerFunc) {
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-homelab-") {
			r.Header.Del(name)
		}
	}
	kept := forwardedCookies(r.Header.Values("Cookie"))
	r.Header.Del("Cookie")
	if kept != "" {
		r.Header.Set("Cookie", kept)
	}
	r = r.WithContext(core.WithViewer(r.Context(), core.Viewer{PersonID: person.ID, Login: person.Email, Name: person.Name}))
	serve(w, r)
}

func forwardedCookies(headers []string) string {
	var kept []string
	for _, header := range headers {
		start, quoted, escaped := 0, false, false
		for i := 0; i <= len(header); i++ {
			if i < len(header) {
				c := header[i]
				if escaped {
					escaped = false
					continue
				}
				if quoted && c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					quoted = !quoted
					continue
				}
				if c != ';' || quoted {
					continue
				}
			}
			part := strings.TrimSpace(header[start:i])
			start = i + 1
			if part == "" {
				continue
			}
			name, _, hasValue := strings.Cut(part, "=")
			if hasValue && strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "homelab_") {
				continue
			}
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

func (h *Hub) proxyForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html><meta name="viewport" content="width=device-width"><title>Access denied</title><p>You don't have access to this app.</p>`))
}

func (h *Hub) appSessionPerson(r *http.Request, app, env string) (core.Person, bool) {
	c, err := r.Cookie("homelab_app_" + app + "_" + env)
	if err != nil {
		return core.Person{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 32 {
		return core.Person{}, false
	}
	hash := sha256.Sum256(raw)
	p, err := h.store.AppSessionPerson(r.Context(), hash[:], app, env, h.cfg.Now(), 8*time.Hour)
	return p, err == nil && Can(p, ActionApps, app) && (env != core.Dev || Can(p, ActionProjects, app))
}

func (h *Hub) appCallback(w http.ResponseWriter, r *http.Request, app, env string) {
	if r.Method != http.MethodGet {
		http.Error(w, "That app link isn't valid.", http.StatusMethodNotAllowed)
		return
	}
	code, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("code"))
	if err != nil || len(code) != 32 {
		http.Error(w, "That app link has expired. Open the app again from Homelab.", http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256(code)
	grant, err := h.store.ConsumeAppCode(r.Context(), digest[:], h.cfg.Now())
	if err != nil || grant.App != app || grant.Env != env {
		http.Error(w, "That app link has expired. Open the app again from Homelab.", http.StatusBadRequest)
		return
	}
	person, err := h.store.Person(r.Context(), grant.PersonID)
	if err != nil || !Can(person, ActionApps, app) || (env == core.Dev && !Can(person, ActionProjects, app)) {
		h.proxyForbidden(w)
		return
	}
	path := grant.ReturnPath
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") {
		path = "/"
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "Homelab couldn't open that app. Try again.", http.StatusInternalServerError)
		return
	}
	now := h.cfg.Now()
	hash := sha256.Sum256(raw)
	if err := h.store.PutAppSession(r.Context(), hash[:], person.ID, app, env, now, now.Add(24*time.Hour)); err != nil {
		http.Error(w, "Homelab couldn't open that app. Try again.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "homelab_app_" + app + "_" + env, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 24 * 60 * 60})
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// person is the signed-in household member an app receives.
func (h *Hub) person(v core.Viewer) *identity.Person {
	if v.PersonID != "" {
		p, err := h.store.Person(context.Background(), v.PersonID)
		if err != nil || p.Status != "signed_in" {
			return nil
		}
		groups := make([]string, 0, len(p.Roles)+len(p.Grants))
		for _, role := range p.Roles {
			groups = append(groups, "role:"+role)
		}
		for app := range p.Grants {
			groups = append(groups, "app:"+app)
		}
		sort.Strings(groups)
		return &identity.Person{User: firstNonEmpty(p.Email, p.ID), Name: p.Name, Groups: groups}
	}
	// A local connection isn't a household session.
	// The API session and app-session cookies provide real identities in milestone 3.
	return nil
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
