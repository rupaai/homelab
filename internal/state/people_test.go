package state

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

func TestPeopleMigrationAndSingleUseRecords(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AddApp(ctx, App{ID: "movies", Name: "Movies", Repo: "a/b", Manifest: []byte("id: movies"), Promotion: "manual"}); err != nil {
		t.Fatal(err)
	}
	p := core.Person{ID: "owner", Name: "Owner", Email: "owner@example.test", Status: "signed_in", Roles: []string{"owner", "admin"}, Grants: map[string]string{"movies": "build"}}
	if err = s.SavePerson(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Person(ctx, "owner")
	if err != nil || got.Grants["movies"] != "build" || len(got.Roles) != 2 {
		t.Fatalf("person=%+v err=%v", got, err)
	}
	expires := time.Now().Add(time.Hour)
	inviteHash := make([]byte, 32)
	if _, err = rand.Read(inviteHash); err != nil {
		t.Fatal(err)
	}
	if err = s.StoreInvite(ctx, inviteHash, p.ID, expires); err != nil {
		t.Fatal(err)
	}
	if id, err := s.ConsumeInvite(ctx, inviteHash); err != nil || id != p.ID {
		t.Fatalf("consume=%q err=%v", id, err)
	}
	if id, err := s.ConsumeInvite(ctx, inviteHash); err != nil || id != p.ID {
		t.Fatalf("invite could not be redeemed again: id=%q err=%v", id, err)
	}
	if err = s.RevokeInvites(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeInvite(ctx, inviteHash); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoked invite accepted: %v", err)
	}
	expiredHash := make([]byte, 32)
	if _, err = rand.Read(expiredHash); err != nil {
		t.Fatal(err)
	}
	if err = s.StoreInvite(ctx, expiredHash, p.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err = s.ConsumeInvite(ctx, expiredHash); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expired invite accepted: %v", err)
	}
	s.now = time.Now
	sessionHash := make([]byte, 32)
	if _, err = rand.Read(sessionHash); err != nil {
		t.Fatal(err)
	}
	if err = s.PutSession(ctx, sessionHash, p.ID, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SessionPerson(ctx, sessionHash, time.Now(), 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	codeHash := make([]byte, 32)
	if _, err = rand.Read(codeHash); err != nil {
		t.Fatal(err)
	}
	if err = s.PutAppCode(ctx, codeHash, p.ID, "movies", core.Prod, "/watch", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if code, err := s.ConsumeAppCode(ctx, codeHash, time.Now()); err != nil || code.PersonID != p.ID || code.ReturnPath != "/watch" {
		t.Fatalf("app code=%+v err=%v", code, err)
	}
	if _, err := s.ConsumeAppCode(ctx, codeHash, time.Now()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("app code replay accepted: %v", err)
	}
	appHash := make([]byte, 32)
	if _, err = rand.Read(appHash); err != nil {
		t.Fatal(err)
	}
	if err = s.PutAppSession(ctx, appHash, p.ID, "movies", core.Prod, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if person, err := s.AppSessionPerson(ctx, appHash, "movies", core.Prod, time.Now(), 8*time.Hour); err != nil || person.ID != p.ID {
		t.Fatalf("app session=%+v err=%v", person, err)
	}
	if err = s.DeleteAppSessions(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppSessionPerson(ctx, appHash, "movies", core.Prod, time.Now(), 8*time.Hour); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted app session remained: %v", err)
	}
	if err = s.SetRecovery(ctx, p.ID, "bcrypt hash"); err != nil {
		t.Fatal(err)
	}
	if hash, used, err := s.RecoveryHash(ctx, p.ID); err != nil || hash != "bcrypt hash" || used {
		t.Fatalf("recovery=%q,%v,%v", hash, used, err)
	}
}

func TestOwnerRoleUnique(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.SavePerson(ctx, core.Person{ID: "one", Name: "One", Status: "signed_in", Roles: []string{"owner"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SavePerson(ctx, core.Person{ID: "two", Name: "Two", Status: "signed_in", Roles: []string{"owner"}}); err == nil {
		t.Fatal("second owner was accepted")
	}
}
