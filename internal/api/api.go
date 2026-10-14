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
	"strconv"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// Options configure the API.
type Options struct {
	Owners  []string // Tailscale logins allowed in; empty means any signed-in person
	Version string
	Static  fs.FS // the companion site root (index.html at the top)
	Logger  *slog.Logger
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
	hub    *hub.Hub
	owners map[string]bool
	ver    string
	log    *slog.Logger
	static *static
}

// New returns the handler for everything Homelab serves: /api, /healthz and the companion.
func New(h *hub.Hub, opts Options) http.Handler {
	s := &server{hub: h, owners: map[string]bool{}, ver: opts.Version, log: opts.Logger}
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
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/apps", s.listApps)
	mux.HandleFunc("POST /api/apps", s.addApp)
	mux.HandleFunc("GET /api/apps/{id}", s.getApp)
	mux.HandleFunc("POST /api/apps/{id}/deploy", s.deploy)
	mux.HandleFunc("GET /api/apps/{id}/promote", s.previewPromote)
	mux.HandleFunc("POST /api/apps/{id}/promote", s.promote)
	mux.HandleFunc("GET /api/apps/{id}/envs/{env}/rollback", s.previewRollback)
	mux.HandleFunc("POST /api/apps/{id}/envs/{env}/rollback", s.rollback)
	mux.HandleFunc("GET /api/apps/{id}/envs/{env}/logs", s.logs)
	mux.HandleFunc("GET /api/activity", s.activity)
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
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			hd.Set("Cache-Control", "no-store")
			if !s.guard(w, r) {
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// allowed says whether the viewer may use Homelab.
func (s *server) allowed(v core.Viewer) bool {
	switch {
	case v.Tagged:
		return false // a server on the tailnet, not a person
	case v.Local:
		return true
	case v.Login != "" && s.owners[strings.ToLower(v.Login)]:
		return true
	case len(s.owners) == 0 && v.Login != "" && !v.Tagged:
		return true
	}
	return false
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
		writeErr(w, http.StatusForbidden, msgNotOwner)
		return false
	}
	if v.Local && !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, "Open the companion at http://127.0.0.1:7080 on this computer.")
		return false
	}
	if r.Method == http.MethodPost {
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
	writeJSON(w, http.StatusOK, map[string]any{"login": v.Login, "name": v.Name, "local": v.Local})
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
	writeJSON(w, http.StatusOK, v)
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
