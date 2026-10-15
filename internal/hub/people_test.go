package hub

import (
	"context"
	"crypto/rand"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/state"
)

func TestInviteIsSingleUseAndHashed(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.AddApp(ctx, structApp()); err != nil {
		t.Fatal(err)
	}
	link, err := r.hub.InvitePerson(ctx, core.Person{Name: "Sam", Email: "sam@example.test", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(u.Path, "/invite/")
	if strings.Contains(link, "@") && strings.Contains(link, "example.test") {
		t.Fatal("invite URL leaked the email")
	}
	if _, err := r.hub.RedeemInvite(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.RedeemInvite(ctx, token); err == nil {
		t.Fatal("used invite was accepted again")
	}
	people, err := r.hub.People(ctx)
	if err != nil || len(people) != 1 || people[0].Status != "invited" {
		t.Fatalf("people=%+v err=%v", people, err)
	}
	if got := r.identity.Users[people[0].ID].Groups; len(got) != 2 || got[0] != "app:movies" || got[1] != "role:member" {
		t.Fatalf("identity groups=%v", got)
	}
}

func TestResendInviteRevokesOldLink(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.store.AddApp(ctx, structApp()); err != nil {
		t.Fatal(err)
	}
	first, err := r.hub.InvitePerson(ctx, core.Person{Name: "Sam", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}})
	if err != nil {
		t.Fatal(err)
	}
	resend, err := r.hub.ResendInvite(ctx, func() string {
		people, _ := r.hub.People(ctx)
		return people[0].ID
	}())
	if err != nil {
		t.Fatal(err)
	}
	oldToken := strings.TrimPrefix(mustURLPath(t, first), "/invite/")
	newToken := strings.TrimPrefix(mustURLPath(t, resend), "/invite/")
	if oldToken == newToken {
		t.Fatal("resend kept the same invite token")
	}
	if _, err := r.hub.RedeemInvite(ctx, oldToken); err == nil {
		t.Fatal("old invite still worked")
	}
	if _, err := r.hub.RedeemInvite(ctx, newToken); err != nil {
		t.Fatalf("new invite failed: %v", err)
	}
}

func mustURLPath(t *testing.T, value string) string {
	t.Helper()
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path
}

func TestRemovePersonEndsSessionsAndProtectsOwner(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.store.SavePerson(ctx, core.Person{ID: "member", Name: "Sam", Status: "signed_in", Roles: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.identity.Ensure(ctx, core.Person{ID: "member", Name: "Sam"}, nil); err != nil {
		t.Fatal(err)
	}
	sessionHash := make([]byte, 32)
	if _, err := rand.Read(sessionHash); err != nil {
		t.Fatal(err)
	}
	if err := r.store.PutSession(ctx, sessionHash, "member", time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	steps, err := r.hub.RemovePerson(ctx, "member")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 || !steps[0].Done || steps[1].Done || !steps[2].Done || !steps[3].Done {
		t.Fatalf("removal checklist=%+v", steps)
	}
	if !strings.Contains(steps[1].Detail, "doesn't provide a way") {
		t.Fatalf("missing Pocket ID limitation: %+v", steps[1])
	}
	if _, err := r.store.SessionPerson(ctx, sessionHash, time.Now(), time.Hour); err == nil {
		t.Fatal("removed person's session remained active")
	}
	if !r.identity.Users["member"].Disabled {
		t.Fatal("identity account remained enabled")
	}
	if err := r.store.SavePerson(ctx, core.Person{ID: "owner", Name: "Owner", Status: "signed_in", Roles: []string{"owner"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.RemovePerson(ctx, "owner"); err == nil {
		t.Fatal("owner was removable")
	}
}

func structApp() state.App {
	return state.App{ID: "movies", Name: "Movies", Repo: "a/b", Manifest: []byte("id: movies"), Promotion: "manual"}
}
