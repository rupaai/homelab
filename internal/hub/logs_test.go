package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

// logRig is a rig with a fake log store and the movies app added.
func logRig(t *testing.T, doc string) (*rig, *fake.Logs) {
	t.Helper()
	r := newRig(t)
	l := fake.NewLogs()
	l.EventSeen = make(chan struct{}, 64)
	r.hub.ad.Logs = l
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(doc))
	if _, err := r.hub.AddApp(context.Background(), repo, "", owner); err != nil {
		t.Fatal(err)
	}
	// AddApp logs events; they are not what these tests look at.
	waitEvents(t, l, 1)
	l.Ingested, l.Events = nil, nil
	return r, l
}

func waitEvents(t *testing.T, l *fake.Logs, n int) []core.LogEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ev := l.Calls(); len(ev) >= n {
			return ev
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, ev := l.Calls()
	t.Fatalf("wanted %d events, got %d: %+v", n, len(ev), ev)
	return nil
}

func batch(env string, lines ...string) core.LogBatch {
	b := core.LogBatch{App: "movies", Env: env, Service: "app"}
	for i, s := range lines {
		b.Lines = append(b.Lines, core.LogLine{T: int64(1000 + i), Stream: "stdout", Text: s})
	}
	return b
}

func TestIngestLogsMasksSecretValuesBeforeTheyReachTheLogs(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	ctx := context.Background()
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "tmdb-secret-value", "t")
	r.sec.PlatformValues["GITHUB_TOKEN"] = "ghp_secret_token_value"

	err := r.hub.IngestLogs(ctx, batch(core.Prod, "calling tmdb with tmdb-secret-value now", "token ghp_secret_token_value", "fine"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := l.Calls()
	if len(got) != 1 {
		t.Fatalf("ingests: %+v", got)
	}
	texts := []string{}
	for _, ln := range got[0].Batch.Lines {
		texts = append(texts, ln.Text)
	}
	all := strings.Join(texts, "\n")
	if strings.Contains(all, "tmdb-secret-value") || strings.Contains(all, "ghp_secret_token_value") {
		t.Fatalf("a secret reached the logs: %q", all)
	}
	if texts[0] != "calling tmdb with [secret TMDB_API_KEY] now" || texts[1] != "token [secret GITHUB_TOKEN]" || texts[2] != "fine" {
		t.Fatalf("texts = %q", texts)
	}
	if got[0].Batch.App != "movies" || got[0].Batch.Env != core.Prod || got[0].Batch.Service != "app" || got[0].Batch.Lines[0].T != 1000 {
		t.Fatalf("batch = %+v", got[0].Batch)
	}
}

func TestIngestLogsDoesNotChangeTheCallersBatch(t *testing.T) {
	r, _ := logRig(t, manifestDoc)
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "tmdb-secret-value", "t")
	b := batch(core.Prod, "tmdb-secret-value")
	if err := r.hub.IngestLogs(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if b.Lines[0].Text != "tmdb-secret-value" {
		t.Fatalf("the caller's lines were changed: %+v", b.Lines)
	}
}

func TestIngestLogsMinLevelDefaultsAndManifest(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	ctx := context.Background()
	_ = r.hub.IngestLogs(ctx, batch(core.Dev, "a"))
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "a"))
	got, _ := l.Calls()
	if len(got) != 2 || got[0].MinLevel != "debug" || got[1].MinLevel != "info" {
		t.Fatalf("defaults: %+v", got)
	}

	r2, l2 := logRig(t, manifestDoc+"logs:\n  min_level: warn\n")
	_ = r2.hub.IngestLogs(ctx, batch(core.Dev, "a"))
	_ = r2.hub.IngestLogs(ctx, batch(core.Prod, "a"))
	got, _ = l2.Calls()
	if len(got) != 2 || got[0].MinLevel != "warn" || got[1].MinLevel != "warn" {
		t.Fatalf("manifest: %+v", got)
	}
}

func TestIngestLogsSkipsAppsHomelabDoesNotKnow(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	b := batch(core.Prod, "hello")
	b.App = "stranger"
	if err := r.hub.IngestLogs(context.Background(), b); err != nil {
		t.Fatalf("an unknown app is not an error: %v", err)
	}
	if got, _ := l.Calls(); len(got) != 0 {
		t.Fatalf("stored lines of an unknown app: %+v", got)
	}
}

func TestIngestLogsWithoutALogStoreDoesNothing(t *testing.T) {
	r := newRig(t)
	if err := r.hub.IngestLogs(context.Background(), batch(core.Prod, "x")); err != nil {
		t.Fatal(err)
	}
}

func TestIngestLogsPassesOnAFailureSoTheSenderKeepsTheLines(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	l.Set(core.ErrNotFound)
	if err := r.hub.IngestLogs(context.Background(), batch(core.Prod, "x")); err == nil {
		t.Fatal("a failed store was reported as success")
	}
}

func TestIngestLogsRemembersTheSecretsForFiveSeconds(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	ctx := context.Background()
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "first-secret-value", "t")
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "first-secret-value"))
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "second-secret-value", "t")

	r.advance(3 * time.Second)
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "second-secret-value"))
	r.advance(3 * time.Second)
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "second-secret-value"))

	got, _ := l.Calls()
	if len(got) != 3 {
		t.Fatalf("ingests: %d", len(got))
	}
	if got[0].Batch.Lines[0].Text != "[secret TMDB_API_KEY]" {
		t.Errorf("first: %q", got[0].Batch.Lines[0].Text)
	}
	if got[1].Batch.Lines[0].Text != "second-secret-value" { // still the old cache
		t.Errorf("within five seconds the old secrets are used: %q", got[1].Batch.Lines[0].Text)
	}
	if got[2].Batch.Lines[0].Text != "[secret TMDB_API_KEY]" {
		t.Errorf("after five seconds the new secret is hidden: %q", got[2].Batch.Lines[0].Text)
	}
}

func TestActivityEventsAreMirroredToTheLogs(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	ctx := context.Background()
	r.hub.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: "movies", Env: core.Prod, Action: "deploy.done", Detail: "Deployed abc"})
	r.hub.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: "movies", Env: core.Prod, Action: "promote.failed", Detail: "Prod didn't start"})
	r.hub.logEvent(ctx, state.Event{Actor: "Labiba", Action: "homelab.started", Detail: "Homelab started"})
	r.hub.logEvent(ctx, state.Event{Actor: "Labiba", App: "movies", Env: core.Prod, Action: "secret.set", Detail: "Set TMDB_API_KEY in prod"})
	ev := waitEvents(t, l, 4)
	byMsg := map[string]core.LogEvent{}
	for _, e := range ev {
		byMsg[e.Msg] = e
	}
	if e := byMsg["Deployed abc"]; e.Service != "deploys" || e.Level != "info" || e.AboutApp != "movies" || e.AboutEnv != core.Prod {
		t.Errorf("deploy: %+v", e)
	}
	if e := byMsg["Prod didn't start"]; e.Service != "deploys" || e.Level != "error" || e.AboutApp != "" || e.Extra["app"] != "movies" {
		t.Errorf("failed promote: %+v", e)
	}
	if e := byMsg["Homelab started"]; e.Service != "homelab" || e.Level != "info" || e.AboutApp != "" || e.AboutEnv != "" {
		t.Errorf("started: %+v", e)
	}
	if e := byMsg["Set TMDB_API_KEY in prod"]; e.Service != "secret" || e.Level != "info" {
		t.Errorf("secret: %+v", e)
	}
}

func TestLogEventServices(t *testing.T) {
	for action, want := range map[string]string{
		"deploy.started": "deploys", "deploy.failed": "deploys", "promote.reverting": "deploys", "rollback.auto": "deploys",
		"build.failed": "deploys", "key.scopes": "key", "db.restored": "db", "homelab.updated": "homelab",
		"health.": "health", "plain": "plain", "": "homelab", ".x": "homelab", "Odd Name.x": "odd-name",
	} {
		if got := logEventService(action); got != want {
			t.Errorf("%q -> %q, want %q", action, got, want)
		}
	}
}

// slowLogs never answers until its context ends.
type slowLogs struct {
	fake.Logs
	deadline chan time.Time
}

func (s *slowLogs) Event(ctx context.Context, _ core.LogEvent) error {
	d, _ := ctx.Deadline()
	s.deadline <- d
	<-ctx.Done()
	return ctx.Err()
}

func TestAStuckLogStoreNeverBlocksTheActivityLog(t *testing.T) {
	r := newRig(t)
	s := &slowLogs{deadline: make(chan time.Time, 1)}
	r.hub.ad.Logs = s
	start := time.Now()
	r.hub.logEvent(context.Background(), state.Event{Actor: core.ActorHomelab, Action: "homelab.started", Detail: "x"})
	if time.Since(start) > time.Second {
		t.Fatalf("logEvent waited for the log store: %v", time.Since(start))
	}
	select {
	case d := <-s.deadline:
		if until := time.Until(d); until <= 0 || until > 5*time.Second {
			t.Fatalf("the send has %v to finish, want at most 5s", until)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the event was never sent")
	}
	// The event is still in the activity log.
	evs, err := r.store.Events(context.Background(), "", 0, 10)
	if err != nil || len(evs) == 0 {
		t.Fatalf("activity log: %v %v", evs, err)
	}
}

func TestASecretMadeByHomelabIsMaskedAtOnce(t *testing.T) {
	r, l := logRig(t, manifestDoc+"generate_secrets: [SESSION_KEY]\n")
	r.addPeople()
	ctx := context.Background()
	texts := func(i int) string { c, _ := l.Calls(); return c[i].Batch.Lines[0].Text }

	// The cache is warm with no secret in it.
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "nothing here"))
	if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "brand-new-secret", owner); err != nil {
		t.Fatal(err)
	}
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "key is brand-new-secret"))
	if got := texts(1); got != "key is [secret TMDB_API_KEY]" {
		t.Fatalf("after SetSecret: %q", got)
	}

	// A regenerated secret.
	if err := r.hub.RegenerateSecret(ctx, "movies", core.Prod, "SESSION_KEY", owner); err != nil {
		t.Fatal(err)
	}
	v, _ := r.sec.Inject("movies", core.Prod, []string{"SESSION_KEY"})
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "now "+v["SESSION_KEY"]))
	if got := texts(2); v["SESSION_KEY"] == "" || strings.Contains(got, v["SESSION_KEY"]) {
		t.Fatalf("after RegenerateSecret: %q", got)
	}

	// A platform setting touches every app.
	if err := r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_just_saved_token", owner); err != nil {
		t.Fatal(err)
	}
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "ghp_just_saved_token"))
	if got := texts(3); got != "[secret GITHUB_TOKEN]" {
		t.Fatalf("after SetPlatformSetting: %q", got)
	}

	// A deleted secret is no longer hidden (and no longer a secret).
	if err := r.hub.DeleteSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", owner); err != nil {
		t.Fatal(err)
	}
	_ = r.hub.IngestLogs(ctx, batch(core.Prod, "brand-new-secret"))
	if got := texts(4); got != "brand-new-secret" {
		t.Fatalf("after DeleteSecret: %q", got)
	}
}

func TestADeployForgetsTheRememberedSecrets(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	ctx := context.Background()
	_ = r.hub.IngestLogs(ctx, batch(core.Dev, "x"))
	// The app saved a secret itself, which Homelab did not see change.
	r.sec.Set("movies", core.Dev, "SAVED_BY_APP", "app-saved-secret", "app")
	_ = r.hub.IngestLogs(ctx, batch(core.Dev, "app-saved-secret"))
	if c, _ := l.Calls(); c[1].Batch.Lines[0].Text != "app-saved-secret" {
		t.Fatalf("the cache was not warm: %q", c[1].Batch.Lines[0].Text)
	}
	r.ship("aaaaaaa1") // deploys dev
	_ = r.hub.IngestLogs(ctx, batch(core.Dev, "app-saved-secret"))
	c, _ := l.Calls()
	if got := c[len(c)-1].Batch.Lines[0].Text; got != "[secret SAVED_BY_APP]" {
		t.Fatalf("after a deploy: %q", got)
	}
}

func TestEventsAboutAnAppNeverCarryWhatTheAppMustNotSee(t *testing.T) {
	r, l := logRig(t, manifestDoc)
	ctx := context.Background()
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "tmdb-secret-value", "t")

	// An error that quotes a secret: not linked to the app, and masked anyway.
	r.hub.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: "movies", Env: core.Prod, Action: "deploy.failed", Detail: "compose said tmdb-secret-value"})
	// Health and smoke text come from the app.
	r.hub.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: "movies", Env: core.Prod, Action: "health.unhealthy", Detail: "it said tmdb-secret-value"})
	r.hub.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: "movies", Env: core.Dev, Action: "smoke.failed", Detail: "output"})
	// A key's use names the key; a person's change names the person.
	r.hub.logEvent(ctx, state.Event{Actor: "Labiba", App: "movies", Env: core.Prod, Action: "secret.set", Detail: "Set TMDB_API_KEY for prod"})
	r.hub.logEvent(ctx, state.Event{Actor: core.ActorHomelab, App: "movies", Env: core.Prod, Action: "deploy.done", Detail: "v1 is running"})
	ev := waitEvents(t, l, 5)
	for _, e := range ev {
		if strings.Contains(e.Msg, "tmdb-secret-value") {
			t.Errorf("a secret reached an event: %+v", e)
		}
		switch e.Extra["action"] {
		case "deploy.failed", "health.unhealthy", "smoke.failed":
			if e.AboutApp != "" || e.AboutEnv != "" || e.Extra["app"] != "movies" {
				t.Errorf("%v is linked to the app: %+v", e.Extra["action"], e)
			}
		case "secret.set":
			if e.AboutApp != "movies" {
				t.Errorf("secret.set is not about the app: %+v", e)
			}
			if _, has := e.Extra["actor"]; has {
				t.Errorf("a person's name is in an event the app can read: %+v", e)
			}
		case "deploy.done":
			if e.AboutApp != "movies" || e.Extra["actor"] != core.ActorHomelab {
				t.Errorf("deploy.done: %+v", e)
			}
		}
	}
	for _, e := range ev {
		if e.Extra["action"] == "deploy.failed" && !strings.Contains(e.Msg, "[secret TMDB_API_KEY]") {
			t.Errorf("the failed deploy was not masked: %q", e.Msg)
		}
	}
}
