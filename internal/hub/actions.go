package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
	"github.com/rupaai/homelab/internal/state"
)

// UserError is a problem the person can fix; its message is safe and meant to be shown.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userErr(f string, a ...any) error { return &UserError{Msg: fmt.Sprintf(f, a...)} }

// ErrBusy means another action is already working on the app.
var ErrBusy = &UserError{Msg: "Something else is already happening on this app. Try again when it's finished."}

// ServeCompanion gives the companion its own address ("homelab").
func (h *Hub) ServeCompanion(ctx context.Context, handler http.Handler) error {
	return h.ad.Addresses.Ensure(ctx, "homelab", handler)
}

// ---- Registry: adding apps (A10) ----

// AddApp registers an app from its repo by reading homelab.yaml on its build branch.
func (h *Hub) AddApp(ctx context.Context, repo, branch string, by core.Viewer) (state.App, error) {
	repo = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "github.com/")), "/")
	repo = strings.TrimSuffix(repo, ".git")
	if strings.Count(repo, "/") != 1 {
		return state.App{}, userErr("Use the repo as owner/name, for example rupaai/smart-recommender-player.")
	}
	if branch == "" {
		branch = "main"
	}
	raw, err := h.ad.Source.ReadFile(ctx, repo, branch, manifest.FileName)
	if errors.Is(err, core.ErrNotFound) {
		return state.App{}, userErr("Couldn't find %s on the %s branch of %s. If the file is there, the repo is probably private "+
			"and the GitHub token can't see it: give the token the \"repo\" scope and restart homelabd.", manifest.FileName, branch, repo)
	}
	if err != nil {
		return state.App{}, fmt.Errorf("read %s from %s: %w", manifest.FileName, repo, err)
	}
	m, err := h.parse(raw, repo)
	if err != nil {
		return state.App{}, &UserError{Msg: err.Error()}
	}
	if _, err := h.store.App(ctx, m.ID); err == nil {
		return state.App{}, userErr("An app with id %q is already registered.", m.ID)
	}
	a, err := h.store.AddApp(ctx, state.App{ID: m.ID, Name: m.Name, Repo: repo, Manifest: raw, Promotion: m.Promotion, AddedBy: by.Label()})
	if err != nil {
		return a, err
	}
	h.logEvent(ctx, state.Event{Actor: by.Label(), App: a.ID, Action: "app.added", Detail: fmt.Sprintf("Added %s from %s", a.Name, repo)})
	go h.CheckBuilds(context.WithoutCancel(ctx))
	if h.ad.Mirror != nil {
		go func() { _ = h.ad.Mirror.Sync(context.WithoutCancel(ctx), repo) }()
	}
	return a, nil
}

// ---- Builds (C4) ----

// CheckBuilds looks for new passing builds once. For auto-dev apps, a new one goes to dev.
func (h *Hub) CheckBuilds(ctx context.Context) {
	apps, err := h.store.Apps(ctx)
	if err != nil {
		return
	}
	for _, a := range apps {
		m, err := h.parse(a.Manifest, a.Repo)
		if err != nil {
			continue
		}
		b, err := h.ad.Builder.LatestBuild(ctx, a.Repo, m.Build.Branch, m.Build.Workflow)
		if err != nil {
			if !errors.Is(err, core.ErrNotFound) {
				h.log.Warn("check builds", "app", a.ID, "err", err)
			}
			continue
		}
		h.mu.Lock()
		h.builds[a.ID] = b
		alreadyLogged := h.failed[a.ID] == b.Commit
		h.mu.Unlock()

		switch b.Status {
		case core.BuildFailed:
			if !alreadyLogged {
				h.mu.Lock()
				h.failed[a.ID] = b.Commit
				h.mu.Unlock()
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Action: "build.failed",
					Detail: fmt.Sprintf("The build of %s failed, so it won't be deployed: %s", short(b.Commit), b.URL)})
			}
			continue
		case core.BuildPassed:
		default:
			continue
		}
		if a.Promotion == core.PromotionManual {
			continue // manual: the companion shows the build and offers "Deploy to dev"
		}
		dev, err := h.store.Env(ctx, a.ID, core.Dev)
		if err != nil || dev.LastBuild == b.Commit {
			continue
		}
		rel, err := h.release(ctx, a, b)
		if errors.Is(err, core.ErrNotFound) {
			continue // the image isn't in the registry yet; try again next round
		}
		if err != nil {
			var ue *UserError
			if errors.As(err, &ue) {
				h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Dev, Action: "build.skipped", Detail: ue.Msg})
				_ = h.store.SetLastBuild(ctx, a.ID, core.Dev, b.Commit)
			} else {
				h.log.Warn("new build", "app", a.ID, "err", err)
			}
			continue
		}
		if err := h.setWantAndApply(ctx, a, core.Dev, rel, "deploy", core.Viewer{}, "New build"); err != nil {
			h.log.Warn("new build", "app", a.ID, "err", err)
			continue
		}
		_ = h.store.SetLastBuild(ctx, a.ID, core.Dev, b.Commit)
	}
}

// release turns a passing build into a release: the image digest, the commit, and the
// manifest at that commit.
func (h *Hub) release(ctx context.Context, a state.App, b core.Build) (core.Release, error) {
	raw, err := h.ad.Source.ReadFile(ctx, a.Repo, b.Commit, manifest.FileName)
	if errors.Is(err, core.ErrNotFound) {
		return core.Release{}, userErr("Commit %s has no %s, so it can't be deployed.", short(b.Commit), manifest.FileName)
	}
	if err != nil {
		return core.Release{}, err
	}
	m, err := h.parse(raw, a.Repo)
	if err != nil {
		return core.Release{}, userErr("Commit %s: %s", short(b.Commit), err.Error())
	}
	if m.ID != a.ID {
		return core.Release{}, userErr("Commit %s changes the app id to %q; ids can't change.", short(b.Commit), m.ID)
	}
	tag := m.TagFor(b.Commit)
	digest, err := h.ad.Registry.Resolve(ctx, m.Image, tag)
	if err != nil {
		return core.Release{}, err
	}
	c, err := h.ad.Source.Commit(ctx, a.Repo, b.Commit)
	if err != nil {
		return core.Release{}, err
	}
	if err := h.store.UpdateApp(ctx, a.ID, m.Name, raw); err != nil {
		return core.Release{}, err
	}
	return core.Release{
		Commit: b.Commit, Image: m.Image, Tag: tag, Digest: digest,
		Message: c.Message, Author: c.Author, When: c.When, BuildURL: b.URL, Manifest: raw,
	}, nil
}

// DeployDev puts the latest passing build (or a given commit) on dev, by hand.
func (h *Hub) DeployDev(ctx context.Context, appID, commit string, by core.Viewer) error {
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return userErr("There's no app %q.", appID)
	}
	b := core.Build{Commit: commit, Status: core.BuildPassed}
	if commit == "" {
		h.mu.Lock()
		b = h.builds[appID]
		h.mu.Unlock()
		if b.Status != core.BuildPassed {
			return userErr("There's no passing build to deploy yet.")
		}
	}
	rel, err := h.release(ctx, a, b)
	if errors.Is(err, core.ErrNotFound) {
		return userErr("There's no image for commit %s yet. Has its build finished?", short(b.Commit))
	}
	if err != nil {
		return err
	}
	return h.setWantAndApply(ctx, a, core.Dev, rel, "deploy", by, "Deploy")
}

// setWantAndApply records a release as desired for an environment and starts it in the
// background. kind is deploy, promote or rollback.
func (h *Hub) setWantAndApply(ctx context.Context, a state.App, env string, rel core.Release, kind string, by core.Viewer, verb string) error {
	lock := h.appLock(a.ID)
	if !lock.TryLock() {
		return ErrBusy
	}
	actor := by.Label()
	if by == (core.Viewer{}) {
		actor = core.ActorHomelab
	}
	if _, err := h.store.SetWant(ctx, state.Deployment{App: a.ID, Env: env, Release: rel, By: actor, Kind: kind}); err != nil {
		lock.Unlock()
		return err
	}
	h.logEvent(ctx, state.Event{Actor: actor, App: a.ID, Env: env, Action: kind + ".started",
		Detail: fmt.Sprintf("%s %s (%s) to %s", verb, rel.Short(), firstLine(rel.Message), env)})
	h.setBusy(a.ID, env, "Deploying "+rel.Short())
	go func() {
		defer h.Kick() // runs last, once the app is unlocked, so the reconciler picks up any leftovers
		defer lock.Unlock()
		defer h.setBusy(a.ID, env, "")
		ctx := context.WithoutCancel(ctx)
		h.applyAndWait(ctx, a, env, rel, kind, actor)
	}()
	return nil
}

func (h *Hub) applyAndWait(ctx context.Context, a state.App, env string, rel core.Release, kind, actor string) bool {
	if err := h.deploy(ctx, a, env, rel); err != nil {
		h.deployFailed(ctx, a.ID, env, err, false)
		h.logEvent(ctx, state.Event{Actor: actor, App: a.ID, Env: env, Action: kind + ".failed", Detail: "Couldn't start " + rel.Short() + ": " + err.Error()})
		return false
	}
	h.deployOK(a.ID, env)
	h.ensureAddress(ctx, manifest.AddressName(a.ID, env), h.hostPort(a.Slot, env), a.ID, a.Name, env)
	ok, detail := h.waitHealthy(ctx, a, env, &rel)
	if ok {
		h.logEvent(ctx, state.Event{Actor: actor, App: a.ID, Env: env, Action: kind + ".done", Detail: rel.Short() + " is running and healthy"})
		h.removeOldImages(ctx, a.ID, rel.Image)
		return true
	}
	h.logEvent(ctx, state.Event{Actor: actor, App: a.ID, Env: env, Action: kind + ".unhealthy",
		Detail: rel.Short() + " started but isn't healthy: " + detail})
	return false
}

// removeOldImages deletes the app's downloaded images that no container uses, keeping
// what each environment runs now and the versions before it, which a rollback may need.
func (h *Hub) removeOldImages(ctx context.Context, appID, image string) {
	var keep []string
	for _, env := range []string{core.Dev, core.Prod} {
		e, err := h.store.Env(ctx, appID, env)
		if err != nil {
			h.log.Warn("old images", "app", appID, "err", err)
			return
		}
		if e.Want != nil {
			keep = append(keep, e.Want.Digest)
		}
		hist, err := h.store.History(ctx, appID, env, 50)
		if err != nil {
			h.log.Warn("old images", "app", appID, "err", err)
			return
		}
		seen := map[string]bool{}
		for _, d := range hist {
			if len(seen) == 3 {
				break
			}
			if !seen[d.Release.Digest] {
				seen[d.Release.Digest] = true
				keep = append(keep, d.Release.Digest)
			}
		}
	}
	slices.Sort(keep)
	if err := h.ad.Deployer.RemoveOldImages(ctx, image, slices.Compact(keep)); err != nil {
		h.log.Warn("old images", "app", appID, "err", err)
	}
}

func dataNames(data []manifest.Data) []string {
	var names []string
	for _, d := range data {
		names = append(names, d.Name)
	}
	return names
}

func (h *Hub) setBusy(app, env, what string) {
	h.mu.Lock()
	h.st(app, env).busy = what
	h.mu.Unlock()
}

// ---- Promote (D4, S7) ----

// PromotePreview says exactly what a promote would do, before it happens (P5).
type PromotePreview struct {
	From           *ReleaseView  `json:"from"` // what prod runs now; nil the first time
	To             *ReleaseView  `json:"to"`   // what dev runs
	Commits        []core.Commit `json:"commits"`
	CompareURL     string        `json:"compareUrl"`
	DataCopy       bool          `json:"dataCopy"` // prod's data is copied before the switch
	MissingSecrets []string      `json:"missingSecrets"`
	DevHealth      string        `json:"devHealth"`
	Blockers       []string      `json:"blockers"` // promote is refused while any are listed
}

// PreviewPromote returns what promoting dev to prod would do.
func (h *Hub) PreviewPromote(ctx context.Context, appID string) (PromotePreview, error) {
	var p PromotePreview
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return p, userErr("There's no app %q.", appID)
	}
	dev, err := h.store.Env(ctx, appID, core.Dev)
	if err != nil {
		return p, err
	}
	prod, err := h.store.Env(ctx, appID, core.Prod)
	if err != nil {
		return p, err
	}
	h.mu.Lock()
	p.DevHealth = h.st(appID, core.Dev).health
	prodBusy := h.st(appID, core.Prod).busy
	h.mu.Unlock()

	if dev.Want == nil {
		p.Blockers = append(p.Blockers, "Nothing is deployed to dev yet.")
		return p, nil
	}
	p.To = h.releaseView(a, dev.Want)
	if prod.Want != nil {
		p.From = h.releaseView(a, prod.Want)
		p.DataCopy = true
		if prod.Want.Digest == dev.Want.Digest {
			p.Blockers = append(p.Blockers, "Prod already runs this build.")
		} else if cs, err := h.ad.Source.Compare(ctx, a.Repo, prod.Want.Commit, dev.Want.Commit); err == nil {
			p.Commits = cs
		}
		p.CompareURL = h.ad.Source.WebURL(a.Repo, prod.Want.Commit, dev.Want.Commit)
	} else {
		p.CompareURL = h.ad.Source.WebURL(a.Repo, "", dev.Want.Commit)
	}
	if p.DevHealth != core.Healthy {
		p.Blockers = append(p.Blockers, "Dev isn't healthy, so this build hasn't proven it works yet.")
	}
	if prodBusy != "" {
		p.Blockers = append(p.Blockers, "Prod is busy: "+prodBusy+".")
	}
	if m, err := h.parse(dev.Want.Manifest, a.Repo); err == nil {
		missing, err := h.ad.Secrets.Missing(appID, core.Prod, m.Secrets)
		if err != nil {
			return p, err
		}
		p.MissingSecrets = missing
		for _, s := range missing {
			p.Blockers = append(p.Blockers, fmt.Sprintf("%s is missing for prod.", s))
		}
	}
	return p, nil
}

// Promote puts dev's exact build on prod: it copies prod's data, switches, and waits for
// health. It returns once the promote has started; progress shows in Overview and the log.
func (h *Hub) Promote(ctx context.Context, appID string, by core.Viewer) error {
	return h.promoteAs(ctx, appID, by.Label(), "Promote")
}

// promoteAs starts a promote on behalf of actor (a person, or homelab for auto-promote).
func (h *Hub) promoteAs(ctx context.Context, appID, actor, verb string) error {
	p, err := h.PreviewPromote(ctx, appID)
	if err != nil {
		return err
	}
	if len(p.Blockers) > 0 {
		return &UserError{Msg: strings.Join(p.Blockers, " ")}
	}
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return err
	}
	dev, err := h.store.Env(ctx, appID, core.Dev)
	if err != nil {
		return err
	}
	lock := h.appLock(appID)
	if !lock.TryLock() {
		return ErrBusy
	}
	rel := *dev.Want
	h.setBusy(appID, core.Prod, "Promoting "+rel.Short())
	h.logEvent(ctx, state.Event{Actor: actor, App: appID, Env: core.Prod, Action: "promote.started",
		Detail: fmt.Sprintf("%s %s (%s) from dev to prod", verb, rel.Short(), firstLine(rel.Message))})
	go func() {
		defer h.Kick() // runs last: if the promote stopped prod and failed, the old version starts again
		defer lock.Unlock()
		defer h.setBusy(appID, core.Prod, "")
		ctx := context.WithoutCancel(ctx)
		if err := h.promote(ctx, a, rel, actor); err != nil {
			h.logEvent(ctx, state.Event{Actor: actor, App: appID, Env: core.Prod, Action: "promote.failed", Detail: err.Error()})
		}
	}()
	return nil
}

func (h *Hub) promote(ctx context.Context, a state.App, rel core.Release, actor string) error {
	spec, err := h.spec(ctx, a, core.Prod, rel)
	if err != nil {
		return fmt.Errorf("Nothing changed: %w", err)
	}
	before, err := h.store.Env(ctx, a.ID, core.Prod)
	if err != nil {
		return fmt.Errorf("Nothing changed: %w", err)
	}
	h.setBusy(a.ID, core.Prod, "Downloading "+rel.Short())
	if err := h.ad.Deployer.Prepare(ctx, spec); err != nil {
		return fmt.Errorf("Nothing changed, the download failed: %w", err)
	}
	// Check the copy can work before stopping anything.
	m, err := h.parse(rel.Manifest, a.Repo)
	if err != nil {
		return fmt.Errorf("Nothing changed: %w", err)
	}
	if err := h.ad.Deployer.CheckFiles(ctx); err != nil {
		return fmt.Errorf("Nothing changed, prod's data can't be copied: %w", err)
	}
	// Stop prod's app (its services keep running) so the data copy is consistent, then copy
	// the app's data (CI/CD: backup before promote).
	h.setBusy(a.ID, core.Prod, "Copying prod's data")
	if err := h.ad.Deployer.StopApp(ctx, a.ID, core.Prod); err != nil {
		return fmt.Errorf("Couldn't stop prod to copy its data: %w", err)
	}
	// A postgres database is dumped (prod's services keep running, the database too), and a
	// sqlite file is copied with the app's other data. Both are looked for in the version
	// prod runs now, because that is the database there is.
	names, dump := dataNames(m.Data), false
	if m.Database == core.DatabaseSQLite {
		names = append(names, core.DatabaseFolder)
	}
	if before.Want != nil {
		if pm, err := h.parse(before.Want.Manifest, a.Repo); err == nil {
			dump = pm.Database == core.DatabasePostgres
			if pm.Database == core.DatabaseSQLite && m.Database != core.DatabaseSQLite {
				names = append(names, core.DatabaseFolder)
			}
		}
	}
	backup, err := h.backupData(ctx, a.ID, core.Prod, rel.Short(), names, dump)
	if err != nil {
		return fmt.Errorf("Prod's data couldn't be copied, so the old version is starting again: %w", err)
	}
	if _, err := h.store.SetWant(ctx, state.Deployment{App: a.ID, Env: core.Prod, Release: rel, By: actor, Kind: "promote", BackupPath: backup}); err != nil {
		return err
	}
	if backup != "" {
		h.logEvent(ctx, state.Event{Actor: actor, App: a.ID, Env: core.Prod, Action: "data.copied", Detail: "Copied prod's data to " + backup})
	}
	h.setBusy(a.ID, core.Prod, "Starting "+rel.Short())
	if h.applyAndWait(ctx, a, core.Prod, rel, "promote", actor) {
		h.startWatch(a.ID, rel, before.Want)
		return nil
	}
	return h.undoPromote(ctx, a, before.Want, rel, backup)
}

// undoPromote puts prod back the way it was before a promote whose new version didn't
// start or didn't become healthy: the previous version, with the data copied just
// before the switch (the new version may have changed it). The failed version's data is
// kept next to the other copies.
func (h *Hub) undoPromote(ctx context.Context, a state.App, prev *core.Release, failed core.Release, backup string) error {
	if prev == nil {
		return fmt.Errorf("%s isn't healthy, and there's no earlier prod version to go back to", failed.Short())
	}
	if err := h.store.AddHold(ctx, a.ID, failed.Commit, "It wasn't healthy right after it was promoted."); err != nil {
		h.log.Error("hold", "app", a.ID, "err", err)
	}
	h.setBusy(a.ID, core.Prod, "Going back to "+prev.Short())
	h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Prod, Action: "promote.reverting",
		Detail: fmt.Sprintf("%s isn't healthy, so prod is going back to %s with its data from before the promote", failed.Short(), prev.Short())})
	if err := h.ad.Deployer.StopApp(ctx, a.ID, core.Prod); err != nil {
		return fmt.Errorf("Couldn't stop %s to go back: %w", failed.Short(), err)
	}
	if backup != "" {
		kept, err := h.restoreData(ctx, a.ID, core.Prod, backup, failed.Short())
		if err != nil {
			return fmt.Errorf("Couldn't put back prod's data from %s: %w", backup, err)
		}
		if kept != "" {
			h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Prod, Action: "data.restored",
				Detail: "Put back prod's data from " + backup + "; the failed version's data is kept at " + kept})
		}
		// The failed version's database container is still running (it stayed up), so the
		// dump goes back into it. If that version had no database, nothing is running to
		// restore into, and the database's own folder was never changed.
		if _, err := os.Stat(filepath.Join(backup, dbDumpFile)); err == nil && h.hasPostgres(failed, a) {
			if err := h.ad.Deployer.RestoreDatabase(ctx, a.ID, core.Prod, filepath.Join(backup, dbDumpFile)); err != nil {
				return fmt.Errorf("Couldn't put back prod's database from %s: %w", backup, err)
			}
			h.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: a.ID, Env: core.Prod, Action: "data.restored",
				Detail: "Put back prod's database from " + backup})
		}
	}
	if _, err := h.store.SetWant(ctx, state.Deployment{App: a.ID, Env: core.Prod, Release: *prev, By: core.ActorHomelab, Kind: "rollback"}); err != nil {
		return err
	}
	if !h.applyAndWait(ctx, a, core.Prod, *prev, "rollback", core.ActorHomelab) {
		return fmt.Errorf("%s isn't healthy, and %s didn't come back healthy either", failed.Short(), prev.Short())
	}
	return fmt.Errorf("%s isn't healthy, so prod went back to %s", failed.Short(), prev.Short())
}

// ---- Rollback ----

// RollbackPreview says what a rollback would bring back.
type RollbackPreview struct {
	From     *ReleaseView `json:"from"`
	To       *ReleaseView `json:"to"` // nil when there's nothing earlier
	Blockers []string     `json:"blockers"`
	Note     string       `json:"note"`
}

// PreviewRollback finds the version an environment ran before the current one.
func (h *Hub) PreviewRollback(ctx context.Context, appID, env string) (RollbackPreview, error) {
	var p RollbackPreview
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return p, userErr("There's no app %q.", appID)
	}
	if env != core.Dev && env != core.Prod {
		return p, userErr("There's no environment %q.", env)
	}
	cur, prev, err := h.previous(ctx, appID, env)
	if err != nil {
		return p, err
	}
	if cur == nil {
		p.Blockers = append(p.Blockers, "Nothing is deployed here yet.")
		return p, nil
	}
	p.From = h.releaseView(a, cur)
	if prev == nil {
		p.Blockers = append(p.Blockers, "There's no earlier version to go back to.")
		return p, nil
	}
	p.To = h.releaseView(a, prev)
	p.Note = "The app's data stays as it is now. A copy of prod's data from before each promote is kept on this computer."
	h.mu.Lock()
	if b := h.st(appID, env).busy; b != "" {
		p.Blockers = append(p.Blockers, "Busy: "+b+".")
	}
	h.mu.Unlock()
	return p, nil
}

// previous returns the current release and the last good version before it. It replays the
// history: a deploy or promote adds a version, a rollback drops the versions it undid. Held
// versions (ones that failed) are never offered, and so two rollbacks in a row keep going back.
func (h *Hub) previous(ctx context.Context, appID, env string) (cur, prev *core.Release, err error) {
	e, err := h.store.Env(ctx, appID, env)
	if err != nil || e.Want == nil {
		return nil, nil, err
	}
	hist, err := h.store.History(ctx, appID, env, 50)
	if err != nil {
		return nil, nil, err
	}
	holds, err := h.store.Holds(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	held := map[string]bool{}
	for _, x := range holds {
		held[x.Commit] = true
	}
	var stack []core.Release
	for i := len(hist) - 1; i >= 0; i-- { // oldest first
		d := hist[i]
		if d.Kind == "rollback" {
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].Digest == d.Release.Digest {
					stack = stack[:j+1]
					break
				}
			}
		}
		if len(stack) == 0 || stack[len(stack)-1].Digest != d.Release.Digest {
			stack = append(stack, d.Release)
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].Digest != e.Want.Digest && !held[stack[i].Commit] {
			r := stack[i]
			return e.Want, &r, nil
		}
	}
	return e.Want, nil, nil
}

// Rollback brings back the version an environment ran before the current one.
func (h *Hub) Rollback(ctx context.Context, appID, env string, by core.Viewer) error {
	p, err := h.PreviewRollback(ctx, appID, env)
	if err != nil {
		return err
	}
	if len(p.Blockers) > 0 {
		return &UserError{Msg: strings.Join(p.Blockers, " ")}
	}
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return err
	}
	_, prev, err := h.previous(ctx, appID, env)
	if err != nil || prev == nil {
		return userErr("There's no earlier version to go back to.")
	}
	return h.setWantAndApply(ctx, a, env, *prev, "rollback", by, "Roll back to")
}

// ---- Reading ----

// Logs returns recent logs of an environment.
func (h *Hub) Logs(ctx context.Context, appID, env string, lines int) (string, error) {
	a, err := h.store.App(ctx, appID)
	if err != nil {
		return "", userErr("There's no app %q.", appID)
	}
	if lines <= 0 || lines > 2000 {
		lines = 200
	}
	out, err := h.ad.Deployer.Logs(ctx, appID, env, lines)
	if err != nil {
		return "", err
	}
	return h.mask(ctx, a, env, out), nil
}

// Events returns the activity log, newest first.
func (h *Hub) Events(ctx context.Context, appID string, before int64, limit int) ([]state.Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return h.store.Events(ctx, appID, before, limit)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
