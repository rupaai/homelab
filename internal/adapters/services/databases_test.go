package services

import (
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestDatabasesCalls(t *testing.T) {
	s, b := newSecretsBackend(t)
	b.answer(204, "")
	d := s.Databases()

	if err := d.Register("movies", "dev", core.DatabaseRegistration{Engine: "postgres", Port: 17015}); err != nil {
		t.Fatal(err)
	}
	c := b.last(t, s)
	if c.method != "PUT" || c.uri != "/internal/db/movies/dev" || c.body != `{"engine":"postgres","port":17015}` || c.caller.Kind != core.ServiceKindSystem {
		t.Fatalf("register: %+v", c)
	}
	if err := d.Register("movies", "prod", core.DatabaseRegistration{Engine: "sqlite", Path: "/h/apps/movies/prod/data/homelab-db/app.db"}); err != nil {
		t.Fatal(err)
	}
	if c := b.last(t, s); c.body != `{"engine":"sqlite","path":"/h/apps/movies/prod/data/homelab-db/app.db"}` {
		t.Fatalf("register sqlite: %+v", c)
	}
	if err := d.Forget("movies"); err != nil {
		t.Fatal(err)
	}
	if c := b.last(t, s); c.method != "DELETE" || c.uri != "/internal/db/movies" || c.caller.Kind != core.ServiceKindSystem {
		t.Fatalf("forget: %+v", c)
	}
	if err := d.Snapshot("movies", "dev", "/h/backups/x/homelab-db/app.db"); err != nil {
		t.Fatal(err)
	}
	if c := b.last(t, s); c.method != "POST" || c.uri != "/internal/db/movies/dev/snapshot" || c.body != `{"to":"/h/backups/x/homelab-db/app.db"}` {
		t.Fatalf("snapshot: %+v", c)
	}
	if err := d.Snapshot("movies", "dev", "relative.db"); err == nil {
		t.Error("a relative path should be refused")
	}
}

func TestDatabasesErrors(t *testing.T) {
	s, b := newSecretsBackend(t)
	d := s.Databases()
	b.answer(409, `{"error":"That database file doesn't exist yet."}`)
	if err := d.Snapshot("movies", "dev", "/x/app.db"); err == nil || err.Error() != "That database file doesn't exist yet" {
		t.Fatalf("409: %v", err)
	}
	b.answer(500, `{"error":"secret detail"}`)
	if err := d.Forget("movies"); err == nil || strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("500: %v", err)
	}
	s.set(false, "starting")
	if err := d.Register("movies", "dev", core.DatabaseRegistration{Engine: "postgres", Port: 1}); err == nil || !strings.Contains(err.Error(), "aren't running") {
		t.Fatalf("down: %v", err)
	}
}
