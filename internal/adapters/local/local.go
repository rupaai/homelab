// Package local serves addresses on 127.0.0.1 ports, for trying Homehub out without
// Tailscale. Each name gets its own port, assigned once and remembered in ports.json.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

var _ core.Addresses = (*Addresses)(nil)

const companionName = "homehub"

// Options configures the adapter.
type Options struct {
	Dir           string // where ports.json is kept
	Bind          string // default 127.0.0.1
	CompanionPort int    // default 7080; the port of the name "homehub"
	BasePort      int    // default 7100; first port handed to other names
	Logger        *slog.Logger
}

type handlerBox struct{ h http.Handler }

type entry struct {
	name string
	port int
	h    atomic.Pointer[handlerBox]
	srv  *http.Server
	ln   net.Listener
	err  string
}

// Addresses is the local core.Addresses adapter.
type Addresses struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	ports   map[string]int
	entries map[string]*entry
	errs    map[string]string // listen failures for names that have no running entry
}

// New creates the adapter and loads remembered port assignments.
func New(opts Options) (*Addresses, error) {
	if opts.Bind == "" {
		opts.Bind = "127.0.0.1"
	}
	if opts.CompanionPort == 0 {
		opts.CompanionPort = 7080
	}
	if opts.BasePort == 0 {
		opts.BasePort = 7100
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	a := &Addresses{opts: opts, log: opts.Logger, ports: map[string]int{}, entries: map[string]*entry{}, errs: map[string]string{}}
	if opts.Dir != "" {
		if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(a.portsFile())
		if err == nil {
			if err := json.Unmarshal(b, &a.ports); err != nil {
				return nil, fmt.Errorf("read %s: %w", a.portsFile(), err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return a, nil
}

func (a *Addresses) portsFile() string { return filepath.Join(a.opts.Dir, "ports.json") }

func (a *Addresses) savePorts() error {
	if a.opts.Dir == "" {
		return nil
	}
	b, err := json.MarshalIndent(a.ports, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.portsFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.portsFile())
}

// nextPort picks the lowest port from BasePort that no name uses. Caller holds a.mu.
func (a *Addresses) nextPort() int {
	used := map[int]bool{a.opts.CompanionPort: true}
	for _, p := range a.ports {
		used[p] = true
	}
	p := a.opts.BasePort
	for used[p] {
		p++
	}
	return p
}

func (a *Addresses) listen(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(a.opts.Bind, strconv.Itoa(port)))
}

func (a *Addresses) wrap(e *entry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(core.WithViewer(r.Context(), core.Viewer{Local: true}))
		e.h.Load().h.ServeHTTP(w, r)
	})
}

// Ensure serves handler at name; a repeated call swaps the handler without re-listening.
func (a *Addresses) Ensure(_ context.Context, name string, handler http.Handler) error {
	if handler == nil {
		return errors.New("local: nil handler")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.entries[name]; ok {
		e.h.Store(&handlerBox{handler})
		return nil
	}
	port, assigned := a.ports[name]
	if name == companionName {
		port, assigned = a.opts.CompanionPort, true
	}
	var ln net.Listener
	var err error
	if assigned {
		ln, err = a.listen(port)
	} else {
		// New name: take the next free port, skipping any another program holds.
		for i := 0; i < 100; i++ {
			port = a.nextPort()
			if ln, err = a.listen(port); err == nil {
				break
			}
			a.ports[name] = port // reserve so nextPort moves on; undone below on failure
		}
		delete(a.ports, name)
	}
	if err != nil {
		a.errs[name] = fmt.Sprintf("Couldn't listen on port %d: %v", port, err)
		a.log.Error("local address", "name", name, "port", port, "err", err)
		return nil // reported through Observe
	}
	delete(a.errs, name)
	if name != companionName {
		a.ports[name] = port
		if err := a.savePorts(); err != nil {
			a.log.Warn("save ports", "err", err)
		}
	}
	e := &entry{name: name, port: port, ln: ln}
	e.h.Store(&handlerBox{handler})
	e.srv = &http.Server{Handler: a.wrap(e), ReadHeaderTimeout: 30 * time.Second}
	a.entries[name] = e
	go func() {
		if err := e.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("local serve", "name", name, "err", err)
		}
	}()
	return nil
}

// Remove closes the listener; the port assignment is kept.
func (a *Addresses) Remove(_ context.Context, name string) error {
	a.mu.Lock()
	e := a.entries[name]
	delete(a.entries, name)
	delete(a.errs, name)
	a.mu.Unlock()
	if e != nil {
		return e.srv.Close()
	}
	return nil
}

// Observe reports every address.
func (a *Addresses) Observe() map[string]core.Address {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]core.Address{}
	for n, e := range a.entries {
		out[n] = core.Address{Name: n, URL: "http://127.0.0.1:" + strconv.Itoa(e.port), Ready: true}
	}
	for n, msg := range a.errs {
		out[n] = core.Address{Name: n, Error: msg}
	}
	return out
}

// Close stops every listener.
func (a *Addresses) Close() error {
	a.mu.Lock()
	es := a.entries
	a.entries = map[string]*entry{}
	a.mu.Unlock()
	var first error
	for _, e := range es {
		if err := e.srv.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
