package hub

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// Release versions. Every build that Homelab puts on dev gets a GitHub pre-release
// (v0.4.0-dev.1), and a promote makes the release (v0.4.0) on the same commit. The work is
// done by homelab-services; here Homelab only asks, keeps the answer on the release, and
// gives it to the app as HOMELAB_VERSION.
//
// A deploy never waits on a version that can't be made. If GitHub says no (the token can't
// write releases), the app is deployed without one and the companion says why.

const (
	msgVersionsToken = "Can't make versions on GitHub: the token needs Contents: read and write."
	msgVersionsNone  = "Can't make versions on GitHub: Homelab has no GitHub token."

	versionNotesMax = 8 << 10          // the longest notes kept on a release
	versionRetry    = 10 * time.Minute // between tries to make an app's first release
)

var prodTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// versioned says whether Homelab makes versions for an app: it is built from a GitHub repo.
func (h *Hub) versioned(a state.App) bool {
	return h.ad.Versions != nil && h.ad.Secrets != nil && len(a.Repo) > 2 && a.Repo[0] != '/' && a.Repo[len(a.Repo)-1] != '/'
}

// versionChannel is the kind of version a deploy to env gets. An app that has only one
// environment has no dev to try things on, so its deploys are the prod releases.
func versionChannel(environments []string, env string) string {
	if len(environments) == 1 {
		return core.Prod
	}
	return env
}

// versionsProblem is why versions can't be made for an app now, or "".
func (h *Hub) versionsProblem(app string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.versionProblem[app]
}

func (h *Hub) versionOK(app string) {
	h.mu.Lock()
	delete(h.versionProblem, app)
	h.mu.Unlock()
}

// versionFailed remembers a problem for the companion and logs it. It never fails a deploy.
func (h *Hub) versionFailed(ctx context.Context, a state.App, env string, err error) {
	text := msgVersionsToken
	switch {
	case errors.Is(err, core.ErrGitHubRefused):
		h.log.Warn("versions: GitHub refused the token", "app", a.ID, "err", err)
	case err == errNoToken:
		text = msgVersionsNone
		h.log.Warn("versions: there is no GitHub token", "app", a.ID)
	default:
		text = "Can't make versions on GitHub right now: " + err.Error() + "."
		h.log.Warn("versions: failed", "app", a.ID, "err", err)
	}
	h.mu.Lock()
	changed := h.versionProblem[a.ID] != text
	h.versionProblem[a.ID] = text
	h.mu.Unlock()
	if changed {
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: env, Action: "version.failed", Detail: text + " The app is deployed without a version."})
	}
}

var errNoToken = errors.New("no GitHub token")

func (h *Hub) githubToken() (string, error) {
	t, _ := h.ad.Secrets.Platform("GITHUB_TOKEN")
	if t == "" {
		return "", errNoToken
	}
	return t, nil
}

// makeVersion asks for the version of a commit. ok is false when there isn't one.
func (h *Hub) makeVersion(ctx context.Context, a state.App, env, commit, channel string) (core.VersionResult, bool) {
	token, err := h.githubToken()
	if err == nil {
		var res core.VersionResult
		res, err = h.ad.Versions.Publish(ctx, a.Repo, commit, channel, token, false)
		if err == nil {
			h.versionOK(a.ID)
			return res, res.Released && res.Version != ""
		}
	}
	h.versionFailed(ctx, a, env, err)
	return core.VersionResult{}, false
}

// stampVersion gives rel its version for env, or leaves it without one. A version it had
// (dev's, when it is promoted) is dropped first: prod gets its own.
func (h *Hub) stampVersion(ctx context.Context, a state.App, rel *core.Release, env string) {
	rel.Version, rel.Summary, rel.Notes = "", "", ""
	if !h.versioned(a) {
		return
	}
	channel := env
	if m, err := h.parse(rel.Manifest, a.Repo); err == nil {
		channel = versionChannel(m.Environments, env)
	}
	h.bootstrapVersions(ctx, a)
	res, ok := h.makeVersion(ctx, a, env, rel.Commit, channel)
	if !ok {
		return
	}
	rel.Version, rel.Summary, rel.Notes = res.Version, res.Summary, capNotes(res.Notes)
}

func capNotes(s string) string {
	if len(s) > versionNotesMax {
		return s[:versionNotesMax]
	}
	return s
}

// bootstrapVersions gives an app that already runs on prod, but has no release on GitHub,
// its first one: the commit prod runs becomes the first version (from pyproject.toml,
// package.json or Cargo.toml, else v0.1.0), so the next dev build continues from it. It
// does this once per app; if GitHub says no it tries again later.
func (h *Hub) bootstrapVersions(ctx context.Context, a state.App) {
	if !h.versioned(a) {
		return
	}
	prod, err := h.store.Env(ctx, a.ID, core.Prod)
	if err != nil || prod.Want == nil {
		return // nothing runs on prod yet
	}
	h.mu.Lock()
	now := h.cfg.Now()
	skip := h.versionDone[a.ID] || now.Sub(h.versionTried[a.ID]) < versionRetry
	if !skip {
		h.versionTried[a.ID] = now
	}
	h.mu.Unlock()
	if skip {
		return
	}
	token, err := h.githubToken()
	if err != nil {
		h.versionFailed(ctx, a, core.Prod, err)
		return
	}
	releases, err := h.ad.Versions.List(ctx, a.Repo, token)
	if err != nil {
		h.versionFailed(ctx, a, core.Prod, err)
		return
	}
	found := false
	for _, r := range releases {
		if r.Prerelease || !prodTag.MatchString(r.Tag) {
			continue
		}
		found = true
		if r.Commit == prod.Want.Commit && prod.Want.Version == "" {
			h.keepVersion(ctx, a, prod.Want.Commit, r.Tag, r.Summary, r.Notes)
		}
	}
	if !found {
		res, ok := h.makeVersion(ctx, a, core.Prod, prod.Want.Commit, core.Prod)
		if !ok {
			return
		}
		h.keepVersion(ctx, a, prod.Want.Commit, res.Version, res.Summary, res.Notes)
		h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Prod, Action: "version.first",
			Detail: "Made the first release, " + res.Version + ", for " + short(prod.Want.Commit) + ", which prod runs"})
	}
	h.versionOK(a.ID)
	h.mu.Lock()
	h.versionDone[a.ID] = true
	h.mu.Unlock()
}

// keepVersion saves a version on what prod runs now. The running container learns it at its
// next deploy (HOMELAB_VERSION); nothing is restarted for a label.
func (h *Hub) keepVersion(ctx context.Context, a state.App, commit, version, summary, notes string) {
	if err := h.store.SetVersion(ctx, a.ID, core.Prod, commit, version, summary, capNotes(notes)); err != nil {
		h.log.Warn("versions: save", "app", a.ID, "err", err)
	}
}

// previewVersion fills in what prod's version would be for a promote preview. It makes
// nothing on GitHub, and says nothing when it can't be worked out.
func (h *Hub) previewVersion(ctx context.Context, a state.App, dev, prod *core.Release, p *PromotePreview) {
	if !h.versioned(a) || dev.Version == "" || (prod != nil && prod.Version == "") {
		return
	}
	token, err := h.githubToken()
	if err != nil {
		return
	}
	res, err := h.ad.Versions.Publish(ctx, a.Repo, dev.Commit, core.Prod, token, true)
	if err != nil || !res.Released {
		return
	}
	p.NextVersion, p.Summary, p.Notes = res.Version, res.Summary, capNotes(res.Notes)
}

// ReleasesHeaders are the headers the releases API (GET /api/releases) needs when it is
// forwarded to homelab-services: the app's repo and the GitHub token. They are only for
// that call; the token is never shown or logged.
func (h *Hub) ReleasesHeaders(ctx context.Context, appID string) (http.Header, error) {
	a, err := h.store.App(ctx, appID)
	if err != nil || !h.versioned(a) {
		return nil, userErr("Release versions aren't available for this app.")
	}
	token, err := h.githubToken()
	if err != nil {
		return nil, userErr("Release versions aren't available: Homelab has no GitHub token.")
	}
	hd := http.Header{}
	hd.Set("X-Homelab-Repo", a.Repo)
	hd.Set("X-Homelab-Github-Token", token)
	return hd, nil
}
