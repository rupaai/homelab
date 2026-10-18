// Package tailscale serves each address as its own Tailscale node (tsnet), reachable at
// https://<name>.<tailnet>.ts.net with a certificate from Tailscale.
package tailscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tailscale.com/tsnet"

	"github.com/rupaai/homelab/internal/core"
)

var _ core.Addresses = (*Addresses)(nil)

const (
	msgMissingKey = "Add the Tailscale OAuth client in Settings, or TS_AUTHKEY to secrets/platform.env"
	msgHTTPS      = "Turn on HTTPS certificates in the Tailscale admin console (DNS page): enable MagicDNS, then HTTPS Certificates"
)

// Options configures the adapter.
type Options struct {
	StateDir string // one subfolder per node
	AuthKey  string // only needed the first time a node logs in
	Tags     []string
	Logger   *slog.Logger
	// TagsFor, when set, gives each address its own tags instead of Tags.
	TagsFor func(name string) []string
	// MintKey, when set, makes a single-use key for a node's first login instead of AuthKey.
	MintKey func(ctx context.Context, tags []string) (string, error)
}

// Who is the person or device behind a connection.
type Who struct {
	Login  string
	Name   string
	Tagged bool
}

// node is the part of a tsnet.Server this package uses; tests replace it.
type node interface {
	// Up connects to the tailnet and returns the node's MagicDNS name (may end with a dot).
	Up(ctx context.Context) (dnsName string, err error)
	// ListenTLS listens on the tailnet with a Tailscale certificate.
	ListenTLS(addr string) (net.Listener, error)
	// Listen listens on the tailnet without TLS.
	Listen(addr string) (net.Listener, error)
	WhoIs(ctx context.Context, remoteAddr string) (Who, error)
	Close() error
}

type nodeFactory func(name, dir string, o Options, log *slog.Logger) node

type handlerBox struct{ h http.Handler }

type entry struct {
	name   string
	h      atomic.Pointer[handlerBox]
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	node  node
	url   string
	ready bool
	err   string
}

func (e *entry) set(f func()) { e.mu.Lock(); f(); e.mu.Unlock() }

// Addresses is the Tailscale core.Addresses adapter.
type Addresses struct {
	opts    Options
	log     *slog.Logger
	newNode nodeFactory

	// Backoff between failed start attempts; fields so tests can shorten them.
	upTimeout  time.Duration
	backoffMin time.Duration
	backoffMax time.Duration

	mu      sync.Mutex
	entries map[string]*entry
	closed  bool
}

// New creates the adapter. Nothing connects until Ensure is called.
func New(opts Options) (*Addresses, error) {
	if opts.StateDir == "" {
		return nil, errors.New("tailscale: StateDir is required")
	}
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, err
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Addresses{
		opts: opts, log: opts.Logger, newNode: newTSNode,
		upTimeout: 2 * time.Minute, backoffMin: 5 * time.Second, backoffMax: 2 * time.Minute,
		entries: map[string]*entry{},
	}, nil
}

// hasState reports whether the node in dir has logged in before.
func hasState(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "tailscaled.state"))
	return err == nil
}

// Ensure starts the node for name (once) and serves handler on it. A repeated call swaps
// the handler in place.
func (a *Addresses) Ensure(_ context.Context, name string, handler http.Handler) error {
	if handler == nil {
		return errors.New("tailscale: nil handler")
	}
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("tailscale: bad name %q", name)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("tailscale: closed")
	}
	if e, ok := a.entries[name]; ok {
		e.h.Store(&handlerBox{handler})
		return nil
	}
	e := &entry{name: name, done: make(chan struct{})}
	e.h.Store(&handlerBox{handler})
	a.entries[name] = e
	dir := filepath.Join(a.opts.StateDir, name)
	if a.opts.AuthKey == "" && a.opts.MintKey == nil && !hasState(dir) {
		e.err = msgMissingKey
		close(e.done)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go a.run(ctx, e, dir)
	return nil
}

// run brings the node up and serves, retrying with backoff until ctx ends.
func (a *Addresses) run(ctx context.Context, e *entry, dir string) {
	defer close(e.done)
	backoff := a.backoffMin
	for ctx.Err() == nil {
		err := a.serve(ctx, e, dir)
		if ctx.Err() != nil {
			return
		}
		msg := err.Error()
		if isHTTPSError(err) {
			msg = msgHTTPS
		}
		e.set(func() { e.err, e.ready = msg, false })
		a.log.Warn("tailscale node", "name", e.name, "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > a.backoffMax {
			backoff = a.backoffMax
		}
	}
}

func isHTTPSError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "enable HTTPS") || strings.Contains(s, "enable MagicDNS") ||
		strings.Contains(s, "tailscale.com/s/https")
}

// serve runs one attempt: start the node, listen, and serve until something fails.
func (a *Addresses) serve(ctx context.Context, e *entry, dir string) error {
	o := a.opts
	if o.TagsFor != nil {
		o.Tags = o.TagsFor(e.name)
	}
	if o.MintKey != nil && !hasState(dir) {
		key, err := o.MintKey(ctx, o.Tags)
		if err != nil {
			return fmt.Errorf("get a Tailscale key for %s: %w", e.name, err)
		}
		o.AuthKey = key
	}
	n := a.newNode(e.name, dir, o, a.log)
	e.set(func() { e.node = n })
	defer func() {
		n.Close()
		e.set(func() { e.node, e.ready = nil, false })
	}()

	upCtx, cancel := context.WithTimeout(ctx, a.upTimeout)
	dns, err := n.Up(upCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("connect to Tailscale: %w", err)
	}
	host := strings.TrimSuffix(dns, ".")
	if host == "" {
		return errors.New("Tailscale did not report a name for this node")
	}
	url := "https://" + host
	e.set(func() { e.url = url })

	tlsLn, err := n.ListenTLS(":443")
	if err != nil {
		return err
	}
	httpLn, err := n.Listen(":80")
	if err != nil {
		tlsLn.Close()
		return err
	}
	main := &http.Server{Handler: a.wrap(e, n), ReadHeaderTimeout: 30 * time.Second}
	redirect := &http.Server{Handler: redirectHandler(host), ReadHeaderTimeout: 30 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- main.Serve(tlsLn) }()
	go func() { errc <- redirect.Serve(httpLn) }()
	e.set(func() { e.ready, e.err = true, "" })
	a.log.Info("tailscale address ready", "name", e.name, "url", url)

	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-errc:
	}
	main.Close()
	redirect.Close()
	return err
}

func redirectHandler(host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// wrap puts the viewer (from WhoIs) into each request's context and calls the current handler.
func (a *Addresses) wrap(e *entry, n node) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		who, err := n.WhoIs(ctx, r.RemoteAddr)
		cancel()
		v := core.Viewer{}
		if err == nil {
			v = core.Viewer{Login: who.Login, Name: who.Name, Tagged: who.Tagged}
		}
		e.h.Load().h.ServeHTTP(w, r.WithContext(core.WithViewer(r.Context(), v)))
	})
}

// Remove closes the node; its state folder stays so it returns with the same name.
func (a *Addresses) Remove(_ context.Context, name string) error {
	a.mu.Lock()
	e := a.entries[name]
	delete(a.entries, name)
	a.mu.Unlock()
	stop(e)
	return nil
}

func stop(e *entry) {
	if e == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	<-e.done
}

// Observe reports every address.
func (a *Addresses) Observe() map[string]core.Address {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]core.Address, len(a.entries))
	for n, e := range a.entries {
		e.mu.Lock()
		out[n] = core.Address{Name: n, URL: e.url, Ready: e.ready, Error: e.err}
		e.mu.Unlock()
	}
	return out
}

// Close stops every node.
func (a *Addresses) Close() error {
	a.mu.Lock()
	a.closed = true
	es := a.entries
	a.entries = map[string]*entry{}
	a.mu.Unlock()
	for _, e := range es {
		stop(e)
	}
	return nil
}

// tsNode is the real node, a tsnet.Server.
type tsNode struct{ s *tsnet.Server }

func newTSNode(name, dir string, o Options, log *slog.Logger) node {
	debug := func(format string, args ...any) { log.Debug(fmt.Sprintf(format, args...), "node", name) }
	info := func(format string, args ...any) { log.Info(fmt.Sprintf(format, args...), "node", name) }
	return &tsNode{s: &tsnet.Server{
		Hostname:      name,
		Dir:           dir,
		AuthKey:       o.AuthKey,
		AdvertiseTags: o.Tags,
		Logf:          debug,
		UserLogf:      info,
	}}
}

func (t *tsNode) Up(ctx context.Context) (string, error) {
	st, err := t.s.Up(ctx)
	if err != nil {
		return "", err
	}
	if st.Self != nil && st.Self.DNSName != "" {
		return st.Self.DNSName, nil
	}
	if d := t.s.CertDomains(); len(d) > 0 {
		return d[0], nil
	}
	return "", nil
}

func (t *tsNode) ListenTLS(addr string) (net.Listener, error) { return t.s.ListenTLS("tcp", addr) }
func (t *tsNode) Listen(addr string) (net.Listener, error)    { return t.s.Listen("tcp", addr) }
func (t *tsNode) Close() error                                { return t.s.Close() }

func (t *tsNode) WhoIs(ctx context.Context, remoteAddr string) (Who, error) {
	lc, err := t.s.LocalClient()
	if err != nil {
		return Who{}, err
	}
	r, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Who{}, err
	}
	var w Who
	if r.UserProfile != nil {
		w.Login, w.Name = r.UserProfile.LoginName, r.UserProfile.DisplayName
	}
	if r.Node != nil {
		w.Tagged = r.Node.IsTagged()
	}
	return w, nil
}
