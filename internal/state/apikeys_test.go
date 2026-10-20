package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

func keyStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	for _, id := range []string{"movies", "music"} {
		if _, err := s.AddApp(ctx, App{ID: id, Name: id, Repo: "a/" + id, Manifest: []byte("id: " + id), Promotion: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SavePerson(ctx, core.Person{ID: "admin", Name: "Admin", Email: "a@example.test", Status: "signed_in", Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func appKey(id, app, env string) APIKey {
	return APIKey{ID: id, Hash: []byte("hash-" + id), Prefix: "hlk_abcd", Name: app + " " + env, Kind: "app", App: app, Env: env, Scopes: []string{"users:read"}}
}

func adminKey(id, person string) APIKey {
	return APIKey{ID: id, Hash: []byte("hash-" + id), Prefix: "hlk_abcd", Name: id, Kind: "admin", Scopes: []string{"users:read"}, CreatedBy: person}
}

func TestPutAPIKeyRevokesEarlierAppKeyOfSameEnv(t *testing.T) {
	ctx := context.Background()
	s := keyStore(t)
	for _, k := range []APIKey{appKey("k1", "movies", "dev"), appKey("other-env", "movies", "prod"), appKey("other-app", "music", "dev")} {
		if err := s.PutAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutAPIKey(ctx, appKey("k2", "movies", "dev")); err != nil {
		t.Fatal(err)
	}
	old, err := s.APIKeyByID(ctx, "k1")
	if err != nil || old.Revoked == nil {
		t.Fatalf("earlier key: %+v %v", old, err)
	}
	for _, id := range []string{"k2", "other-env", "other-app"} {
		k, err := s.APIKeyByID(ctx, id)
		if err != nil || k.Revoked != nil {
			t.Fatalf("%s should still work: %+v %v", id, k, err)
		}
	}
	if k, err := s.APIKeyByHash(ctx, []byte("hash-k2")); err != nil || k.ID != "k2" || len(k.Scopes) != 1 || k.App != "movies" || k.Env != "dev" {
		t.Fatalf("by hash: %+v %v", k, err)
	}
}

func TestAPIKeysListsOnlyNotRevoked(t *testing.T) {
	ctx := context.Background()
	s := keyStore(t)
	for _, k := range []APIKey{adminKey("a1", "admin"), adminKey("a2", "admin"), appKey("p1", "movies", "dev")} {
		if err := s.PutAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RevokeAPIKey(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	// Revoking again is fine.
	if err := s.RevokeAPIKey(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	admins, err := s.APIKeys(ctx, "admin")
	if err != nil || len(admins) != 1 || admins[0].ID != "a2" || admins[0].CreatedBy != "admin" {
		t.Fatalf("admin keys: %+v %v", admins, err)
	}
	apps, err := s.APIKeys(ctx, "app")
	if err != nil || len(apps) != 1 || apps[0].ID != "p1" {
		t.Fatalf("app keys: %+v %v", apps, err)
	}
	if k, _ := s.APIKeyByID(ctx, "a1"); k.Revoked == nil {
		t.Fatal("a1 should be marked revoked")
	}
}

func TestDeletingAppRemovesItsKeys(t *testing.T) {
	ctx := context.Background()
	s := keyStore(t)
	for _, k := range []APIKey{appKey("m1", "movies", "dev"), appKey("m2", "movies", "prod"), appKey("u1", "music", "dev")} {
		if err := s.PutAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveApp(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m1", "m2"} {
		if _, err := s.APIKeyByID(ctx, id); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("%s survived the app: %v", id, err)
		}
	}
	if _, err := s.APIKeyByID(ctx, "u1"); err != nil {
		t.Fatalf("other app's key: %v", err)
	}
}

func TestDeletingPersonRemovesTheirAdminKeys(t *testing.T) {
	ctx := context.Background()
	s := keyStore(t)
	if err := s.SavePerson(ctx, core.Person{ID: "second", Name: "Second", Status: "signed_in", Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []APIKey{adminKey("a1", "admin"), adminKey("s1", "second"), appKey("p1", "movies", "dev")} {
		if err := s.PutAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeletePerson(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.APIKeyByID(ctx, "a1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("admin key survived its person: %v", err)
	}
	for _, id := range []string{"s1", "p1"} {
		if _, err := s.APIKeyByID(ctx, id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

func TestAPIKeyShapeRulesAndUnknowns(t *testing.T) {
	ctx := context.Background()
	s := keyStore(t)
	if err := s.RevokeAPIKey(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	if _, err := s.APIKeyByHash(ctx, []byte("nope")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("by hash unknown: %v", err)
	}
	if err := s.TouchAPIKey(ctx, "nope", time.Now()); err != nil {
		t.Fatalf("touch unknown: %v", err)
	}
	// An app key must name an existing app; an admin key must name a person.
	if err := s.PutAPIKey(ctx, appKey("x", "ghost", "dev")); err == nil {
		t.Fatal("key for an unknown app was accepted")
	}
	bad := adminKey("y", "")
	if err := s.PutAPIKey(ctx, bad); err == nil {
		t.Fatal("admin key with no person was accepted")
	}
	// Touch records last use.
	if err := s.PutAPIKey(ctx, adminKey("a1", "admin")); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	if err := s.TouchAPIKey(ctx, "a1", at); err != nil {
		t.Fatal(err)
	}
	if k, _ := s.APIKeyByID(ctx, "a1"); k.LastUsed == nil || !k.LastUsed.Equal(at) {
		t.Fatalf("last used: %+v", k.LastUsed)
	}
}
