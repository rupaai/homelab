package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestStoreSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "homelab.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AddApp(ctx, App{ID: "movies", Name: "Movie Night", Repo: "rupaai/x", Manifest: []byte("id: movies"), Promotion: "auto-dev", AddedBy: "t"})
	if err != nil || a.Slot != 1 {
		t.Fatalf("AddApp: %v slot=%d", err, a.Slot)
	}
	if _, err := s.AddApp(ctx, App{ID: "movies", Repo: "r", Manifest: []byte("x"), Promotion: "manual"}); err == nil {
		t.Fatal("duplicate app accepted")
	}
	rel := core.Release{Commit: "abc1234567", Image: "img", Digest: "sha256:1", Manifest: []byte("id: movies")}
	if _, err := s.SetWant(ctx, Deployment{App: "movies", Env: core.Dev, Release: rel, By: "t", Kind: "deploy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWant(ctx, Deployment{App: "nope", Env: core.Dev, Release: rel, Kind: "deploy"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("SetWant on unknown app: %v", err)
	}
	if err := s.Log(ctx, Event{Actor: "t", App: "movies", Action: "app.added", Detail: "hi"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, err := s.Env(ctx, "movies", core.Dev)
	if err != nil || e.Want == nil || e.Want.Digest != "sha256:1" || string(e.Want.Manifest) != "id: movies" {
		t.Fatalf("Env after reopen: %+v %v", e, err)
	}
	p, _ := s.Env(ctx, "movies", core.Prod)
	if p.Want != nil {
		t.Fatal("prod should start empty")
	}
	h, _ := s.History(ctx, "movies", core.Dev, 10)
	if len(h) != 1 || h[0].Release.Commit != "abc1234567" {
		t.Fatalf("History: %+v", h)
	}
	ev, _ := s.Events(ctx, "", 0, 10)
	if len(ev) != 1 || ev[0].Action != "app.added" {
		t.Fatalf("Events: %+v", ev)
	}
}

func TestVersionsStayOnTheReleaseAndItsHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "homelab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AddApp(ctx, App{ID: "movies", Name: "Movies", Repo: "rupaai/x", Manifest: []byte("id: movies"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	rel := core.Release{Commit: "abc1234567", Digest: "sha256:1", Manifest: []byte("id: movies"), Version: "v0.4.0-dev.1", Summary: "1 new", Notes: "## New\n"}
	if _, err := s.SetWant(ctx, Deployment{App: "movies", Env: core.Dev, Release: rel, By: "t", Kind: "deploy"}); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Env(ctx, "movies", core.Dev)
	hist, _ := s.History(ctx, "movies", core.Dev, 1)
	if e.Want.Version != "v0.4.0-dev.1" || e.Want.Summary != "1 new" || hist[0].Release.Notes != "## New\n" {
		t.Fatalf("%+v %+v", e.Want, hist[0].Release)
	}

	// A version put on what runs now lands on the environment and its latest history entry.
	prod := core.Release{Commit: "def", Digest: "sha256:2", Manifest: []byte("id: movies")}
	s.SetWant(ctx, Deployment{App: "movies", Env: core.Prod, Release: prod, By: "t", Kind: "promote"})
	if err := s.SetVersion(ctx, "movies", core.Prod, "def", "v0.3.0", "2 new", "notes"); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Env(ctx, "movies", core.Prod)
	hist, _ = s.History(ctx, "movies", core.Prod, 5)
	if e.Want.Version != "v0.3.0" || e.Want.Digest != "sha256:2" || string(e.Want.Manifest) != "id: movies" || len(hist) != 1 || hist[0].Release.Version != "v0.3.0" {
		t.Fatalf("%+v %+v", e.Want, hist)
	}
	// Not for a commit prod doesn't run, or an environment with nothing.
	if err := s.SetVersion(ctx, "movies", core.Prod, "other", "v9.9.9", "", ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other commit: %v", err)
	}
	if err := s.SetVersion(ctx, "nope", core.Prod, "def", "v1.0.0", "", ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown app: %v", err)
	}
}
