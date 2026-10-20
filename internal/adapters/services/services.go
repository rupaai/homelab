// Package services runs homelab-services, the Rust program that serves files, databases
// and data (platform services), and forwards calls to it.
//
// homelabd is its only caller. It starts the program next to itself, restarts it when it
// stops, and forwards /api/files, /api/db and /api/data to it on 127.0.0.1. Every forwarded
// call carries who is calling, signed with a key homelabd makes at each start: the program
// gets only the public half, and refuses anything unsigned, old, or signed for another
// request.
package services

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// The signed caller: these names and the format are the contract with homelab-services.
const (
	HeaderCaller    = "X-Homelab-Caller"
	HeaderSignature = "X-Homelab-Signature"
	signContext     = "homelab-services/v1\n"
)

// signed is the caller as homelab-services receives it: who, plus which request it is
// for and when.
type signed struct {
	V int `json:"v"`
	core.ServiceCaller
	Method string `json:"method"`
	Path   string `json:"path"`
	TS     int64  `json:"ts"`
}

// Program is where homelab-services sits for a homelabd at exe: next to it, with the
// same ending. bin/homelabd-build-7 runs bin/homelab-services-build-7.
func Program(exe string) string {
	dir, base := filepath.Split(exe)
	if rest, ok := strings.CutPrefix(base, "homelabd"); ok {
		return filepath.Join(dir, "homelab-services"+rest)
	}
	return filepath.Join(dir, "homelab-services")
}

// Options configures Services.
type Options struct {
	Program   string      // the homelab-services program
	DataDir   string      // its own state
	Port      int         // it listens on 127.0.0.1:Port
	Storage   []core.Disk // where it keeps files
	MaxUpload int64       // the largest upload it takes, in bytes
	Logger    *slog.Logger
}

// Services runs homelab-services and forwards calls to it.
type Services struct {
	o     Options
	addr  string
	key   ed25519.PrivateKey
	proxy *httputil.ReverseProxy
	now   func() time.Time

	mu      sync.Mutex
	up      bool
	problem string
}

type callerKey struct{}

// New prepares Services. Run starts the program.
func New(o Options) (*Services, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := &Services{o: o, addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)), key: key, now: time.Now,
		problem: "Homelab's services are starting."}
	target := &url.URL{Scheme: "http", Host: s.addr}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			strip(pr.Out.Header)
			c, _ := pr.In.Context().Value(callerKey{}).(core.ServiceCaller)
			s.sign(pr.Out, c)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.o.Logger.Warn("homelab-services didn't answer", "err", err)
			unavailable(w)
		},
	}
	return s, nil
}

// strip removes anything a caller could use to pretend to be someone else.
func strip(h http.Header) {
	h.Del("Cookie")
	h.Del("Authorization")
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-homelab-") {
			h.Del(name)
		}
	}
}

// sign puts the signed caller on an outgoing request. The method and path are the ones
// the request is sent with, so the signature is good for this request only.
func (s *Services) sign(out *http.Request, c core.ServiceCaller) {
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	b, _ := json.Marshal(signed{V: 1, ServiceCaller: c, Method: out.Method, Path: out.URL.RequestURI(), TS: s.now().Unix()})
	value := base64.RawURLEncoding.EncodeToString(b)
	sig := ed25519.Sign(s.key, []byte(signContext+value))
	out.Header.Set(HeaderCaller, value)
	out.Header.Set(HeaderSignature, base64.RawURLEncoding.EncodeToString(sig))
}

// PublicKey is what homelab-services checks signatures with.
func (s *Services) PublicKey() string {
	return base64.RawURLEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

// Forward sends a call to homelab-services on behalf of c.
func (s *Services) Forward(w http.ResponseWriter, r *http.Request, c core.ServiceCaller) {
	if !s.Up() {
		unavailable(w)
		return
	}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
}

func unavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"Homelab's services aren't running right now. Try again in a minute."}` + "\n"))
}

// Up says whether homelab-services is running and answering.
func (s *Services) Up() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.up
}

// Problem says what's wrong with homelab-services, or "" when it runs.
func (s *Services) Problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.up {
		return ""
	}
	return s.problem
}

func (s *Services) set(up bool, problem string) {
	s.mu.Lock()
	s.up, s.problem = up, problem
	s.mu.Unlock()
}

// Restart waits: after the program stops, it starts again after a pause that grows to
// maxPause while it keeps stopping, and goes back to minPause once it has run a while.
const (
	minPause = time.Second
	maxPause = 30 * time.Second
	steady   = time.Minute
)

// Run starts homelab-services and keeps it running until ctx ends.
func (s *Services) Run(ctx context.Context) {
	if _, err := os.Stat(s.o.Program); err != nil {
		s.set(false, "Homelab's services aren't installed next to homelabd ("+filepath.Base(s.o.Program)+").")
		s.o.Logger.Warn("homelab-services isn't installed; files, databases and data are off", "program", s.o.Program)
		return
	}
	pause := minPause
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		s.set(false, fmt.Sprintf("Homelab's services stopped (%v). Starting them again.", err))
		s.o.Logger.Warn("homelab-services stopped; starting it again", "err", err, "in", pause)
		if time.Since(started) > steady {
			pause = minPause
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, maxPause)
	}
}

// runOnce runs the program until it stops.
func (s *Services) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, s.o.Program)
	cmd.Env = []string{
		"HOMELAB_SERVICES_LISTEN=" + s.addr,
		"HOMELAB_SERVICES_DATA=" + s.o.DataDir,
		"HOMELAB_SERVICES_KEY=" + s.PublicKey(),
		"PATH=" + os.Getenv("PATH"),
	}
	storage, _ := json.Marshal(s.o.Storage)
	cmd.Env = append(cmd.Env, "HOMELAB_SERVICES_STORAGE="+string(storage))
	if s.o.MaxUpload > 0 {
		cmd.Env = append(cmd.Env, "HOMELAB_SERVICES_MAX_UPLOAD="+strconv.FormatInt(s.o.MaxUpload, 10))
	}
	logs := &lineLogger{log: s.o.Logger}
	cmd.Stdout, cmd.Stderr = logs, logs
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := s.waitHealthy(ctx, done)
	if ready {
		s.set(true, "")
		s.o.Logger.Info("homelab-services is running", "listen", s.addr)
	}
	err := <-done
	s.set(false, "")
	if err == nil {
		err = errors.New("it exited")
	}
	return err
}

// waitHealthy polls /healthz until it answers, for up to 20 seconds, or until the program
// stops. A stopped program's result is put back for runOnce.
func (s *Services) waitHealthy(ctx context.Context, done chan error) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 80; i++ {
		select {
		case err := <-done:
			done <- err
			return false
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
		res, err := client.Get("http://" + s.addr + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	s.set(false, "Homelab's services started but don't answer.")
	return false
}

// lineLogger passes the program's own log lines on to Homelab's log.
type lineLogger struct {
	log *slog.Logger
	buf []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(l.buf[:i])); line != "" {
			l.log.Info("homelab-services: " + line)
		}
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 64<<10 { // a very long line without an end
		l.log.Info("homelab-services: " + string(l.buf))
		l.buf = nil
	}
	return len(p), nil
}

var _ core.Services = (*Services)(nil)
