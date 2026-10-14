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
