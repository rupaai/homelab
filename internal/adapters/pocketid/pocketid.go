// Package pocketid adapts Pocket ID's documented REST API to core.Identity.
package pocketid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Options identify the local Pocket ID service and its API key.
type Options struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Client implements core.Identity using Pocket ID's REST API.
type Client struct {
	base *url.URL
	key  string
	http *http.Client
}

func New(o Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(o.BaseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("Pocket ID address must be a full URL")
	}
	if o.APIKey == "" {
		return nil, fmt.Errorf("Pocket ID API key is not set")
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{base: u, key: o.APIKey, http: o.HTTPClient}, nil
}

type userDTO struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	Email       string     `json:"email"`
	Disabled    bool       `json:"disabled"`
	UserGroups  []groupDTO `json:"userGroups"`
}
type groupDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type page[T any] struct {
	Data []T `json:"data"`
}

// EnsureOIDCClient creates or updates Homelab's confidential OIDC client and installs
// the generated secret. Pocket ID returns the secret only on creation.
func (c *Client) EnsureOIDCClient(ctx context.Context, callback, secret string) (string, error) {
	const id = "homelab"
	var existing map[string]any
	err := c.request(ctx, http.MethodGet, "/api/oidc/clients/"+id, nil, &existing)
	body := map[string]any{"id": id, "name": "Homelab", "description": "Homelab companion sign-in", "callbackURLs": []string{callback}, "logoutCallbackURLs": []string{callback}, "pkceEnabled": true, "isPublic": false, "skipConsent": true, "requiresReauthentication": false}
	if err != nil {
		if !strings.Contains(err.Error(), "404") {
			return "", err
		}
		if err := c.request(ctx, http.MethodPost, "/api/oidc/clients", body, &existing); err != nil {
			return "", err
		}
	} else {
		if err := c.request(ctx, http.MethodPut, "/api/oidc/clients/"+id, body, &existing); err != nil {
			return "", err
		}
	}
	var secrets []struct {
		IsActive bool   `json:"isActive"`
		Prefix   string `json:"prefix"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/oidc/clients/"+id+"/secrets", nil, &secrets); err != nil {
		return "", err
	}
	active := false
	for _, item := range secrets {
		active = active || (item.IsActive && item.Prefix != "" && strings.HasPrefix(secret, item.Prefix))
	}
	if !active {
		var created struct {
			Secret string `json:"secret"`
		}
		if err := c.request(ctx, http.MethodPost, "/api/oidc/clients/"+id+"/secrets", map[string]string{"secret": secret}, &created); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (c *Client) request(ctx context.Context, method, route string, body any, out any) error {
	rel, err := url.Parse(route)
	if err != nil {
		return err
	}
	u := *c.base
	u.Path = path.Join(c.base.Path, rel.Path)
	u.RawQuery = rel.RawQuery
	var b io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		b = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), b)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&e)
		return fmt.Errorf("Pocket ID returned %s: %s", res.Status, strings.TrimSpace(e.Error))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

func (c *Client) groupID(ctx context.Context, name string) (string, error) {
	var p page[groupDTO]
	route := "/api/user-groups?search=" + url.QueryEscape(name) + "&pagination%5Blimit%5D=100"
	if err := c.request(ctx, http.MethodGet, route, nil, &p); err != nil {
		return "", err
	}
	for _, g := range p.Data {
		if g.Name == name {
			return g.ID, nil
		}
	}
	friendlyName := name
	if len(friendlyName) > 50 {
		friendlyName = friendlyName[:50]
	}
	var created groupDTO
	err := c.request(ctx, http.MethodPost, "/api/user-groups", map[string]string{"name": name, "friendlyName": friendlyName}, &created)
	return created.ID, err
}

// Ensure creates or updates a Pocket ID user and replaces their group memberships.
func (c *Client) Ensure(ctx context.Context, p core.Person, groups []string) (string, error) {
	if len(p.Name) == 0 || len([]rune(p.Name)) > 100 {
		return "", fmt.Errorf("Pocket ID display name must be between 1 and 100 characters")
	}
	groupIDs := make([]string, 0, len(groups))
	for _, name := range groups {
		id, err := c.groupID(ctx, name)
		if err != nil {
			return "", err
		}
		groupIDs = append(groupIDs, id)
	}
	sort.Strings(groupIDs)
	localID := p.ID
	providerUserID := p.IdentityID
	if providerUserID == "" {
		providerUserID = localID
	}
	username := "homelab-" + localID
	if len(username) > 50 {
		username = username[:50]
	}
	body := map[string]any{"username": username, "displayName": p.Name, "email": p.Email, "disabled": false, "isAdmin": false, "userGroupIds": groupIDs}
	var existing userDTO
	err := c.request(ctx, http.MethodGet, "/api/users/"+url.PathEscape(providerUserID), nil, &existing)
	if err == nil {
		// Pocket ID owns user ids. The local person id is only a lookup fallback for
		// accounts created before Homelab stored the provider's canonical id.
		body["id"] = existing.ID
		if err := c.request(ctx, http.MethodPut, "/api/users/"+url.PathEscape(providerUserID), body, &existing); err != nil {
			return "", err
		}
		if existing.ID == "" {
			existing.ID = providerUserID
		}
		return existing.ID, nil
	}
	if !strings.Contains(err.Error(), "404") {
		return "", err
	}
	if err := c.request(ctx, http.MethodPost, "/api/users", body, &existing); err != nil {
		return "", err
	}
	if existing.ID == "" {
		return "", fmt.Errorf("Pocket ID didn't return the created account id")
	}
	return existing.ID, nil
}

// Disable prevents further Pocket ID sign-ins for this user.
func (c *Client) Disable(ctx context.Context, id string) error {
	var u userDTO
	if err := c.request(ctx, http.MethodGet, "/api/users/"+url.PathEscape(id), nil, &u); err != nil {
		return err
	}
	ids := make([]string, 0, len(u.UserGroups))
	for _, g := range u.UserGroups {
		ids = append(ids, g.ID)
	}
	body := map[string]any{"id": u.ID, "username": u.Username, "displayName": u.DisplayName, "email": u.Email, "disabled": true, "userGroupIds": ids}
	return c.request(ctx, http.MethodPut, "/api/users/"+url.PathEscape(id), body, nil)
}

// LoginLink creates a one-time Pocket ID link for passkey registration or sign-in.
func (c *Client) LoginLink(ctx context.Context, id string, _ time.Duration) (string, error) {
	var response struct {
		Token string `json:"token"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/users/"+url.PathEscape(id)+"/one-time-access-token", map[string]any{}, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", fmt.Errorf("Pocket ID didn't return a login link")
	}
	u := *c.base
	u.Path = path.Join(c.base.Path, "lc", response.Token)
	return u.String(), nil
}

// Observe returns the users Pocket ID reports; its current API exposes no last-sign-in field.
func (c *Client) Observe(ctx context.Context) ([]core.IdentityUser, error) {
	var p page[userDTO]
	if err := c.request(ctx, http.MethodGet, "/api/users?pagination%5Blimit%5D=100", nil, &p); err != nil {
		return nil, err
	}
	out := make([]core.IdentityUser, 0, len(p.Data))
	for _, u := range p.Data {
		groups := make([]string, 0, len(u.UserGroups))
		for _, g := range u.UserGroups {
			groups = append(groups, g.Name)
		}
		out = append(out, core.IdentityUser{ID: u.ID, Email: u.Email, Name: u.DisplayName, Groups: groups, Disabled: u.Disabled})
	}
	return out, nil
}
