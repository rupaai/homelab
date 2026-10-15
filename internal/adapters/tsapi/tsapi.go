// Package tsapi adapts Tailscale's documented REST API to core.RemoteAccess.
//
// It signs in with an OAuth client (client credentials) and keeps the short-lived access
// token in memory only; neither the secret nor the token is ever put in an error or a log.
package tsapi

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

// Options configures a Client.
type Options struct {
	ClientID     string
	ClientSecret string
	Tailnet      string // default "-", the tailnet the credential belongs to
	BaseURL      string // default https://api.tailscale.com
	HTTPClient   *http.Client
	Now          func() time.Time
}

// Client implements core.RemoteAccess using Tailscale's REST API.
type Client struct {
	base    string
	id      string
	secret  string
	tailnet string
	http    *http.Client
	now     func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

var _ core.RemoteAccess = (*Client)(nil)

const refreshMargin = time.Minute

func New(o Options) (*Client, error) {
	if strings.TrimSpace(o.ClientID) == "" || strings.TrimSpace(o.ClientSecret) == "" {
		return nil, fmt.Errorf("Add the Tailscale OAuth client in Settings first")
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.tailscale.com"
	}
	u, err := url.Parse(strings.TrimRight(o.BaseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("Tailscale address must be a full URL")
	}
	if o.Tailnet == "" {
		o.Tailnet = "-"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Client{base: u.String(), id: o.ClientID, secret: o.ClientSecret, tailnet: o.Tailnet, http: o.HTTPClient, now: o.Now}, nil
}

// Scopes the OAuth client needs for each kind of call.
const (
	scopeKeys    = "auth_keys"
	scopeDevices = "devices:core"
	scopePolicy  = "policy_file"
	scopeUsers   = "users"
)

// apiError is a non-success answer from Tailscale.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func newAPIError(status int, body []byte, scope string) *apiError {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &apiError{status, fmt.Sprintf("Tailscale refused Homelab's credentials. Check the OAuth client in Settings and its scopes (this needs: %s)", scope)}
	case http.StatusTooManyRequests:
		return &apiError{status, "Tailscale asked Homelab to slow down. Wait a minute and try again"}
	}
	var e struct {
		Message string `json:"message"`
	}
	text := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		text = e.Message
	}
	return &apiError{status, fmt.Sprintf("Tailscale returned %d %s: %s", status, http.StatusText(status), text)}
}

func readLimited(r io.Reader, n int64) ([]byte, error) { return io.ReadAll(io.LimitReader(r, n)) }

// accessToken returns a cached token, fetching a new one shortly before the old one expires.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Add(refreshMargin).Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.id}, "client_secret": {c.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("can't reach Tailscale: %w", errors.Unwrap(err))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := readLimited(res.Body, 4096)
		return "", newAPIError(res.StatusCode, body, "the scopes Homelab uses")
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&t); err != nil || t.AccessToken == "" {
		return "", fmt.Errorf("Tailscale didn't hand back a sign-in token")
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 3600
	}
	c.token = t.AccessToken
	c.expires = c.now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return c.token, nil
}

// do sends one authenticated request and returns the answer's headers and body.
func (c *Client) do(ctx context.Context, method, route, scope string, body []byte, header map[string]string) (http.Header, []byte, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, nil, err
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v2"+route, r)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("can't reach Tailscale: %w", errors.Unwrap(err))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := readLimited(res.Body, 4096)
		return nil, nil, newAPIError(res.StatusCode, msg, scope)
	}
	data, err := readLimited(res.Body, 8<<20)
	return res.Header, data, err
}

func (c *Client) call(ctx context.Context, method, route, scope string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	_, data, err := c.do(ctx, method, route, scope, body, nil)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("Tailscale sent an answer Homelab couldn't read")
	}
	return nil
}

func (c *Client) tn(suffix string) string { return "/tailnet/" + url.PathEscape(c.tailnet) + suffix }

// Invite asks Tailscale to invite email to the tailnet as a member.
func (c *Client) Invite(ctx context.Context, email string) (core.RemoteInvite, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return core.RemoteInvite{}, fmt.Errorf("An email address is needed to invite someone")
	}
	// Unverified: Tailscale's docs list user invites for reading and deleting, but this create
	// call (POST user-invites with a list of {role, email}) isn't documented.
	var out []struct {
		ID        string `json:"id"`
		InviteURL string `json:"inviteUrl"`
	}
	in := []map[string]string{{"role": "member", "email": email}}
	if err := c.call(ctx, http.MethodPost, c.tn("/user-invites"), scopeUsers, in, &out); err != nil {
		return core.RemoteInvite{}, err
	}
	if len(out) == 0 || out[0].InviteURL == "" {
		return core.RemoteInvite{}, fmt.Errorf("Tailscale didn't give an invite link for %s. They may already be on the tailnet", email)
	}
	return core.RemoteInvite{ID: out[0].ID, URL: out[0].InviteURL}, nil
}

// CancelInvite withdraws an invite that hasn't been accepted.
func (c *Client) CancelInvite(ctx context.Context, inviteID string) error {
	if inviteID == "" {
		return nil
	}
	err := c.call(ctx, http.MethodDelete, "/user-invites/"+url.PathEscape(inviteID), scopeUsers, nil, nil)
	if ae := (*apiError)(nil); errors.As(err, &ae) && ae.status == http.StatusNotFound {
		return nil
	}
	return err
}

// AuthKey mints a single-use, pre-approved key for a device carrying tags.
func (c *Client) AuthKey(ctx context.Context, tags []string) (string, error) {
	if len(tags) == 0 {
		return "", fmt.Errorf("A key for a Homelab address needs at least one tag")
	}
	for _, t := range tags {
		if !tagRe.MatchString(t) {
			return "", fmt.Errorf("%q can't be used as a tag; it should look like tag:name", t)
		}
	}
	in := map[string]any{
		"capabilities": map[string]any{"devices": map[string]any{"create": map[string]any{
			"reusable": false, "ephemeral": false, "preauthorized": true, "tags": tags,
		}}},
		"expirySeconds": 3600,
		"description":   "homelab",
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := c.call(ctx, http.MethodPost, c.tn("/keys"), scopeKeys, in, &out); err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", fmt.Errorf("Tailscale didn't hand back a key")
	}
	return out.Key, nil
}

// SetPolicy replaces the tailnet's access rules, unless they already match.
func (c *Client) SetPolicy(ctx context.Context, p core.AccessPolicy) error {
	want, err := PolicyJSON(p)
	if err != nil {
		return err
	}
	wantNorm, _ := normalizeJSON(want)
	for attempt := 0; ; attempt++ {
		hdr, cur, err := c.do(ctx, http.MethodGet, c.tn("/acl"), scopePolicy, nil, map[string]string{"Accept": "application/json"})
		if err != nil {
			return err
		}
		if curNorm, ok := normalizeJSON(cur); ok && curNorm == wantNorm {
			return nil
		}
		_, _, err = c.do(ctx, http.MethodPost, c.tn("/acl"), scopePolicy, want, map[string]string{"If-Match": hdr.Get("ETag")})
		if ae := (*apiError)(nil); errors.As(err, &ae) && ae.status == http.StatusPreconditionFailed && attempt == 0 {
			continue
		}
		if ae := (*apiError)(nil); errors.As(err, &ae) && ae.status == http.StatusPreconditionFailed {
			return fmt.Errorf("The access rules changed in Tailscale while Homelab was saving them. Try again")
		}
		return err
	}
}

type userDTO struct {
	ID          string `json:"id"`
	LoginName   string `json:"loginName"`
	DisplayName string `json:"displayName"`
}

type deviceDTO struct {
	ID                 string   `json:"id"`
	NodeID             string   `json:"nodeId"`
	Name               string   `json:"name"`
	Hostname           string   `json:"hostname"`
	User               string   `json:"user"`
	Tags               []string `json:"tags"`
	LastSeen           string   `json:"lastSeen"`
	ConnectedToControl bool     `json:"connectedToControl"`
}

func (d deviceDTO) id() string {
	if d.NodeID != "" {
		return d.NodeID
	}
	return d.ID
}

func (c *Client) users(ctx context.Context) ([]userDTO, error) {
	var out struct {
		Users []userDTO `json:"users"`
	}
	err := c.call(ctx, http.MethodGet, c.tn("/users"), scopeUsers, nil, &out)
	return out.Users, err
}

func (c *Client) devices(ctx context.Context) ([]deviceDTO, error) {
	var out struct {
		Devices []deviceDTO `json:"devices"`
	}
	err := c.call(ctx, http.MethodGet, c.tn("/devices"), scopeDevices, nil, &out)
	return out.Devices, err
}

// Observe lists the tailnet's people, devices and pending invites.
func (c *Client) Observe(ctx context.Context) (core.RemoteState, error) {
	st := core.RemoteState{Users: []core.RemoteUser{}, Devices: []core.RemoteDevice{}, Invites: []core.RemoteInvite{}}
	users, err := c.users(ctx)
	if err != nil {
		return st, err
	}
	for _, u := range users {
		st.Users = append(st.Users, core.RemoteUser{ID: u.ID, Login: u.LoginName, Name: u.DisplayName})
	}
	devs, err := c.devices(ctx)
	if err != nil {
		return st, err
	}
	for _, d := range devs {
		name := d.Name
		if name == "" {
			name = d.Hostname
		}
		rd := core.RemoteDevice{ID: d.id(), Name: name, Tags: append([]string{}, d.Tags...), Online: d.ConnectedToControl}
		if len(d.Tags) == 0 {
			rd.Login = d.User
		}
		if t, err := time.Parse(time.RFC3339, d.LastSeen); err == nil {
			rd.LastSeen = &t
		}
		st.Devices = append(st.Devices, rd)
	}
	var invites []struct {
		ID        string `json:"id"`
		InviteURL string `json:"inviteUrl"`
	}
	if err := c.call(ctx, http.MethodGet, c.tn("/user-invites"), scopeUsers, nil, &invites); err != nil {
		return st, err
	}
	for _, i := range invites {
		st.Invites = append(st.Invites, core.RemoteInvite{ID: i.ID, URL: i.InviteURL})
	}
	return st, nil
}

// RemoveDevices deletes every device that belongs to login. Tagged devices are Homelab's
// own and are left alone.
func (c *Client) RemoveDevices(ctx context.Context, login string) (int, error) {
	if strings.TrimSpace(login) == "" {
		return 0, nil
	}
	devs, err := c.devices(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, d := range devs {
		if len(d.Tags) > 0 || !strings.EqualFold(d.User, login) {
			continue
		}
		if err := c.call(ctx, http.MethodDelete, "/device/"+url.PathEscape(d.id()), scopeDevices, nil, nil); err != nil {
			errs = append(errs, fmt.Errorf("couldn't remove %s: %w", d.Name, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// RemoveUser takes login off the tailnet. Someone who is already gone is not an error.
func (c *Client) RemoveUser(ctx context.Context, login string) error {
	users, err := c.users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if strings.EqualFold(u.LoginName, login) {
			return c.call(ctx, http.MethodPost, "/users/"+url.PathEscape(u.ID)+"/delete", scopeUsers, nil, nil)
		}
	}
	return nil
}
