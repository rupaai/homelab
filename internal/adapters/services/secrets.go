package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Secrets is core.Secrets backed by homelab-services: the encrypted store lives there.
//
// homelabd calls it as the "system" caller. Only homelabd can sign that: the key that signs
// every call exists only in this process, homelab-services holds just its public half, and
// Forward (the path every request from people, API keys and apps takes) refuses a system
// caller. Homelab's own rules (the owner, the real sign-in) are applied by the hub and the
// API before they get here; homelab-services trusts them for system calls, and applies its
// own rules to everyone else.
//
// While homelab-services isn't running, every operation fails with core.ErrSecretsUnavailable.
// Nothing falls back to plain text, and nothing is skipped.
type Secrets struct {
	s *Services

	mu    sync.Mutex
	cache map[string]cached // platform values, read often (GITHUB_TOKEN on every request)
}

type cached struct {
	value string
	at    time.Time
}

// platformTTL is how long a platform value is remembered. A change made here clears it at once.
const platformTTL = 30 * time.Second

// callTimeout bounds one call to homelab-services.
const callTimeout = 20 * time.Second

var _ core.Secrets = (*Secrets)(nil)

// Secrets returns the secrets client.
func (s *Services) Secrets() *Secrets {
	return &Secrets{s: s, cache: map[string]cached{}}
}

// permanent is a reason the secrets service is up but won't work (no key, a wrong key).
type permanent struct{ msg string }

func (e *permanent) Error() string { return e.msg }

// reply is what homelab-services answered.
type reply struct {
	status int
	body   []byte
}

// call sends one system call. A service that is down, or doesn't answer, is
// core.ErrSecretsUnavailable. Failed answers become plain errors with the service's message.
func (c *Secrets) call(method, path string, query url.Values, in any) (reply, error) {
	if !c.s.Up() {
		return reply{}, core.ErrSecretsUnavailable
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return reply{}, err
		}
		body = bytes.NewReader(b)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.s.addr+target, body)
	if err != nil {
		return reply{}, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.s.sign(req, core.ServiceCaller{Kind: core.ServiceKindSystem})
	res, err := c.s.client.Do(req)
	if err != nil {
		c.s.o.Logger.Warn("homelab-services didn't answer a secrets call", "err", err)
		return reply{}, core.ErrSecretsUnavailable
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return reply{}, core.ErrSecretsUnavailable
	}
	r := reply{status: res.StatusCode, body: b}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return r, nil
	}
	msg := serviceMessage(b)
	switch {
	case res.StatusCode == http.StatusServiceUnavailable:
		c.s.o.Logger.Warn("the secrets service is not available", "why", msg)
		return r, &permanent{msg: strings.TrimSuffix(msg, ".")}
	case res.StatusCode >= 500:
		c.s.o.Logger.Error("the secrets service failed", "status", res.StatusCode, "why", msg)
		return r, errors.New("the secrets service had a problem; look in Homelab's log")
	}
	if msg == "" {
		msg = "the secrets service refused that"
	}
	return r, &refused{status: res.StatusCode, msg: strings.TrimSuffix(msg, ".")}
}

// refused is a call the service understood and said no to (a bad name, a clash).
type refused struct {
	status int
	msg    string
}

func (e *refused) Error() string { return e.msg }

func serviceMessage(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	return e.Error
}

// down turns the service's "not set up" answer into the plain error operations return.
func down(err error) error {
	var p *permanent
	if errors.As(err, &p) {
		return core.ErrSecretsUnavailable
	}
	return err
}

// Check says whether secrets work. A service that is still starting is
// core.ErrSecretsUnavailable. One that is up but can't use secrets (no key, the wrong key)
// gives the reason, which won't change by waiting.
func (c *Secrets) Check() error {
	_, err := c.call("GET", "/internal/secrets", url.Values{"kind": {"config"}}, nil)
	return err
}

// IsPermanent says whether err from Check is a problem that waiting won't fix.
func IsPermanent(err error) bool {
	var p *permanent
	return errors.As(err, &p)
}

func place(app, env string) url.Values {
	q := url.Values{}
	if app != "" {
		q.Set("app", app)
	}
	if env != "" {
		q.Set("env", env)
	}
	return q
}

func item(name string) string { return "/internal/secrets/item/" + url.PathEscape(name) }

type infoJSON struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

func (c *Secrets) list(app, env, kind string) ([]core.SecretInfo, error) {
	q := place(app, env)
	q.Set("kind", kind)
	r, err := c.call("GET", "/internal/secrets", q, nil)
	if err != nil {
		return nil, down(err)
	}
	var out struct {
		Secrets []infoJSON `json:"secrets"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		return nil, err
	}
	var infos []core.SecretInfo
	for _, i := range out.Secrets {
		infos = append(infos, core.SecretInfo{Name: i.Name, Version: i.Version, UpdatedBy: i.UpdatedBy, UpdatedAt: time.Unix(i.UpdatedAt, 0)})
	}
	return infos, nil
}

// List describes the config secrets of app/env (app "" for platform), never the values.
func (c *Secrets) List(app, env string) ([]core.SecretInfo, error) { return c.list(app, env, "config") }

// ListSaved describes the secrets the app saved itself, never the values.
func (c *Secrets) ListSaved(app, env string) ([]core.SecretInfo, error) {
	if app == "" {
		return nil, errors.New("ListSaved needs an app")
	}
	return c.list(app, env, "app")
}

type namesIn struct {
	App   string   `json:"app"`
	Env   string   `json:"env"`
	Names []string `json:"names"`
}

// Missing lists which of names have no config value for app/env.
func (c *Secrets) Missing(app, env string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	r, err := c.call("POST", "/internal/secrets/missing", nil, namesIn{app, env, names})
	if err != nil {
		return nil, down(err)
	}
	var out struct {
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		return nil, err
	}
	return out.Missing, nil
}

// Inject returns the config values of names that are set for app/env, for a deploy.
func (c *Secrets) Inject(app, env string, names []string) (map[string]string, error) {
	if app == "" {
		return nil, errors.New("Inject needs an app")
	}
	return c.values(app, env, names)
}

func (c *Secrets) values(app, env string, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	r, err := c.call("POST", "/internal/secrets/values", nil, namesIn{app, env, names})
	if err != nil {
		return nil, down(err)
	}
	var got struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(r.body, &got); err != nil {
		return nil, err
	}
	for k, v := range got.Values {
		out[k] = v
	}
	return out, nil
}

// Platform returns a Homelab credential; ok is false when it is not set or can't be read.
func (c *Secrets) Platform(name string) (string, bool) {
	v, err := c.LoadPlatform(name)
	return v, err == nil && v != ""
}

// LoadPlatform returns a Homelab credential. The value is "" when it is not set; err is set
// when the secrets can't be read, so callers don't mistake that for "not set".
func (c *Secrets) LoadPlatform(name string) (string, error) {
	c.mu.Lock()
	hit, ok := c.cache[name]
	c.mu.Unlock()
	if ok && time.Since(hit.at) < platformTTL {
		return hit.value, nil
	}
	vals, err := c.values("", "", []string{name})
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.cache[name] = cached{value: vals[name], at: time.Now()}
	c.mu.Unlock()
	return vals[name], nil
}

func (c *Secrets) forget(app, name string) {
	if app != "" {
		return
	}
	c.mu.Lock()
	delete(c.cache, name)
	c.mu.Unlock()
}

// Set stores (or replaces) a config value. app "" means a platform credential.
func (c *Secrets) Set(app, env, name, value, by string) error {
	defer c.forget(app, name)
	_, err := c.call("PUT", item(name), place(app, env), map[string]string{"value": value, "kind": "config", "by": by})
	return down(err)
}

// Delete removes a config value. Deleting one that isn't set is not an error.
func (c *Secrets) Delete(app, env, name string) error {
	defer c.forget(app, name)
	q := place(app, env)
	q.Set("kind", "config")
	_, err := c.call("DELETE", item(name), q, nil)
	return down(err)
}

// DeleteSaved removes a secret the app saved. Removing one that isn't there is not an error.
func (c *Secrets) DeleteSaved(app, env, name string) error {
	if app == "" {
		return errors.New("DeleteSaved needs an app")
	}
	q := place(app, env)
	q.Set("kind", "app")
	_, err := c.call("DELETE", item(name), q, nil)
	return down(err)
}

// DeleteApp removes every secret of an app, in every environment and of both kinds.
func (c *Secrets) DeleteApp(app string) error {
	if app == "" {
		return errors.New("DeleteApp needs an app")
	}
	_, err := c.call("DELETE", "/internal/secrets/app/"+url.PathEscape(app), nil, nil)
	return down(err)
}

// Versions lists the kept values of one secret, newest first, without the values.
func (c *Secrets) Versions(app, env, name string) ([]core.SecretVersion, error) {
	r, err := c.call("GET", item(name)+"/versions", place(app, env), nil)
	if err != nil {
		return nil, down(err)
	}
	var out struct {
		Versions []struct {
			Version int    `json:"version"`
			SetAt   int64  `json:"set_at"`
			SetBy   string `json:"set_by"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		return nil, err
	}
	var vs []core.SecretVersion
	for _, v := range out.Versions {
		vs = append(vs, core.SecretVersion{Version: v.Version, SetBy: v.SetBy, SetAt: time.Unix(v.SetAt, 0)})
	}
	return vs, nil
}

// Restore stores an old version's value again as a new version, and returns its number.
func (c *Secrets) Restore(app, env, name string, version int, by string) (int, error) {
	defer c.forget(app, name)
	r, err := c.call("POST", item(name)+"/restore", place(app, env), map[string]any{"version": version, "by": by})
	if err != nil {
		var rf *refused
		if errors.As(err, &rf) && rf.status == http.StatusNotFound {
			return 0, fmt.Errorf("there's no version %d of %s", version, name)
		}
		return 0, down(err)
	}
	var out struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		return 0, err
	}
	return out.Version, nil
}

// GetSaved returns the value of one secret the app saved; ok is false when it isn't there.
func (c *Secrets) GetSaved(app, env, name string) (string, bool, error) {
	if app == "" {
		return "", false, errors.New("GetSaved needs an app")
	}
	r, err := c.call("GET", item(name), place(app, env), nil)
	if err != nil {
		var rf *refused
		if errors.As(err, &rf) && rf.status == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, down(err)
	}
	var out struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		return "", false, err
	}
	// Only the app's own secrets come out here. A config secret has its own route.
	if out.Kind != "app" {
		return "", false, nil
	}
	return out.Value, true, nil
}
