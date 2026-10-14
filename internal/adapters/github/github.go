// Package github implements the Source, Builder and Registry contracts on top of GitHub
// (the REST API, Actions and the ghcr.io container registry).
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

var (
	_ core.Source   = (*Client)(nil)
	_ core.Builder  = (*Client)(nil)
	_ core.Registry = (*Client)(nil)
)

const (
	defaultAPIBase      = "https://api.github.com"
	defaultRegistryBase = "https://ghcr.io"
	apiVersion          = "2022-11-28"
	userAgent           = "homehub"
	registryUser        = "homehub"
	maxCompareCommits   = 100
	maxBody             = 32 << 20
)

// Options configures a Client.
type Options struct {
	Token        string // GitHub token with read access to repos, Actions and packages; may be empty for public repos
	APIBase      string // default https://api.github.com
	RegistryBase string // default https://ghcr.io
	HTTP         *http.Client
	Logger       *slog.Logger
}

// Client talks to GitHub. It is safe for concurrent use.
type Client struct {
	token        string
	apiBase      string
	registryBase string
	registryHost string
	http         *http.Client
	log          *slog.Logger

	mu     sync.Mutex
	etags  map[string]cachedResponse
	tokens map[string]cachedToken
	now    func() time.Time
}

type cachedResponse struct {
	etag string
	body []byte
}

type cachedToken struct {
	value   string
	expires time.Time
}

// New returns a Client with defaults filled in.
func New(opts Options) *Client {
	c := &Client{
		token:        opts.Token,
		apiBase:      strings.TrimRight(opts.APIBase, "/"),
		registryBase: strings.TrimRight(opts.RegistryBase, "/"),
		http:         opts.HTTP,
		log:          opts.Logger,
		etags:        map[string]cachedResponse{},
		tokens:       map[string]cachedToken{},
		now:          time.Now,
	}
	if c.apiBase == "" {
		c.apiBase = defaultAPIBase
	}
	if c.registryBase == "" {
		c.registryBase = defaultRegistryBase
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.log == nil {
		c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if u, err := url.Parse(c.registryBase); err == nil {
		c.registryHost = u.Host
	}
	return c
}

// ---- GitHub REST plumbing ----

// get performs a GitHub API GET. With useETag it sends If-None-Match and serves the cached
// body on 304 (which doesn't count against the rate limit). 404 returns core.ErrNotFound.
func (c *Client) get(ctx context.Context, path, accept string, useETag bool) ([]byte, error) {
	u := c.apiBase + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	var cached cachedResponse
	var haveCached bool
	if useETag {
		c.mu.Lock()
		cached, haveCached = c.etags[u]
		c.mu.Unlock()
		if haveCached {
			req.Header.Set("If-None-Match", cached.etag)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach GitHub at %s: %w", c.apiBase, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("reading GitHub's answer: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		return cached.body, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if etag := resp.Header.Get("ETag"); useETag && etag != "" {
			c.mu.Lock()
			c.etags[u] = cachedResponse{etag: etag, body: body}
			c.mu.Unlock()
		}
		return body, nil
	}
	return nil, c.apiError(resp, body, path)
}

func (c *Client) apiError(resp *http.Response, body []byte, path string) error {
	code := resp.StatusCode
	switch code {
	case http.StatusNotFound:
		hint := ""
		if c.token == "" {
			hint = " (no GitHub token is set, so private repos look missing)"
		}
		return fmt.Errorf("GitHub has no %s%s: %w", path, hint, core.ErrNotFound)
	case http.StatusUnauthorized:
		return errors.New("GitHub said 401: the token in secrets/platform.env is missing or wrong")
	case http.StatusForbidden, http.StatusTooManyRequests:
		if msg, ok := rateLimitMessage(resp, c.now()); ok {
			return fmt.Errorf("GitHub said %d: %s", code, msg)
		}
		if code == http.StatusForbidden {
			return fmt.Errorf("GitHub said 403: the token in secrets/platform.env isn't allowed to read this (needs read access to repos, Actions and packages)%s", apiMessage(body))
		}
	}
	return fmt.Errorf("GitHub said %d%s", code, apiMessage(body))
}

func apiMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return ": " + e.Message
	}
	return ""
}

// rateLimitMessage explains a rate-limit refusal, including when it lifts.
func rateLimitMessage(resp *http.Response, now time.Time) (string, bool) {
	var reset time.Time
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			reset = now.Add(time.Duration(secs) * time.Second)
		}
	}
	if reset.IsZero() && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if ts, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = time.Unix(ts, 0)
		}
	}
	if reset.IsZero() {
		if resp.StatusCode == http.StatusTooManyRequests {
			return "GitHub is rate limiting Homehub; try again in a few minutes", true
		}
		return "", false
	}
	wait := reset.Sub(now).Round(time.Second)
	if wait < 0 {
		wait = 0
	}
	return fmt.Sprintf("GitHub's rate limit is used up; it resets at %s (in %s)", reset.Local().Format("15:04:05"), wait), true
}

func escapePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ---- core.Source ----

// ReadFile returns a file's raw bytes at a branch, tag or commit.
func (c *Client) ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error) {
	p := "/repos/" + repo + "/contents/" + escapePath(path)
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	return c.get(ctx, p, "application/vnd.github.raw+json", false)
}

type apiCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (a apiCommit) toCore() core.Commit {
	author := a.Commit.Author.Name
	if a.Author != nil && a.Author.Login != "" {
		author = a.Author.Login
	}
	msg, _, _ := strings.Cut(a.Commit.Message, "\n")
	return core.Commit{
		SHA:     a.SHA,
		Message: strings.TrimSpace(msg),
		Author:  author,
		When:    a.Commit.Author.Date,
		URL:     a.HTMLURL,
	}
}

// Commit describes one commit.
func (c *Client) Commit(ctx context.Context, repo, sha string) (core.Commit, error) {
	body, err := c.get(ctx, "/repos/"+repo+"/commits/"+url.PathEscape(sha), "", false)
	if err != nil {
		return core.Commit{}, err
	}
	var ac apiCommit
	if err := json.Unmarshal(body, &ac); err != nil {
		return core.Commit{}, fmt.Errorf("GitHub sent a commit I couldn't read: %w", err)
	}
	return ac.toCore(), nil
}

// Compare lists the commits in head that aren't in base, oldest first (at most 100).
func (c *Client) Compare(ctx context.Context, repo, base, head string) ([]core.Commit, error) {
	body, err := c.get(ctx, "/repos/"+repo+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head), "", false)
	if err != nil {
		return nil, err
	}
	var res struct {
		Commits []apiCommit `json:"commits"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("GitHub sent a comparison I couldn't read: %w", err)
	}
	out := make([]core.Commit, 0, len(res.Commits))
	for _, ac := range res.Commits {
		out = append(out, ac.toCore())
	}
	if len(out) > maxCompareCommits {
		out = out[len(out)-maxCompareCommits:] // keep the newest 100, still oldest first
	}
	return out, nil
}

// WebURL links to the repo, a commit or a comparison on github.com.
func (c *Client) WebURL(repo, base, head string) string {
	root := "https://github.com/" + repo
	switch {
	case base == "" && head == "":
		return root
	case base == "":
		return root + "/commit/" + head
	default:
		return root + "/compare/" + base + "..." + head
	}
}

// ---- core.Builder ----

// LatestBuild returns the newest push-triggered workflow run on branch, as it is right now.
func (c *Client) LatestBuild(ctx context.Context, repo, branch, workflow string) (core.Build, error) {
	q := "?branch=" + url.QueryEscape(branch) + "&event=push&per_page=1"
	p := "/repos/" + repo + "/actions/runs" + q
	if workflow != "" {
		p = "/repos/" + repo + "/actions/workflows/" + url.PathEscape(workflow) + "/runs" + q
	}
	body, err := c.get(ctx, p, "", true)
	if err != nil {
		return core.Build{}, err
	}
	var res struct {
		Runs []struct {
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return core.Build{}, fmt.Errorf("GitHub sent build results I couldn't read: %w", err)
	}
	if len(res.Runs) == 0 {
		return core.Build{}, fmt.Errorf("no builds of %s on %s yet: %w", repo, branch, core.ErrNotFound)
	}
	r := res.Runs[0]
	status := core.BuildRunning
	if r.Status == "completed" {
		if r.Conclusion == "success" {
			status = core.BuildPassed
		} else {
			status = core.BuildFailed
		}
	}
	return core.Build{Commit: r.HeadSHA, Status: status, URL: r.HTMLURL}, nil
}

// ---- core.Registry ----

// splitImage splits ghcr.io/owner/name into host and repository name.
func splitImage(image string) (host, name string) {
	host, name, _ = strings.Cut(image, "/")
	return host, name
}

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Resolve returns the digest of image:tag using the Docker Registry v2 API.
func (c *Client) Resolve(ctx context.Context, image, tag string) (string, error) {
	host, name := splitImage(image)
	if name == "" || host != c.registryHost {
		return "", fmt.Errorf("the registry for %q isn't supported yet (only %s is)", image, c.registryHost)
	}
	u := c.registryBase + "/v2/" + name + "/manifests/" + url.PathEscape(tag)
	bearer := ""
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := c.manifestRequest(ctx, u, bearer)
		if err != nil {
			return "", err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			d := resp.Header.Get("Docker-Content-Digest")
			if d == "" {
				return "", fmt.Errorf("%s answered without a digest for %s:%s", host, image, tag)
			}
			return d, nil
		case http.StatusNotFound:
			return "", fmt.Errorf("%s:%s isn't in the registry: %w", image, tag, core.ErrNotFound)
		case http.StatusUnauthorized:
			if bearer != "" {
				if strings.Contains(string(body), "MANIFEST_UNKNOWN") || strings.Contains(string(body), "NAME_UNKNOWN") {
					return "", fmt.Errorf("%s:%s isn't in the registry: %w", image, tag, core.ErrNotFound)
				}
				return "", fmt.Errorf("%s said 401 for %s: the token in secrets/platform.env can't read this package (it needs read:packages)", host, image)
			}
			ch := resp.Header.Get("Www-Authenticate")
			realm, service, scope, ok := parseBearer(ch)
			if !ok {
				return "", fmt.Errorf("%s said 401 for %s and didn't say how to sign in", host, image)
			}
			if scope == "" {
				scope = "repository:" + name + ":pull"
			}
			bearer, err = c.registryToken(ctx, realm, service, scope)
			if err != nil {
				return "", err
			}
		default:
			if strings.Contains(string(body), "MANIFEST_UNKNOWN") {
				return "", fmt.Errorf("%s:%s isn't in the registry: %w", image, tag, core.ErrNotFound)
			}
			return "", fmt.Errorf("%s said %d when looking up %s:%s", host, resp.StatusCode, image, tag)
		}
	}
	return "", fmt.Errorf("%s kept refusing access to %s", host, image)
}

func (c *Client) manifestRequest(ctx context.Context, u, bearer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	req.Header.Set("User-Agent", userAgent)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the registry at %s: %w", c.registryBase, err)
	}
	return resp, nil
}

// registryToken fetches (or reuses) a bearer token for one scope.
func (c *Client) registryToken(ctx context.Context, realm, service, scope string) (string, error) {
	key := realm + "|" + service + "|" + scope
	c.mu.Lock()
	t, ok := c.tokens[key]
	c.mu.Unlock()
	if ok && c.now().Before(t.expires) {
		return t.value, nil
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("the registry sent a sign-in address I couldn't read: %q", realm)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.SetBasicAuth(registryUser, c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach the registry sign-in at %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("the registry said %d at sign-in: the token in secrets/platform.env is missing or wrong (it needs read:packages)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry sign-in said %d", resp.StatusCode)
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("the registry sent a sign-in answer I couldn't read: %w", err)
	}
	if tr.Token == "" {
		tr.Token = tr.AccessToken
	}
	if tr.Token == "" {
		return "", errors.New("the registry sign-in answered without a token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second // the registry spec's default
	}
	margin := 30 * time.Second
	if ttl <= 2*margin {
		margin = ttl / 4
	}
	c.mu.Lock()
	c.tokens[key] = cachedToken{value: tr.Token, expires: c.now().Add(ttl - margin)}
	c.mu.Unlock()
	return tr.Token, nil
}

// parseBearer reads `Bearer realm="...",service="...",scope="..."`.
func parseBearer(h string) (realm, service, scope string, ok bool) {
	scheme, rest, found := strings.Cut(strings.TrimSpace(h), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", "", false
	}
	for rest = strings.TrimSpace(rest); rest != ""; {
		k, v, found := strings.Cut(rest, "=")
		if !found {
			break
		}
		k = strings.TrimSpace(strings.TrimLeft(k, ", "))
		rest = v
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			val = rest[1 : 1+end]
			rest = rest[end+2:]
		} else {
			val, rest, _ = strings.Cut(rest, ",")
		}
		switch strings.ToLower(k) {
		case "realm":
			realm = val
		case "service":
			service = val
		case "scope":
			scope = val
		}
		rest = strings.TrimLeft(rest, ", ")
	}
	return realm, service, scope, realm != ""
}

// PullAuth returns credentials for pulling image: the GitHub token for ghcr images.
func (c *Client) PullAuth(ctx context.Context, image string) (core.RegistryAuth, error) {
	host, _ := splitImage(image)
	if c.token == "" || host != c.registryHost {
		return core.RegistryAuth{}, nil
	}
	return core.RegistryAuth{Server: host, Username: registryUser, Password: c.token}, nil
}
