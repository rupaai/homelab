package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

// fakeServices records what Homelab forwards to homelab-services.
type fakeServices struct {
	calls []core.ServiceCaller
	paths []string
	extra []http.Header // what Homelab asked to add to each call
}

func (f *fakeServices) Forward(w http.ResponseWriter, r *http.Request, c core.ServiceCaller) {
	f.calls = append(f.calls, c)
	f.paths = append(f.paths, r.Method+" "+r.URL.RequestURI())
	f.extra = append(f.extra, core.ServiceHeadersFrom(r.Context()))
	w.WriteHeader(http.StatusTeapot)
}

func (f *fakeServices) Problem() string { return "" }

func TestServicesGetTheRightCaller(t *testing.T) {
	r := newRig(t)
	r.people(t)
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	h := New(r.h, opts)
	door := Door(r.h, opts)
	adminTok, ak := r.adminKey(t, "users:read")
	appTok, pk := r.appKey(t, "movies", "dev")

	// A signed-in admin, from the companion.
	if rec := do(h, "GET", "/api/files/photos?x=1", "", &local); rec.Code != http.StatusTeapot {
		t.Fatalf("person: %d %s", rec.Code, rec.Body)
	}
	// An admin key on the companion's address.
	if rec := keyReq(h, "GET", "/api/db", adminTok, "", &local); rec.Code != http.StatusTeapot {
		t.Fatalf("admin key: %d %s", rec.Code, rec.Body)
	}
	// An app's key on the app door.
	if rec := keyReq(door, "POST", "/api/data/movies/likes", appTok, "", nil); rec.Code != http.StatusTeapot {
		t.Fatalf("app key: %d %s", rec.Code, rec.Body)
	}
	want := []core.ServiceCaller{
		{Kind: "person", Person: "owner"},
		{Kind: "admin", Person: "owner", Key: ak.ID, Scopes: []string{"users:read"}},
		{Kind: "app", Key: pk.ID, App: "movies", Env: "dev", Scopes: []string{"users:read"}},
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %+v", f.calls)
	}
	for i := range want {
		g := f.calls[i]
		if g.Kind != want[i].Kind || g.Person != want[i].Person || g.Key != want[i].Key || g.App != want[i].App || g.Env != want[i].Env || len(g.Scopes) != len(want[i].Scopes) {
			t.Errorf("call %d: %+v want %+v", i, g, want[i])
		}
	}
	if f.paths[0] != "GET /api/files/photos?x=1" || f.paths[2] != "POST /api/data/movies/likes" {
		t.Errorf("paths %v", f.paths)
	}
}

func TestServicesRefuseWhoTheRestRefuses(t *testing.T) {
	r := newRig(t)
	r.people(t)
	f := &fakeServices{}
	opts := Options{Version: "test", Static: testSite(), Store: r.store, Services: f}
	h := New(r.h, opts)
	member := &core.Viewer{Login: "mo@example.test", PersonID: "member"}
	appTok, _ := r.appKey(t, "movies", "dev")
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"nobody":                   do(h, "GET", "/api/files", "", nil),
		"member":                   do(h, "GET", "/api/files", "", member),
		"change without header":    do(h, "POST", "/api/files/x", "{}", &local),
		"app key on companion":     keyReq(h, "GET", "/api/files", appTok, "", &local),
		"bad key":                  keyReq(h, "GET", "/api/files", "hlk_nope", "", &local),
		"near miss is not a match": do(h, "GET", "/api/filesystem", "", &local),
	} {
		if rec.Code == http.StatusTeapot {
			t.Errorf("%s reached the services", name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("forwarded %+v", f.calls)
	}
}

func TestServicesNotSetUpIs503(t *testing.T) {
	r := newRig(t)
	if rec := do(r.handler(), "GET", "/api/files", "", &local); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestUploadsSkipTheJSONRulesButNotTheCSRFHeader(t *testing.T) {
	r := newRig(t)
	f := &fakeServices{}
	h := New(r.h, Options{Version: "test", Static: testSite(), Store: r.store, Services: f})
	photo := strings.Repeat("x", 200<<10) // bigger than the JSON body limit
	if rec := do(h, "POST", "/api/files/photos?name=a.jpg", photo, &local, "X-Homelab-Request", "1", "Content-Type", "image/jpeg"); rec.Code != http.StatusTeapot {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/files/photos?name=a.jpg", photo, &local, "Content-Type", "image/jpeg"); rec.Code == http.StatusTeapot {
		t.Fatal("upload without the CSRF header reached the services")
	}
	// The rest of the API still wants JSON.
	if rec := do(h, "POST", "/api/apps", "x", &local, "X-Homelab-Request", "1", "Content-Type", "image/jpeg"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON elsewhere: %d", rec.Code)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls %+v", f.calls)
	}
}

func TestStorageListsDisks(t *testing.T) {
	r := newRig(t)
	r.people(t)
	rec := do(r.handler(), "GET", "/api/storage", "", &local)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"homelab"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(r.handler(), "GET", "/api/storage", "", &core.Viewer{Login: "mo@example.test", PersonID: "member"}); rec.Code == http.StatusOK {
		t.Fatal("a non-admin saw the disks")
	}
}
