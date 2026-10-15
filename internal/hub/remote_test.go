package hub

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
)

func remoteRig(t *testing.T) *rig {
	r := newRig(t)
	r.remote = fake.NewRemote()
	r.open()
	if _, err := r.store.AddApp(context.Background(), structApp()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInviteSendsTailnetInviteAndRules(t *testing.T) {
	r := remoteRig(t)
	ctx := context.Background()
	inv, err := r.hub.InvitePerson(ctx, core.Person{Name: "Sam", Email: "Sam@Example.test", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.TailscaleURL == "" || inv.TailscaleError != "" || inv.URL == "" {
		t.Fatalf("invitation = %+v", inv)
	}
	p, err := r.store.Person(ctx, inv.ID)
	if err != nil || p.TailscaleLogin != "sam@example.test" || p.RemoteInviteURL != inv.TailscaleURL || p.RemoteInviteID == "" {
		t.Fatalf("person = %+v %v", p, err)
	}
	if got := r.remote.Groups("hl-app-movies"); !reflect.DeepEqual(got, []string{"sam@example.test"}) {
		t.Fatalf("movies group = %v", got)
	}
	if got := r.remote.Groups("hl-build-movies"); len(got) != 0 {
		t.Fatalf("a member was given dev: %v", got)
	}
	if got := r.remote.Groups("hl-admins"); len(got) != 0 {
		t.Fatalf("a member was made admin: %v", got)
	}
}

func TestInviteStillWorksWhenTailscaleFails(t *testing.T) {
	r := remoteRig(t)
	r.remote.Fail = true
	inv, err := r.hub.InvitePerson(context.Background(), core.Person{Name: "Sam", Email: "sam@example.test", Roles: []string{"member"}})
	if err != nil || inv.URL == "" || inv.TailscaleURL != "" || inv.TailscaleError == "" {
		t.Fatalf("invitation = %+v %v", inv, err)
	}
}

func TestAccessPolicyGivesEachPersonOnlyTheirAddresses(t *testing.T) {
	r := remoteRig(t)
	ctx := context.Background()
	for _, p := range []core.Person{
		{ID: "o", Name: "Owner", Email: "o@x.test", Status: "signed_in", Roles: []string{"owner", "admin"}},
		{ID: "b", Name: "Builder", Email: "b@x.test", Status: "signed_in", Roles: []string{"builder"}, Grants: map[string]string{"movies": "build"}},
		{ID: "m", Name: "Member", Email: "m@x.test", Status: "invited", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}},
		{ID: "n", Name: "Nobody", Email: "n@x.test", Status: "signed_in", Roles: []string{"member"}},
	} {
		if err := r.store.SavePerson(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := r.store.SetTailscaleLogin(ctx, p.ID, p.Email); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := r.hub.accessPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reach := func(login string) []string {
		var tags []string
		for _, g := range pol.Grants {
			for _, name := range g.Groups {
				for _, m := range pol.Groups[name] {
					if m == login {
						tags = append(tags, g.Tags...)
					}
				}
			}
		}
		sort.Strings(tags)
		out := tags[:0]
		for i, t := range tags {
			if i == 0 || t != tags[i-1] {
				out = append(out, t)
			}
		}
		return out
	}
	for login, want := range map[string][]string{
		"o@x.test": {"tag:hl-homelab", "tag:hl-movies", "tag:hl-movies-dev"},
		"b@x.test": {"tag:hl-homelab", "tag:hl-movies", "tag:hl-movies-dev"},
		"m@x.test": {"tag:hl-homelab", "tag:hl-movies"},
		"n@x.test": {"tag:hl-homelab"},
	} {
		if got := reach(login); !reflect.DeepEqual(got, want) {
			t.Errorf("%s reaches %v, want %v", login, got, want)
		}
	}
	if !reflect.DeepEqual(pol.TagOwners, []string{"tag:hl-homelab", "tag:hl-movies", "tag:hl-movies-dev"}) {
		t.Fatalf("tags = %v", pol.TagOwners)
	}
}

func TestTailnetLoginSignsInAndRemoveTakesThemOff(t *testing.T) {
	r := remoteRig(t)
	ctx := context.Background()
	inv, err := r.hub.InvitePerson(ctx, core.Person{Name: "Sam", Email: "sam@example.test", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []core.Viewer{
		{Login: "sam@example.test", Local: true},
		{Login: "sam@example.test", Tagged: true},
		{Login: "other@example.test"},
		{},
	} {
		if _, ok := r.hub.TailnetPerson(ctx, v); ok {
			t.Fatalf("%+v was taken for Sam", v)
		}
	}
	p, ok := r.hub.TailnetPerson(ctx, core.Viewer{Login: "SAM@example.test"})
	if !ok || p.ID != inv.ID || p.Status != "signed_in" || !Can(p, ActionApps, "movies") {
		t.Fatalf("tailnet person = %+v %v", p, ok)
	}

	// A device coming online marks them connected and retires the invite.
	seen := time.Now()
	r.remote.State = core.RemoteState{
		Users:   []core.RemoteUser{{ID: "u1", Login: "sam@example.test"}},
		Devices: []core.RemoteDevice{{ID: "d1", Name: "sams-iphone", Login: "sam@example.test", Online: true, LastSeen: &seen}, {ID: "d2", Name: "movies", Tags: []string{"tag:hl-movies"}}},
	}
	r.hub.reconcileRemote(ctx)
	p, _ = r.store.Person(ctx, inv.ID)
	if p.ConnectedAt == nil || p.RemoteInviteID != "" {
		t.Fatalf("after a device connected: %+v", p)
	}

	steps, err := r.hub.RemovePerson(ctx, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if !s.Done {
			t.Errorf("step %q not done: %s", s.Name, s.Detail)
		}
	}
	if len(r.remote.State.Devices) != 1 || len(r.remote.State.Users) != 0 {
		t.Fatalf("tailnet after remove = %+v", r.remote.State)
	}
	if got := r.remote.Groups("hl-everyone"); len(got) != 0 {
		t.Fatalf("still in the rules: %v", got)
	}
	if _, ok := r.hub.TailnetPerson(ctx, core.Viewer{Login: "sam@example.test"}); ok {
		t.Fatal("removed person still signed in over the tailnet")
	}
}

func TestRemoveShowsTailnetStepsThatFailed(t *testing.T) {
	r := remoteRig(t)
	ctx := context.Background()
	inv, err := r.hub.InvitePerson(ctx, core.Person{Name: "Sam", Email: "sam@example.test", Roles: []string{"member"}})
	if err != nil {
		t.Fatal(err)
	}
	r.remote.Fail = true
	steps, err := r.hub.RemovePerson(ctx, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, s := range steps {
		if !s.Done {
			failed++
		}
	}
	if failed != 4 { // invite, devices, user, rules
		t.Fatalf("steps = %+v", steps)
	}
	if _, err := r.store.Person(ctx, inv.ID); err == nil {
		t.Fatal("person kept although Homelab's own steps worked")
	}
}

func TestUpdatePersonChangesTailscaleLogin(t *testing.T) {
	r := remoteRig(t)
	ctx := context.Background()
	inv, err := r.hub.InvitePerson(ctx, core.Person{Name: "Sam", Email: "sam@example.test", Roles: []string{"member"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.hub.InvitePerson(ctx, core.Person{Name: "Alex", Email: "alex@example.test", Roles: []string{"member"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := r.store.Person(ctx, inv.ID)
	p.TailscaleLogin = "sam@github"
	if err := r.hub.UpdatePerson(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.store.Person(ctx, inv.ID); got.TailscaleLogin != "sam@github" {
		t.Fatalf("login = %q", got.TailscaleLogin)
	}
	if got := r.remote.Groups("hl-everyone"); !reflect.DeepEqual(got, []string{"alex@example.test", "sam@github"}) {
		t.Fatalf("everyone = %v", got)
	}
	p.TailscaleLogin = "alex@example.test"
	if err := r.hub.UpdatePerson(ctx, p); err == nil {
		t.Fatalf("two people got the same login (other=%s)", other.ID)
	}
}

func TestSetupOwnerLinksOwnerTailscaleLogin(t *testing.T) {
	r := remoteRig(t)
	ctx := context.Background()
	if _, err := r.hub.SetupOwner(ctx, core.Person{Name: "Owner", Email: "owner@example.test"}, "hash"); err != nil {
		t.Fatal(err)
	}
	p, err := r.store.PersonByTailscaleLogin(ctx, "owner@example.test")
	if err != nil || !contains(p.Roles, "owner") {
		t.Fatalf("owner by login = %+v %v", p, err)
	}
}
