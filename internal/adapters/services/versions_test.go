package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestPublishAsksAsTheSystemCallerWithTheTokenInTheBody(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(200, `{"version":"v0.4.0-dev.1","released":true,"existing":false,"previous":"v0.3.0","summary":"2 new, 1 fix","notes":"## New\n"}`)
	got, err := s.Versions().Publish(context.Background(), "rupaai/demo", "abc123", "dev", "ghp_token", false)
	if err != nil || got.Version != "v0.4.0-dev.1" || got.Summary != "2 new, 1 fix" || !got.Released || got.Previous != "v0.3.0" {
		t.Fatalf("%+v %v", got, err)
	}
	c := b.last(t, s)
	if c.method != "POST" || c.uri != "/internal/releases/publish" || c.caller.Kind != core.ServiceKindSystem {
		t.Fatalf("%s %s %+v", c.method, c.uri, c.caller)
	}
	for _, want := range []string{`"repo":"rupaai/demo"`, `"commit":"abc123"`, `"channel":"dev"`, `"token":"ghp_token"`, `"always":true`, `"dry_run":false`} {
		if !strings.Contains(c.body, want) {
			t.Errorf("body %s lacks %s", c.body, want)
		}
	}
	if strings.Contains(c.uri, "ghp_") {
		t.Error("the token is in the address")
	}
}

func TestListReadsTheReleases(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(200, `{"releases":[{"tag":"v0.3.0","prerelease":false,"commit":"abc","date":"2026-01-01T00:00:00Z","summary":"1 fix","notes":"- x"}]}`)
	got, err := s.Versions().List(context.Background(), "rupaai/demo", "ghp_token")
	if err != nil || len(got) != 1 || got[0].Tag != "v0.3.0" || got[0].Commit != "abc" || got[0].Summary != "1 fix" {
		t.Fatalf("%+v %v", got, err)
	}
	if c := b.last(t, s); c.uri != "/internal/releases/list" || !strings.Contains(c.body, `"token":"ghp_token"`) {
		t.Fatalf("%s %s", c.uri, c.body)
	}
}

func TestAGitHubRefusalIsErrGitHubRefusedAndOtherProblemsAreNot(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(403, `{"error":"GitHub POST /repos/a/b/releases gave 403: Resource not accessible"}`)
	_, err := s.Versions().Publish(context.Background(), "a/b", "abc", "dev", "tok", false)
	if !errors.Is(err, core.ErrGitHubRefused) || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("%v", err)
	}
	b.answer(424, `{"error":"a/b has no commit abc"}`)
	_, err = s.Versions().Publish(context.Background(), "a/b", "abc", "dev", "tok", false)
	if err == nil || errors.Is(err, core.ErrGitHubRefused) || !strings.Contains(err.Error(), "no commit") {
		t.Fatalf("%v", err)
	}
}

func TestVersionsWhileDownFailsWithoutCalling(t *testing.T) {
	s, b := newSecretsBackend(t)
	s.set(false, "stopped")
	if _, err := s.Versions().List(context.Background(), "a/b", "tok"); err == nil {
		t.Fatal("no error while down")
	}
	if b.count() != 0 {
		t.Fatal("called while down")
	}
}

func TestForwardAddsOnlyTheHeadersHomelabAsksFor(t *testing.T) {
	s, b := newSecretsBackend(t)
	in := httptest.NewRequest("GET", "/api/releases", nil)
	in.Header.Set("X-Homelab-Repo", "evil/repo")
	in.Header.Set("X-Homelab-Github-Token", "from-the-app")
	extra := http.Header{}
	extra.Set("X-Homelab-Repo", "rupaai/demo")
	extra.Set("X-Homelab-Github-Token", "ghp_token")
	in = in.WithContext(core.WithServiceHeaders(in.Context(), extra))
	rec := httptest.NewRecorder()
	s.Forward(rec, in, core.ServiceCaller{Kind: "app", App: "demo", Env: "prod"})
	c := b.last(t, s)
	if got := c.req.Header.Get("X-Homelab-Repo"); got != "rupaai/demo" {
		t.Errorf("repo %q", got)
	}
	if got := c.req.Header.Get("X-Homelab-Github-Token"); got != "ghp_token" {
		t.Errorf("token %q", got)
	}

	// Without them the caller's own are gone.
	in = httptest.NewRequest("GET", "/api/releases", nil)
	in.Header.Set("X-Homelab-Repo", "evil/repo")
	s.Forward(httptest.NewRecorder(), in, core.ServiceCaller{Kind: "app", App: "demo", Env: "prod"})
	if got := b.last(t, s).req.Header.Get("X-Homelab-Repo"); got != "" {
		t.Errorf("the caller's header got through: %q", got)
	}
}
