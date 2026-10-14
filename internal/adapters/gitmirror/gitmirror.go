// Package gitmirror keeps a full, read-only copy of each app's repo on this computer, using
// the git command line. It implements core.Mirror.
//
// A mirror lives at <Dir>/<owner>/<name>.git. The GitHub token is handed to git through
// its environment only, so it never appears in a URL, a command line or a file.
package gitmirror

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Options configures a Mirror.
type Options struct {
	Dir     string        // where mirrors live, e.g. ~/.homelab/mirrors
	BaseURL string        // default https://github.com
	Token   func() string // may return ""; read on every Sync
	GitBin  string        // default "git"
	Logger  *slog.Logger  // default: discard
	Now     func() time.Time
}

// Mirror is the git implementation of core.Mirror.
type Mirror struct {
	dir     string
	baseURL string
	token   func() string
	bin     string
	log     *slog.Logger
	now     func() time.Time

	mu    sync.Mutex
	repos map[string]*repoState
}

var _ core.Mirror = (*Mirror)(nil)

type repoState struct {
	sync     sync.Mutex // held for the whole of a Sync
	lastSync time.Time
	err      string
}

const defaultTimeout = 10 * time.Minute

var partRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// New returns a Mirror. It doesn't need git or the network yet.
func New(opts Options) (*Mirror, error) {
	if opts.Dir == "" {
		return nil, errors.New("gitmirror: Dir is required")
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	m := &Mirror{
		dir: dir, baseURL: strings.TrimRight(opts.BaseURL, "/"), token: opts.Token,
		bin: opts.GitBin, log: opts.Logger, now: opts.Now, repos: map[string]*repoState{},
	}
	if m.baseURL == "" {
		m.baseURL = "https://github.com"
	}
	if m.bin == "" {
		m.bin = "git"
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// split checks that repo is exactly owner/name and returns the two parts.
func split(repo string) (owner, name string, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%q isn't a repo name; it should look like owner/name", repo)
	}
	for _, p := range parts {
		if !partRe.MatchString(p) || p == "." || p == ".." {
			return "", "", fmt.Errorf("%q isn't a repo name; it should look like owner/name", repo)
		}
	}
	return parts[0], parts[1], nil
}

func (m *Mirror) path(owner, name string) string {
	return filepath.Join(m.dir, owner, name+".git")
}

// state returns the in-memory state for repo, seeding LastSync from disk the first time.
func (m *Mirror) state(repo, path string) *repoState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.repos[repo]
	if !ok {
		st = &repoState{lastSync: diskSyncTime(path)}
		m.repos[repo] = st
	}
	return st
}

// diskSyncTime is when an existing mirror was last updated, judged by git's own files.
func diskSyncTime(path string) time.Time {
	var best time.Time
	for _, f := range []string{"FETCH_HEAD", "packed-refs"} {
		if fi, err := os.Stat(filepath.Join(path, f)); err == nil && fi.ModTime().After(best) {
			best = fi.ModTime()
		}
	}
	return best
}

// Status says when repo was last synced and what went wrong last time.
func (m *Mirror) Status(repo string) core.MirrorStatus {
	owner, name, err := split(repo)
	if err != nil {
		return core.MirrorStatus{Error: err.Error()}
	}
	path := m.path(owner, name)
	st := m.state(repo, path)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := core.MirrorStatus{Path: path, Error: st.err}
	if !st.lastSync.IsZero() {
		t := st.lastSync
		s.LastSync = &t
	}
	return s
}

// Sync clones repo the first time and fetches every branch and tag after that. Only one
// sync of the same repo runs at a time; a second caller waits and then syncs again.
func (m *Mirror) Sync(ctx context.Context, repo string) error {
	owner, name, err := split(repo)
	if err != nil {
		return err
	}
	path := m.path(owner, name)
	st := m.state(repo, path)
	st.sync.Lock()
	defer st.sync.Unlock()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	token := ""
	if m.token != nil {
		token = m.token()
	}
	err = m.run(ctx, path, owner, name, token)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		st.err = err.Error()
		m.log.Warn("mirror sync failed", "repo", repo, "error", err)
		return err
	}
	st.err = ""
	st.lastSync = m.now()
	m.log.Info("mirror synced", "repo", repo)
	return nil
}

func (m *Mirror) run(ctx context.Context, path, owner, name, token string) error {
	if err := os.MkdirAll(m.home(), 0o700); err != nil {
		return fmt.Errorf("can't create %s: %w", m.dir, err)
	}
	env := m.env(token)
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		out, err := m.git(ctx, env, token, "-C", path, "remote", "update", "--prune")
		if err != nil {
			return explain(ctx, out, err, "update")
		}
		return nil
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("can't create %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, "."+name+".clone-")
	if err != nil {
		return fmt.Errorf("can't create a folder in %s: %w", parent, err)
	}
	defer os.RemoveAll(tmp)
	url := m.baseURL + "/" + owner + "/" + name + ".git"
	if out, err := m.git(ctx, env, token, "clone", "--mirror", url, tmp); err != nil {
		return explain(ctx, out, err, "clone")
	}
	if err := os.RemoveAll(path); err != nil { // an empty or broken leftover
		return fmt.Errorf("can't replace %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("can't move the new mirror into place at %s: %w", path, err)
	}
	return nil
}

func (m *Mirror) home() string { return filepath.Join(m.dir, ".home") }

// git runs git and returns its combined output with the token scrubbed.
func (m *Mirror) git(ctx context.Context, env []string, token string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, m.bin, args...)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return scrub(buf.String(), token), err
}

// env is git's environment: nothing inherited from GIT_*, no user or system config, no
// prompts, and the token (if any) as an Authorization header for the base URL only.
func (m *Mirror) env(token string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") || k == "HOME" || k == "XDG_CONFIG_HOME" || k == "GCM_INTERACTIVE" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+m.home(),
		"XDG_CONFIG_HOME="+m.home(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
		"GCM_INTERACTIVE=never",
		"LC_ALL=C",
	)
	if token != "" {
		cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http."+m.baseURL+"/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+cred,
		)
	}
	return env
}

// scrub removes the token, and its base64 form, from git's output.
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return strings.NewReplacer(token, "***", cred, "***").Replace(s)
}

// explain turns a failed git run into a message for people.
func explain(ctx context.Context, out string, err error, what string) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("Syncing took too long and was stopped")
		}
		return ctx.Err()
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("Git isn't installed on this computer, so the repo can't be mirrored")
	}
	low := strings.ToLower(out)
	// git says "unable to access" for refused tokens too, so the specific answers come first.
	switch {
	case strings.Contains(low, "authentication failed"), strings.Contains(low, "invalid username or password"),
		strings.Contains(low, "terminal prompts disabled"), strings.Contains(low, "returned error: 401"),
		strings.Contains(low, "returned error: 403"):
		return errors.New("GitHub refused the token (does it have the repo scope?)")
	case strings.Contains(low, "repository not found"), strings.Contains(low, "returned error: 404"):
		return errors.New("GitHub says this repo doesn't exist, or the token can't see it")
	case strings.Contains(low, "could not resolve host"), strings.Contains(low, "unable to access"),
		strings.Contains(low, "failed to connect"), strings.Contains(low, "connection timed out"),
		strings.Contains(low, "connection refused"), strings.Contains(low, "network is unreachable"):
		return errors.New("Couldn't reach GitHub")
	}
	if l := lastLine(out); l != "" {
		return fmt.Errorf("git %s failed: %s", what, l)
	}
	return fmt.Errorf("git %s failed: %w", what, err)
}

// lastLine is the last non-empty line of s, cut to a sensible length.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300] + "..."
	}
	return l
}
