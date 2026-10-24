package api

import (
	"net/http"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestLogsAreForwardedOnlyToWhoMayRead(t *testing.T) {
	r := newRig(t)
	r.people(t)
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	h := New(r.h, opts)
	door := Door(r.h, opts)
	adminTok, ak := r.adminKey(t, "logs:read")
	appTok, pk := r.appKeyScopes(t, "movies", "dev", "users:read", "logs:read")

	// Only a signed-in admin reaches the logs from the companion (the guard treats
	// /api/logs like the other service paths: manage rights).
	for name, v := range map[string]*core.Viewer{
		"member":    {Login: "mo@example.test", PersonID: "member"},
		"movie fan": {Login: "fan@example.test", PersonID: "movie-fan"},
	} {
		if rec := do(h, "GET", "/api/logs?app=movies", "", v); rec.Code == http.StatusTeapot {
			t.Errorf("%s reached the logs", name)
		}
	}
	if rec := do(h, "GET", "/api/logs", "", nil); rec.Code == http.StatusTeapot {
		t.Error("nobody reached the logs")
	}
	if len(f.calls) != 0 {
		t.Fatalf("forwarded %+v", f.calls)
	}

	if rec := do(h, "GET", "/api/logs?q=boom", "", &local); rec.Code != http.StatusTeapot {
		t.Fatalf("admin person: %d %s", rec.Code, rec.Body)
	}
	if rec := keyReq(h, "GET", "/api/logs", adminTok, "", &local); rec.Code != http.StatusTeapot {
		t.Fatalf("admin key: %d %s", rec.Code, rec.Body)
	}
	if rec := keyReq(door, "GET", "/api/logs", appTok, "", nil); rec.Code != http.StatusTeapot {
		t.Fatalf("app key: %d %s", rec.Code, rec.Body)
	}
	if rec := keyReq(h, "GET", "/api/logs", appTok, "", &local); rec.Code == http.StatusTeapot {
		t.Fatal("an app key worked on the companion's address")
	}
	if rec := keyReq(door, "GET", "/api/logs", adminTok, "", nil); rec.Code == http.StatusTeapot {
		t.Fatal("an admin key worked on the app door")
	}
	want := []core.ServiceCaller{
		{Kind: "person", Person: "owner"},
		{Kind: "admin", Person: "owner", Key: ak.ID, Scopes: []string{"logs:read"}},
		{Kind: "app", Key: pk.ID, App: "movies", Env: "dev", Scopes: []string{"users:read", "logs:read"}},
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %+v", f.calls)
	}
	for i := range want {
		g := f.calls[i]
		if g.Kind != want[i].Kind || g.Key != want[i].Key || g.App != want[i].App || g.Env != want[i].Env || len(g.Scopes) != len(want[i].Scopes) {
			t.Errorf("call %d: %+v want %+v", i, g, want[i])
		}
	}
}

func TestLogsIsAServicePath(t *testing.T) {
	for _, p := range []string{"/api/logs", "/api/logs/x"} {
		if !isServicePath(p) {
			t.Errorf("%s is not forwarded", p)
		}
	}
	if isServicePath("/api/logsx") {
		t.Error("/api/logsx matched")
	}
}
