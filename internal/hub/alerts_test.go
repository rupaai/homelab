package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// household adds an owner, a builder and a member of movies, each with one device.
func (r *rig) household() {
	ctx := context.Background()
	if _, err := r.store.App(ctx, "movies"); err != nil {
		if _, err := r.store.AddApp(ctx, structApp()); err != nil {
			r.t.Fatal(err)
		}
	}
	for _, p := range []core.Person{
		{ID: "o", Name: "Owner", Status: "signed_in", Roles: []string{"owner", "admin"}},
		{ID: "b", Name: "Builder", Status: "signed_in", Roles: []string{"builder"}, Grants: map[string]string{"movies": "build"}},
		{ID: "m", Name: "Member", Status: "signed_in", Roles: []string{"member"}, Grants: map[string]string{"movies": "use"}},
	} {
		if err := r.store.SavePerson(ctx, p); err != nil {
			r.t.Fatal(err)
		}
		sub := core.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/" + p.ID, P256dh: "k", Auth: "a"}
		if err := r.hub.SubscribePush(ctx, p.ID, "phone", sub); err != nil {
			r.t.Fatal(err)
		}
	}
}

// to lists who got a notification whose title ends with suffix.
func (r *rig) to(suffix string) []string {
	var out []string
	for _, p := range r.pusher.Sent {
		if strings.HasSuffix(p.Title, suffix) {
			out = append(out, p.Endpoint[strings.LastIndex(p.Endpoint, "/")+1:])
		}
	}
	return out
}

func TestProdDownAlertsBuildersAndAdminsOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.household()
	r.advance(11 * time.Minute) // past the watch, so nothing rolls back
	r.health.SetFailing("http://127.0.0.1:17011/healthz", true)
	r.hub.CheckHealth(ctx)
	r.hub.checkAlerts(ctx)
	if got := r.to(" is down"); len(got) != 0 {
		t.Fatalf("alerted before two minutes: %v", got)
	}
	r.advance(3 * time.Minute)
	r.hub.CheckHealth(ctx)
	r.hub.checkAlerts(ctx)
	r.hub.checkAlerts(ctx)
	if got := strings.Join(r.to(" is down"), ","); got != "o,b" && got != "b,o" {
		t.Fatalf("down alert went to %q, want the owner and the builder once each", got)
	}
	r.open() // a restart doesn't tell anyone again
	r.hub.CheckHealth(ctx)
	r.hub.checkAlerts(ctx)
	if got := r.to(" is down"); len(got) != 2 {
		t.Fatalf("after restart: %v", got)
	}
	r.health.SetFailing("http://127.0.0.1:17011/healthz", false)
	r.hub.CheckHealth(ctx)
	r.hub.checkAlerts(ctx)
	if got := r.to(" is back"); len(got) != 2 {
		t.Fatalf("back alert went to %v", got)
	}
}

func TestShortOutageDoesntAlert(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.household()
	r.advance(11 * time.Minute)
	r.health.SetFailing("http://127.0.0.1:17011/healthz", true)
	r.hub.CheckHealth(ctx)
	r.hub.checkAlerts(ctx)
	r.health.SetFailing("http://127.0.0.1:17011/healthz", false)
	r.hub.CheckHealth(ctx)
	r.hub.checkAlerts(ctx)
	if len(r.pusher.Sent) != 0 {
		t.Fatalf("sent %v", r.pusher.Titles())
	}
}

func TestAutoRollbackAlerts(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	r.household()
	r.health.SetFailWhen(func(u string) bool {
		return strings.Contains(u, ":17011/") && r.running(core.Prod) == "bbbbbbb2"
	})
	r.hub.CheckHealth(ctx)
	r.hub.CheckHealth(ctx)
	r.settle()
	if got := r.to(" was rolled back"); len(got) != 2 {
		t.Fatalf("rollback alert went to %v (all: %v)", got, r.pusher.Titles())
	}
}

func TestDiskAlertGoesToAdmins(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.household()
	r.res.SetDisk(90, 100)
	r.hub.checkAlerts(ctx)
	r.advance(10 * time.Minute)
	r.hub.checkAlerts(ctx)
	if got := r.to("almost full"); len(got) != 1 || got[0] != "o" {
		t.Fatalf("disk alert went to %v", got)
	}
}

func TestPushSubscriptions(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.household()
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://127.0.0.1/x", "https://evil.example/x", "https://fcm.googleapis.com:8443/x"} {
		if err := r.hub.SubscribePush(ctx, "o", "phone", core.PushSubscription{Endpoint: bad, P256dh: "k", Auth: "a"}); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	r.pusher.Gone["https://fcm.googleapis.com/fcm/send/o"] = true
	if _, err := r.hub.TestPush(ctx, "o"); err == nil {
		t.Fatal("a test to a gone device succeeded")
	}
	if d, _ := r.store.PushDevices(ctx, []string{"o"}); len(d) != 0 {
		t.Fatalf("gone device kept: %v", d)
	}
	if n, err := r.hub.TestPush(ctx, "m"); err != nil || n != 1 {
		t.Fatalf("test push = %d %v", n, err)
	}
	if err := r.hub.UnsubscribePush(ctx, "o", "https://fcm.googleapis.com/fcm/send/m"); err != nil {
		t.Fatal(err)
	}
	if d, _ := r.store.PushDevices(ctx, []string{"m"}); len(d) != 1 {
		t.Fatal("someone else's device was removed")
	}
}
