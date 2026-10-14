package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/identity"
)

const autoManifest = `
id: movies
name: Movie Night
image: ghcr.io/rupaai/movies
tag: "sha-{commit}"
port: 8000
health: /healthz
promotion: auto
promote_after: 30m
smoke_test: [python, -m, app.smoke]
data: [{name: db, path: /data}]
secrets: [TMDB_API_KEY]
`

func (r *rig) pushWith(sha, doc string) {
	r.src.Push(repo, "main", sha, "change "+sha, []byte(doc))
	r.reg.Put("ghcr.io/rupaai/movies", "sha-"+sha, "sha256:"+sha)
	r.build.Set(repo, core.Build{Commit: sha, Status: core.BuildPassed, URL: "https://ci/" + sha})
}

func (r *rig) auto() *autoState {
	r.t.Helper()
	v, err := r.hub.App(context.Background(), "movies")
	if err != nil {
		r.t.Fatal(err)
	}
	return v.AutoPromote
}

func (r *rig) events(action string) []string {
	evs, _ := r.store.Events(context.Background(), "movies", 0, 200)
	var out []string
	for _, e := range evs {
		if e.Action == action {
			out = append(out, e.Actor+": "+e.Detail)
		}
	}
	return out
}

func TestAutoPromote(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.advance(time.Second) // freeze the clock
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(autoManifest))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.pushWith("aaaaaaa1", autoManifest)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)

	// A prod secret is missing: blocked.
	r.hub.AutoPromote(ctx)
	if a := r.auto(); a == nil || a.State != "blocked" || !strings.Contains(a.Detail, "TMDB_API_KEY") {
		t.Fatalf("auto = %+v", a)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")

	// Healthy, but not for long enough yet.
	r.hub.AutoPromote(ctx)
	if a := r.auto(); a.State != "waiting" || a.At == nil || a.At.Sub(r.now()) < 29*time.Minute {
		t.Fatalf("auto = %+v", a)
	}
	if r.running(core.Prod) != "" || len(r.smoke.Runs) != 0 {
		t.Fatal("promoted too early")
	}

	// 31 minutes later: smoke test, then promote.
	r.advance(31 * time.Minute)
	r.hub.CheckHealth(ctx)
	r.hub.AutoPromote(ctx)
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q", got)
	}
	if len(r.smoke.Runs) != 1 || r.smoke.Runs[0].Env != core.Dev || r.smoke.Runs[0].Port != 8000 || r.smoke.Runs[0].Command[0] != "python" {
		t.Fatalf("smoke runs = %+v", r.smoke.Runs)
	}
	if ev := r.events("promote.started"); len(ev) != 1 || !strings.HasPrefix(ev[0], "homelab: Auto-promote") {
		t.Fatalf("promote events = %v", ev)
	}
	r.hub.AutoPromote(ctx)
	if a := r.auto(); a.State != "done" {
		t.Fatalf("auto after promote = %+v", a)
	}

	// A build whose smoke test fails is held, and never retried by itself.
	r.smoke.Fail["bbbbbbb2"] = true
	r.pushWith("bbbbbbb2", autoManifest)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.hub.CheckHealth(ctx)
	r.advance(31 * time.Minute)
	r.hub.CheckHealth(ctx)
	r.hub.AutoPromote(ctx)
	r.hub.AutoPromote(ctx)
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q after a failed smoke test", got)
	}
	if len(r.smoke.Runs) != 2 {
		t.Fatalf("smoke ran %d times", len(r.smoke.Runs))
	}
	v, _ := r.hub.App(ctx, "movies")
	if len(v.Held) != 1 || v.Held[0].Commit != "bbbbbbb2" || !strings.Contains(v.Held[0].Reason, "expected 200") {
		t.Fatalf("held = %+v", v.Held)
	}
	if v.AutoPromote.State != "held" || v.Mirror == nil || v.PromoteAfter != "30m" {
		t.Fatalf("view = %+v", v)
	}
	// People can still promote a held build by hand.
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := r.running(core.Prod); got != "bbbbbbb2" {
		t.Fatalf("prod runs %q after a manual promote", got)
	}
}

func TestPromotionModeIsASetting(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(autoManifest))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.SetPromotion(ctx, "movies", "sometimes", owner); err == nil {
		t.Fatal("bad mode accepted")
	}
	v, err := r.hub.SetPromotion(ctx, "movies", core.PromotionManual, owner)
	if err != nil || v.Promotion != core.PromotionManual || v.AutoPromote != nil {
		t.Fatalf("set = %+v %v", v, err)
	}
	// A new build (whose manifest says auto) doesn't change it back, and isn't deployed.
	r.pushWith("aaaaaaa1", autoManifest)
	r.hub.CheckBuilds(ctx)
	r.settle()
	if a, _ := r.store.App(ctx, "movies"); a.Promotion != core.PromotionManual {
		t.Fatalf("promotion = %s", a.Promotion)
	}
	if r.running(core.Dev) != "" {
		t.Fatal("manual app deployed by itself")
	}
}

// promoteBoth puts a on prod, then b on dev and prod, by hand.
func (r *rig) promoteBoth(a, b string) {
	ctx := context.Background()
	r.src.Push(repo, "main", a, "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		r.t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	for _, sha := range []string{a, b} {
		r.push(sha)
		r.hub.CheckBuilds(ctx)
		r.settle()
		r.hub.CheckHealth(ctx)
		if err := r.hub.Promote(ctx, "movies", owner); err != nil {
			r.t.Fatal(err)
		}
		r.settle()
	}
	if got := r.running(core.Prod); got != b {
		r.t.Fatalf("prod runs %q", got)
	}
}

func TestWatchRollsBackAndKeepsData(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	data := filepath.Join(r.dir, "apps", "movies", "prod", "data", "db", "new.db")
	if err := os.WriteFile(data, []byte("written by b"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.health.SetFailWhen(func(u string) bool {
		return strings.Contains(u, ":17011/") && r.running(core.Prod) == "bbbbbbb2"
	})
	r.hub.CheckHealth(ctx) // one bad check isn't enough
	if got := r.running(core.Prod); got != "bbbbbbb2" {
		t.Fatalf("rolled back after one check: %q", got)
	}
	r.hub.CheckHealth(ctx)
	r.settle()
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q, want the previous version", got)
	}
	if ev := r.events("rollback.auto"); len(ev) != 1 || !strings.HasPrefix(ev[0], "homelab: bbbbbbb") {
		t.Fatalf("events = %v", ev)
	}
	if b, err := os.ReadFile(data); err != nil || string(b) != "written by b" {
		t.Fatalf("data after auto rollback: %q %v", b, err)
	}
	holds, _ := r.store.Holds(ctx, "movies")
	if len(holds) != 1 || holds[0].Commit != "bbbbbbb2" {
		t.Fatalf("holds = %+v", holds)
	}
}

func TestWatchEndsAfterItsWindow(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.advance(11 * time.Minute)
	r.health.SetFailing("http://127.0.0.1:17011/healthz", true)
	r.hub.CheckHealth(ctx)
	r.hub.CheckHealth(ctx)
	r.hub.CheckHealth(ctx)
	r.settle()
	if got := r.running(core.Prod); got != "bbbbbbb2" {
		t.Fatalf("rolled back after the watch ended: %q", got)
	}
}

func TestIdentityHeaders(t *testing.T) {
	r := newRig(t)
	got := make(chan http.Header, 1)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { got <- req.Header.Clone() }))
	defer app.Close()
	u, _ := url.Parse(app.URL)
	port, _ := strconv.Atoi(u.Port())
	r.hub.cfg.Owners = []string{"labiba@example.com"}
	proxy := r.hub.appProxy(port, "movies", "Movie Night", core.Dev)
	pub := r.hub.cfg.Identity.PublicKey()

	send := func(v core.Viewer) http.Header {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Homelab-User", "forged")
		req = req.WithContext(core.WithViewer(req.Context(), v))
		proxy.ServeHTTP(httptest.NewRecorder(), req)
		return <-got
	}
	h := send(core.Viewer{Local: true})
	p, err := identity.Verify(h, pub, "movies", core.Dev, time.Now())
	if err != nil || p == nil || p.User != "owner" || p.Groups[0] != "role:owner" {
		t.Fatalf("local = %+v %v", p, err)
	}
	h = send(core.Viewer{Login: "Labiba@example.com", Name: "Labiba"})
	if p, err := identity.Verify(h, pub, "movies", core.Dev, time.Now()); err != nil || p == nil || p.Name != "Labiba" {
		t.Fatalf("owner = %+v %v", p, err)
	}
	h = send(core.Viewer{Login: "guest@example.com"})
	if h.Get("X-Homelab-User") != "" || h.Get("X-Homelab-Signature") != "" {
		t.Fatalf("stranger got identity headers: %v", h)
	}
}

func TestRemoveApp(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.sec.Set("movies", core.Dev, "TMDB_API_KEY", "d", "t")
	if err := r.hub.RemoveApp(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	if r.running(core.Prod) != "" || r.running(core.Dev) != "" {
		t.Fatal("still running")
	}
	if _, ok := r.addr.Handlers["movies"]; ok {
		t.Fatal("address still served")
	}
	if l, _ := r.sec.List("movies", core.Prod); len(l) != 0 {
		t.Fatal("secrets kept")
	}
	if _, err := r.hub.App(ctx, "movies"); err == nil {
		t.Fatal("app still registered")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "apps", "movies", "prod", "data", "db")); err != nil {
		t.Fatalf("data removed: %v", err)
	}
	// It can be added again, with a fresh history.
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	if e := r.env(core.Prod); e.Version != nil || e.CanRollback {
		t.Fatalf("old history came back: %+v", e)
	}
	// Adding it starts a build check in the background; let it finish before cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for r.running(core.Dev) == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r.settle()
}

func TestSecretsViewAndLogMasking(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.sec.Set("movies", core.Prod, "OLD_KEY", "x", "t")
	envs, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	if envs[0].Env != core.Dev || len(envs[0].Secrets) != 1 || envs[0].Secrets[0].Set {
		t.Fatalf("dev = %+v", envs[0])
	}
	if p := envs[1].Secrets; len(p) != 2 || !p[0].Set || p[1].Name != "OLD_KEY" || p[1].Declared {
		t.Fatalf("prod = %+v", p)
	}
	if e := r.env(core.Dev); len(e.MissingSecrets) != 1 {
		t.Fatalf("dev missing = %v", e.MissingSecrets)
	}
	if err := r.hub.SetSecret(ctx, "movies", "staging", "A", "v", owner); err == nil {
		t.Fatal("unknown env accepted")
	}
	if err := r.hub.SetPlatformSetting(ctx, "HOMELAB_IDENTITY_KEY", "v", owner); err == nil {
		t.Fatal("internal setting accepted")
	}
	if err := r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_secret_token", owner); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-secret", owner); err != nil {
		t.Fatal(err)
	}
	r.dep.LogTail = "using key tmdb-secret and token ghp_secret_token\n"
	out, err := r.hub.Logs(ctx, "movies", core.Prod, 10)
	if err != nil || strings.Contains(out, "tmdb-secret") || strings.Contains(out, "ghp_secret_token") || !strings.Contains(out, "••••••") {
		t.Fatalf("logs = %q %v", out, err)
	}
	set, _ := r.hub.PlatformSettings(ctx)
	if len(set) != 2 || set[0].Name != "GITHUB_TOKEN" || !set[0].Set || set[1].Set {
		t.Fatalf("settings = %+v", set)
	}
}
