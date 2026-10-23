package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// versionRig is a rig whose GitHub makes versions. The app is added, but nothing is deployed.
func versionRig(t *testing.T) (*rig, *fake.Versions) {
	t.Helper()
	r := newRig(t)
	v := fake.NewVersions()
	r.hub.ad.Versions = v
	r.sec.PlatformValues["GITHUB_TOKEN"] = "ghp_secret_token_value"
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(context.Background(), repo, "", owner); err != nil {
		t.Fatal(err)
	}
	return r, v
}

// ship pushes a commit, deploys it to dev and waits.
func (r *rig) ship(sha string) {
	r.t.Helper()
	r.push(sha)
	r.hub.CheckBuilds(context.Background())
	r.settle()
}

func (r *rig) promoteNow() {
	r.t.Helper()
	ctx := context.Background()
	r.hub.CheckHealth(ctx)
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		r.t.Fatal(err)
	}
	r.settle()
}

func TestDevBuildsGetADevVersionAndPromoteGetsTheProdVersion(t *testing.T) {
	r, v := versionRig(t)
	ctx := context.Background()

	r.ship("aaaaaaa1")
	dev := r.env(core.Dev)
	// Nothing runs on prod yet, so the first version is the first release.
	if dev.Version.Version != "v0.3.0-dev.1" || dev.Version.Summary != "1 new" || !strings.Contains(dev.Version.Notes, "something") {
		t.Fatalf("dev version: %+v", dev.Version)
	}
	if got := r.dep.Specs["movies/dev"].Vars["HOMELAB_VERSION"]; got != "v0.3.0-dev.1" {
		t.Fatalf("HOMELAB_VERSION on dev = %q", got)
	}
	// The preview says what prod will be called, without making it.
	p, err := r.hub.PreviewPromote(ctx, "movies")
	if err != nil || p.NextVersion != "v0.3.0" || p.Summary != "1 new" || p.To.Version != "v0.3.0-dev.1" {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if v.Called("preview prod aaaaaaa1") != 1 || v.Called("publish prod") != 0 {
		t.Fatalf("calls: %v", v.Calls)
	}

	r.promoteNow()
	prod := r.env(core.Prod)
	if prod.Version == nil || prod.Version.Version != "v0.3.0" {
		t.Fatalf("prod version: %+v", prod.Version)
	}
	if got := r.dep.Specs["movies/prod"].Vars["HOMELAB_VERSION"]; got != "v0.3.0" {
		t.Fatalf("HOMELAB_VERSION on prod = %q", got)
	}
	// The same commit, the same release: it's on the deploy record too.
	hist, _ := r.store.History(ctx, "movies", core.Prod, 1)
	if hist[0].Release.Version != "v0.3.0" {
		t.Fatalf("history: %+v", hist[0].Release)
	}
	if got := r.eventDetails("promote.done"); len(got) != 1 || !strings.HasPrefix(got[0], "v0.3.0 is running") {
		t.Fatalf("events: %v", got)
	}

	// The next build continues from prod's version.
	r.ship("bbbbbbb2")
	if got := r.env(core.Dev).Version.Version; got != "v0.4.0-dev.1" {
		t.Fatalf("next dev version %q", got)
	}
	r.ship("ccccccc3")
	if got := r.env(core.Dev).Version.Version; got != "v0.4.0-dev.2" {
		t.Fatalf("second dev version %q", got)
	}
	p, _ = r.hub.PreviewPromote(ctx, "movies")
	if p.NextVersion != "v0.4.0" || p.From.Version != "v0.3.0" || p.To.Version != "v0.4.0-dev.2" {
		t.Fatalf("preview: %+v", p)
	}
	r.promoteNow()
	if got := r.env(core.Prod).Version.Version; got != "v0.4.0" {
		t.Fatalf("prod version %q", got)
	}
	// The token went to the services and nowhere else.
	for _, token := range v.Tokens {
		if token != "ghp_secret_token_value" {
			t.Fatalf("token %q", token)
		}
	}
}

func TestRollbackKeepsTheVersionOfTheOldRelease(t *testing.T) {
	r, _ := versionRig(t)
	ctx := context.Background()
	r.ship("aaaaaaa1")
	r.promoteNow()
	r.ship("bbbbbbb2")
	r.promoteNow()
	if err := r.hub.Rollback(ctx, "movies", core.Prod, owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.env(core.Prod).Version.Version; got != "v0.3.0" {
		t.Fatalf("after rollback prod is %q", got)
	}
	if got := r.dep.Specs["movies/prod"].Vars["HOMELAB_VERSION"]; got != "v0.3.0" {
		t.Fatalf("HOMELAB_VERSION after rollback = %q", got)
	}
}

func TestGitHubRefusingDoesNotBlockADeploy(t *testing.T) {
	r, v := versionRig(t)
	ctx := context.Background()
	v.Set(fmt.Errorf("%w: GitHub POST /repos/rupaai/movies/releases gave 403: Resource not accessible", core.ErrGitHubRefused))

	r.ship("aaaaaaa1")
	if got := r.running(core.Dev); got != "aaaaaaa1" {
		t.Fatalf("dev runs %q", got)
	}
	dev := r.env(core.Dev)
	if dev.Version.Version != "" {
		t.Fatalf("dev got version %q", dev.Version.Version)
	}
	if _, ok := r.dep.Specs["movies/dev"].Vars["HOMELAB_VERSION"]; ok {
		t.Fatal("HOMELAB_VERSION set without a version")
	}
	app, _ := r.hub.App(ctx, "movies")
	if app.VersionProblem != "Can't make versions on GitHub: the token needs Contents: read and write." {
		t.Fatalf("problem: %q", app.VersionProblem)
	}
	if got := r.eventDetails("version.failed"); len(got) != 1 || strings.Contains(got[0], "ghp_") {
		t.Fatalf("events: %v", got)
	}

	// Promote still works. Dev's own version isn't copied to prod.
	r.promoteNow()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q", got)
	}
	if got := r.env(core.Prod).Version.Version; got != "" {
		t.Fatalf("prod got version %q", got)
	}

	// Once the token can write, the next build gets a version and the problem goes away.
	v.Set(nil)
	r.ship("bbbbbbb2")
	app, _ = r.hub.App(ctx, "movies")
	if app.VersionProblem != "" || r.env(core.Dev).Version.Version == "" {
		t.Fatalf("problem %q, version %q", app.VersionProblem, r.env(core.Dev).Version.Version)
	}
}

func TestOtherGitHubProblemsAreShownToo(t *testing.T) {
	r, v := versionRig(t)
	v.Set(errors.New("cannot reach GitHub"))
	r.ship("aaaaaaa1")
	app, _ := r.hub.App(context.Background(), "movies")
	if !strings.Contains(app.VersionProblem, "Can't make versions on GitHub right now: cannot reach GitHub") {
		t.Fatalf("problem: %q", app.VersionProblem)
	}
	if r.running(core.Dev) != "aaaaaaa1" {
		t.Fatal("not deployed")
	}
}

func TestNoGitHubTokenMeansNoVersionAndAProblem(t *testing.T) {
	r, v := versionRig(t)
	delete(r.sec.PlatformValues, "GITHUB_TOKEN")
	r.ship("aaaaaaa1")
	app, _ := r.hub.App(context.Background(), "movies")
	if !strings.Contains(app.VersionProblem, "no GitHub token") || r.running(core.Dev) != "aaaaaaa1" {
		t.Fatalf("problem %q", app.VersionProblem)
	}
	if len(v.Calls) != 0 {
		t.Fatalf("called GitHub without a token: %v", v.Calls)
	}
}

func TestAnAppWithoutVersionsAtAllIsUnchanged(t *testing.T) {
	r := newRig(t) // no Versions adapter
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(context.Background(), repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.ship("aaaaaaa1")
	app, _ := r.hub.App(context.Background(), "movies")
	if app.VersionProblem != "" || r.env(core.Dev).Version.Version != "" {
		t.Fatalf("%+v", app)
	}
}

// An app that already runs on prod, with no release on GitHub, gets its first one once.
func TestTheFirstProdReleaseIsMadeForWhatProdRunsNow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	// Before versions: build, promote.
	r.ship("aaaaaaa1")
	r.promoteNow()
	if r.env(core.Prod).Version.Version != "" {
		t.Fatal("prod has a version already")
	}
	// Versions arrive (an upgrade of Homelab).
	v := fake.NewVersions()
	r.hub.ad.Versions = v
	r.sec.PlatformValues["GITHUB_TOKEN"] = "ghp_secret_token_value"

	r.push("bbbbbbb2")
	r.hub.CheckBuilds(ctx)
	r.settle()
	prod := r.env(core.Prod)
	if prod.Version.Version != "v0.3.0" || prod.Version.Commit != "aaaaaaa1" {
		t.Fatalf("prod: %+v", prod.Version)
	}
	// The version is on the deploy record, and the container isn't restarted for it.
	hist, _ := r.store.History(ctx, "movies", core.Prod, 5)
	if len(hist) != 1 || hist[0].Release.Version != "v0.3.0" {
		t.Fatalf("history: %+v", hist)
	}
	if r.dep.Specs["movies/prod"].Release.Version != "" {
		t.Fatal("prod was redeployed for a label")
	}
	// The new build continues from it.
	if got := r.env(core.Dev).Version.Version; got != "v0.4.0-dev.1" {
		t.Fatalf("dev: %q", got)
	}
	if got := r.eventDetails("version.first"); len(got) != 1 {
		t.Fatalf("events: %v", got)
	}
	// Only once.
	r.advance(time.Hour)
	r.push("ccccccc3")
	r.hub.CheckBuilds(ctx)
	r.settle()
	if v.Called("publish prod") != 1 || v.Called("list") != 1 {
		t.Fatalf("calls: %v", v.Calls)
	}
}

func TestAnAppThatHasReleasesIsNotGivenAFirstOne(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.ship("aaaaaaa1")
	r.promoteNow()
	v := fake.NewVersions()
	v.Releases = []core.ReleaseInfo{
		{Tag: "v1.2.0-dev.1", Prerelease: true, Commit: "zzz"},
		{Tag: "v1.1.0", Commit: "aaaaaaa1", Summary: "2 new", Notes: "## New\n"},
	}
	r.hub.ad.Versions = v
	r.sec.PlatformValues["GITHUB_TOKEN"] = "ghp_secret_token_value"
	r.hub.bootstrapVersions(ctx, mustApp(t, r))
	if v.Called("publish") != 0 {
		t.Fatalf("calls: %v", v.Calls)
	}
	// Prod's commit is that release, so prod learns its version.
	if got := r.env(core.Prod).Version.Version; got != "v1.1.0" {
		t.Fatalf("prod: %q", got)
	}
}

func TestAFailedFirstReleaseIsTriedAgainLater(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.ship("aaaaaaa1")
	r.promoteNow()
	v := fake.NewVersions()
	v.Set(core.ErrGitHubRefused)
	r.hub.ad.Versions = v
	r.sec.PlatformValues["GITHUB_TOKEN"] = "ghp_secret_token_value"
	a := mustApp(t, r)
	r.hub.bootstrapVersions(ctx, a)
	r.hub.bootstrapVersions(ctx, a) // too soon: not tried again
	if v.Called("list") != 1 {
		t.Fatalf("calls: %v", v.Calls)
	}
	v.Set(nil)
	r.advance(versionRetry + time.Minute)
	r.hub.bootstrapVersions(ctx, a)
	if got := r.env(core.Prod).Version.Version; got != "v0.3.0" {
		t.Fatalf("prod: %q", got)
	}
}

func mustApp(t *testing.T, r *rig) (a state.App) {
	t.Helper()
	a, err := r.store.App(context.Background(), "movies")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAnAppWithOneEnvironmentMakesProdReleases(t *testing.T) {
	for _, tc := range []struct {
		envs []string
		env  string
		want string
	}{
		{[]string{"dev", "prod"}, "dev", "dev"},
		{[]string{"dev", "prod"}, "prod", "prod"},
		{[]string{"prod"}, "prod", "prod"},
		{[]string{"dev"}, "dev", "prod"},
	} {
		if got := versionChannel(tc.envs, tc.env); got != tc.want {
			t.Errorf("%v %s: %s, want %s", tc.envs, tc.env, got, tc.want)
		}
	}
}

func TestReleasesHeadersCarryTheRepoAndTheToken(t *testing.T) {
	r, _ := versionRig(t)
	ctx := context.Background()
	h, err := r.hub.ReleasesHeaders(ctx, "movies")
	if err != nil || h.Get("X-Homelab-Repo") != repo || h.Get("X-Homelab-Github-Token") != "ghp_secret_token_value" {
		t.Fatalf("%v %v", h, err)
	}
	if _, err := r.hub.ReleasesHeaders(ctx, "nope"); err == nil {
		t.Fatal("headers for an app that doesn't exist")
	}
	delete(r.sec.PlatformValues, "GITHUB_TOKEN")
	if _, err := r.hub.ReleasesHeaders(ctx, "movies"); err == nil {
		t.Fatal("headers without a token")
	}
}
