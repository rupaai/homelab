package hub

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
)

// keyRig is a rig with an admin, a member, and the movies app added.
func keyRig(t *testing.T) (*rig, core.Viewer) {
	t.Helper()
	ctx := context.Background()
	r := newRig(t)
	for _, p := range []core.Person{
		{ID: "admin", Name: "Ada", Email: "ada@example.test", Status: "signed_in", Roles: []string{"admin"}},
		{ID: "member", Name: "Mo", Email: "mo@example.test", Status: "signed_in", Roles: []string{"member"}},
	} {
		if err := r.store.SavePerson(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(manifestDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	return r, core.Viewer{Login: "ada@example.test", PersonID: "admin"}
}

func isUserErr(err error) bool {
	var ue *UserError
	return errors.As(err, &ue)
}

func TestCreateAdminKeyRules(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	future := r.now().Add(time.Hour)
	past := r.now().Add(-time.Hour)
	cases := []struct {
		name    string
		by      core.Viewer
		keyName string
		scopes  []string
		expires *time.Time
	}{
		{"member", core.Viewer{PersonID: "member"}, "k", []string{"users:read"}, nil},
		{"unknown person", core.Viewer{PersonID: "ghost"}, "k", []string{"users:read"}, nil},
		{"no name", admin, "  ", []string{"users:read"}, nil},
		{"long name", admin, strings.Repeat("x", 61), []string{"users:read"}, nil},
		{"control char in name", admin, "a\nb", []string{"users:read"}, nil},
		{"no scopes", admin, "k", nil, nil},
		{"unknown scope", admin, "k", []string{"root:write"}, nil},
		{"later scope", admin, "k", []string{"files:photos:read"}, nil},
		{"users write", admin, "k", []string{"users:write"}, nil},
		{"past expiry", admin, "k", []string{"users:read"}, &past},
	}
	for _, c := range cases {
		tok, k, err := r.hub.CreateAdminKey(ctx, c.keyName, c.scopes, c.expires, c.by)
		if err == nil || !isUserErr(err) || tok != "" || k.ID != "" {
			t.Errorf("%s: token=%q key=%+v err=%v", c.name, tok, k, err)
		}
	}
	if keys, _ := r.store.APIKeys(ctx, KeyKindAdmin); len(keys) != 0 {
		t.Fatalf("refused requests left keys behind: %+v", keys)
	}
	// Fine: users:read with a future expiry.
	tok, k, err := r.hub.CreateAdminKey(ctx, " photo service ", []string{"users:read"}, &future, admin)
	if err != nil || tok == "" || k.Name != "photo service" || k.Expires == nil {
		t.Fatalf("create: %q %+v %v", tok, k, err)
	}
}

func TestCreateAdminKeyDedupesSortsAndAuthenticates(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	tok, k, err := r.hub.CreateAdminKey(ctx, "svc", []string{"users:read", " users:read "}, nil, admin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"users:read"}
	if !reflect.DeepEqual(k.Scopes, want) {
		t.Fatalf("scopes=%v want %v", k.Scopes, want)
	}
	if !strings.HasPrefix(tok, "hlk_") || k.Prefix != tok[:8] || strings.Contains(k.Prefix, tok[8:]) {
		t.Fatalf("token=%q prefix=%q", tok, k.Prefix)
	}
	stored, err := r.store.APIKeyByID(ctx, k.ID)
	if err != nil || !reflect.DeepEqual(stored.Scopes, want) || string(stored.Hash) == tok || stored.CreatedBy != "admin" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	c, err := r.hub.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KeyKindAdmin || c.KeyID != k.ID || c.PersonID != "admin" || c.KeyName != "svc" || !reflect.DeepEqual(c.Scopes, want) {
		t.Fatalf("caller=%+v", c)
	}
	if got := c.Label(); got != "key svc" {
		t.Fatalf("label=%q", got)
	}
	ev, _ := r.store.Events(ctx, "", 0, 10)
	var logged bool
	for _, e := range ev {
		if e.Action == "key.created" && strings.Contains(e.Detail, "svc") {
			logged = true
		}
		if strings.Contains(e.Detail, tok) {
			t.Fatal("the key itself reached the activity log")
		}
	}
	if !logged {
		t.Fatalf("key.created not logged: %+v", ev)
	}
}

func TestAuthenticateRefusals(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	tok, k, err := r.hub.CreateAdminKey(ctx, "svc", []string{"users:read"}, nil, admin)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"empty":        "",
		"wrong prefix": "hlx_" + tok[4:],
		"no prefix":    tok[4:],
		"too short":    tok[:len(tok)-2],
		"too long":     tok + "AA",
		"not base64":   "hlk_" + strings.Repeat("!", 43),
		"unknown":      "hlk_" + strings.Repeat("A", 43),
	} {
		if _, err := r.hub.Authenticate(ctx, bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.hub.Authenticate(ctx, tok); err != nil {
		t.Fatal(err)
	}
	// Revoked.
	if err := r.hub.RevokeKey(ctx, k.ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Authenticate(ctx, tok); !errors.Is(err, ErrBadKey) {
		t.Fatalf("revoked key worked: %v", err)
	}
}

func TestAuthenticateExpiry(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	r.advance(0) // fix the clock so it can be moved
	exp := r.now().Add(time.Hour)
	tok, _, err := r.hub.CreateAdminKey(ctx, "svc", []string{"users:read"}, &exp, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Authenticate(ctx, tok); err != nil {
		t.Fatal(err)
	}
	r.advance(time.Hour + time.Second)
	if _, err := r.hub.Authenticate(ctx, tok); !errors.Is(err, ErrBadKey) {
		t.Fatalf("expired key worked: %v", err)
	}
}

func TestAdminKeyStopsWhenCreatorLosesAdminOrIsDeleted(t *testing.T) {
	ctx := context.Background()
	r, _ := keyRig(t)
	for _, p := range []core.Person{
		{ID: "demoted", Name: "D", Status: "signed_in", Roles: []string{"admin"}},
		{ID: "gone", Name: "G", Status: "signed_in", Roles: []string{"admin"}},
		{ID: "owner2", Name: "O", Status: "signed_in", Roles: []string{"owner"}},
	} {
		if err := r.store.SavePerson(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(id string) string {
		tok, _, err := r.hub.CreateAdminKey(ctx, id, []string{"users:read"}, nil, core.Viewer{PersonID: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.hub.Authenticate(ctx, tok); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return tok
	}
	demoted, gone, owner2 := mk("demoted"), mk("gone"), mk("owner2")

	if err := r.store.SavePerson(ctx, core.Person{ID: "demoted", Name: "D", Status: "signed_in", Roles: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Authenticate(ctx, demoted); !errors.Is(err, ErrBadKey) {
		t.Fatalf("demoted creator's key: %v", err)
	}
	if err := r.store.DeletePerson(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Authenticate(ctx, gone); !errors.Is(err, ErrBadKey) {
		t.Fatalf("deleted creator's key: %v", err)
	}
	if _, err := r.hub.Authenticate(ctx, owner2); err != nil {
		t.Fatalf("an owner counts as admin: %v", err)
	}
}

func TestCallerAllows(t *testing.T) {
	c := Caller{Scopes: []string{ScopeUsersRead}}
	if !c.Allows("users:read") || c.Allows("users:write") || c.Allows("") {
		t.Error("Allows doesn't match exactly")
	}
	if (Caller{}).Allows("users:read") {
		t.Error("a key with no scopes allowed users:read")
	}
	if got := (Caller{Kind: KeyKindApp, App: "movies", Env: "dev"}).Label(); got != "app key movies/dev" {
		t.Errorf("label=%q", got)
	}
}

func TestRevokeAppKeyGivesANewOneOnNextDeploy(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	if err := r.hub.RevokeKey(ctx, "nope", admin); !isUserErr(err) {
		t.Fatalf("unknown id: %v", err)
	}
	old := movieSpec(t, r, core.Dev).Vars["HOMELAB_TOKEN"]
	keys, _ := r.store.APIKeys(ctx, KeyKindApp)
	if len(keys) != 1 {
		t.Fatalf("app keys=%+v", keys)
	}
	if err := r.hub.RevokeKey(ctx, keys[0].ID, admin); err != nil {
		t.Fatalf("revoke app key: %v", err)
	}
	if err := r.hub.RevokeKey(ctx, keys[0].ID, admin); !isUserErr(err) {
		t.Fatalf("revoked twice: %v", err)
	}
	if _, err := r.hub.Authenticate(ctx, old); !errors.Is(err, ErrBadKey) {
		t.Fatalf("revoked app key still works: %v", err)
	}
	fresh := movieSpec(t, r, core.Dev).Vars["HOMELAB_TOKEN"]
	if fresh == "" || fresh == old {
		t.Fatalf("next deploy token=%q", fresh)
	}
	if c, err := r.hub.Authenticate(ctx, fresh); err != nil || c.App != "movies" {
		t.Fatalf("new key: %+v %v", c, err)
	}
}

func TestAPIKeysListNeverHoldsTheKey(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	tok, k, _ := r.hub.CreateAdminKey(ctx, "svc", []string{"users:read"}, nil, admin)
	r.hub.appKey(ctx, "movies", core.Dev, appKeyScopes(&manifest.Manifest{}))
	got, err := r.hub.APIKeys(ctx)
	if err != nil || len(got[KeyKindAdmin]) != 1 || len(got[KeyKindApp]) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if v := got[KeyKindAdmin][0]; v.ID != k.ID || v.CreatedByName != "Ada" {
		t.Fatalf("view=%+v", v)
	}
	if !reflect.DeepEqual(got[KeyKindApp][0].Scopes, appKeyScopes(&manifest.Manifest{})) {
		t.Fatalf("app scopes=%v", got[KeyKindApp][0].Scopes)
	}
	_ = tok
}

// ---- the key an app gets ----

func movieSpec(t *testing.T, r *rig, env string) core.EnvSpec {
	t.Helper()
	ctx := context.Background()
	a, err := r.store.App(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	rel := core.Release{Commit: "aaaaaaa1", Image: "ghcr.io/rupaai/movies", Tag: "sha-aaaaaaa1", Digest: "sha256:aaaaaaa1", Manifest: []byte(manifestDoc)}
	spec, err := r.hub.spec(ctx, a, env, rel)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestAppKeyIsInjectedAndStable(t *testing.T) {
	ctx := context.Background()
	r, _ := keyRig(t)
	s1 := movieSpec(t, r, core.Dev)
	tok := s1.Vars["HOMELAB_TOKEN"]
	if !strings.HasPrefix(tok, "hlk_") {
		t.Fatalf("token=%q", tok)
	}
	if _, ok := s1.Vars["HOMELAB_API_URL"]; ok {
		t.Fatal("HOMELAB_API_URL set without Config.AppAPIURL")
	}
	if s1.Vars["HOMELAB_APP"] != "movies" || s1.Vars["HOMELAB_ENV"] != core.Dev {
		t.Fatalf("vars=%v", s1.Vars)
	}
	if s2 := movieSpec(t, r, core.Dev); s2.Vars["HOMELAB_TOKEN"] != tok {
		t.Fatal("the next deploy got a different token")
	}
	// Prod has its own key.
	if p := movieSpec(t, r, core.Prod); p.Vars["HOMELAB_TOKEN"] == tok || p.Vars["HOMELAB_TOKEN"] == "" {
		t.Fatal("prod shares dev's token")
	}
	c, err := r.hub.Authenticate(ctx, tok)
	if err != nil || c.Kind != KeyKindApp || c.App != "movies" || c.Env != core.Dev || !reflect.DeepEqual(c.Scopes, []string{ScopeUsersRead, ScopeSecretsRead}) {
		t.Fatalf("caller=%+v err=%v", c, err)
	}
	if got := c.Label(); got != "app key movies/dev" {
		t.Fatalf("label=%q", got)
	}

	// Revoke the stored token's key: the next deploy gets a new one, the old one is dead.
	k, err := r.store.APIKeyByHash(ctx, hashKey(tok))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.RevokeAPIKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	s3 := movieSpec(t, r, core.Dev)
	if s3.Vars["HOMELAB_TOKEN"] == tok || s3.Vars["HOMELAB_TOKEN"] == "" {
		t.Fatal("revoked key was handed out again")
	}
	if _, err := r.hub.Authenticate(ctx, tok); !errors.Is(err, ErrBadKey) {
		t.Fatalf("revoked app key: %v", err)
	}
	if _, err := r.hub.Authenticate(ctx, s3.Vars["HOMELAB_TOKEN"]); err != nil {
		t.Fatal(err)
	}
	if keys, _ := r.store.APIKeys(ctx, KeyKindApp); len(keys) != 2 {
		t.Fatalf("active app keys=%d, want one per env", len(keys))
	}
}

func TestAppKeyThroughRealDeploy(t *testing.T) {
	ctx := context.Background()
	r, _ := keyRig(t)
	r.push("aaaaaaa1")
	r.hub.CheckBuilds(ctx)
	r.settle()
	tok := r.dep.Specs["movies/dev"].Vars["HOMELAB_TOKEN"]
	if !strings.HasPrefix(tok, "hlk_") {
		t.Fatalf("deployed spec has no token: %v", r.dep.Specs["movies/dev"].Vars)
	}
	if _, err := r.hub.Authenticate(ctx, tok); err != nil {
		t.Fatal(err)
	}
}

func TestAppAPIURLOnlyWhenConfigured(t *testing.T) {
	r, _ := keyRig(t)
	if _, ok := movieSpec(t, r, core.Dev).Vars["HOMELAB_API_URL"]; ok {
		t.Fatal("URL set by default")
	}
	r.hub.cfg.AppAPIURL = "http://host.docker.internal:7085"
	if got := movieSpec(t, r, core.Dev).Vars["HOMELAB_API_URL"]; got != "http://host.docker.internal:7085" {
		t.Fatalf("url=%q", got)
	}
}

func TestReservedSecretNames(t *testing.T) {
	ctx := context.Background()
	r, _ := keyRig(t)
	r.addPeople()
	movieSpec(t, r, core.Dev) // makes HOMELAB_TOKEN
	if _, ok := r.sec.Values["movies/dev/HOMELAB_TOKEN"]; !ok {
		t.Fatal("token not kept in the secret store")
	}
	for _, name := range []string{"HOMELAB_TOKEN", "HOMELAB_ANYTHING", "HOMELAB_"} {
		if err := r.hub.SetSecret(ctx, "movies", core.Dev, name, "x", owner); !isUserErr(err) {
			t.Errorf("SetSecret %s: %v", name, err)
		}
		if err := r.hub.DeleteSecret(ctx, "movies", core.Dev, name, owner); !isUserErr(err) {
			t.Errorf("DeleteSecret %s: %v", name, err)
		}
	}
	if _, ok := r.sec.Values["movies/dev/HOMELAB_TOKEN"]; !ok {
		t.Fatal("token was deleted")
	}
	if err := r.hub.SetSecret(ctx, "movies", core.Dev, "OTHER", "x", owner); err != nil {
		t.Fatal(err)
	}
	all, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, es := range all {
		for _, s := range es.Secrets {
			names = append(names, es.Env+"/"+s.Name)
			if strings.HasPrefix(s.Name, "HOMELAB_") {
				t.Errorf("AppSecrets lists %s", s.Name)
			}
		}
	}
	if !slices.Contains(names, "dev/OTHER") {
		t.Fatalf("other secrets should still show: %v", names)
	}
}

// ---- Users API ----

func TestUsersListsOnlyPeopleWithAccess(t *testing.T) {
	ctx := context.Background()
	r, _ := keyRig(t)
	r.src.Push("rupaai/music", "main", "bbbbbbb1", "x", []byte(strings.Replace(manifestDoc, "id: movies", "id: music", 1)))
	if _, err := r.hub.AddApp(ctx, "rupaai/music", "", owner); err != nil {
		t.Fatal(err)
	}
	for _, p := range []core.Person{
		{ID: "boss", Name: "Boss", Status: "signed_in", Roles: []string{"owner"}},
		{ID: "builder", Name: "Bu", Status: "signed_in", Roles: []string{"member"}, Grants: map[string]string{"movies": "build"}},
		{ID: "user", Name: "Us", Status: "invited", Roles: []string{"member"}, Grants: map[string]string{"movies": "use", "music": "use"}},
		{ID: "nobody", Name: "No", Status: "signed_in"},
	} {
		if err := r.store.SavePerson(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	access := func(app string) map[string]string {
		list, err := r.hub.Users(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, u := range list {
			m[u.ID] = u.Access
			if u.Roles == nil {
				t.Errorf("%s has nil roles (JSON null)", u.ID)
			}
		}
		return m
	}
	if got, want := access("movies"), map[string]string{"admin": "admin", "boss": "admin", "builder": "build", "user": "use"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("movies: %v want %v", got, want)
	}
	if got, want := access("music"), map[string]string{"admin": "admin", "boss": "admin", "user": "use"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("music: %v want %v", got, want)
	}
	everyone := access("")
	if len(everyone) != 6 { // admin, member, boss, builder, user, nobody
		t.Fatalf("everyone: %v", everyone)
	}
	for id, a := range everyone {
		if a != "" {
			t.Errorf("%s: access %q shown without an app", id, a)
		}
	}
	if _, err := r.hub.Users(ctx, "ghost"); !isUserErr(err) {
		t.Fatalf("unknown app: %v", err)
	}
	u, err := r.hub.UserByID(ctx, "builder", "movies")
	if err != nil || u.Access != "build" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := r.hub.UserByID(ctx, "ghost", ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown person: %v", err)
	}
}

// ---- activity log ----

func TestLogKeyUseThrottlesReads(t *testing.T) {
	ctx := context.Background()
	r, _ := keyRig(t)
	r.advance(0)
	c := Caller{KeyID: "k1", KeyName: "svc", Kind: KeyKindAdmin}
	count := func() int {
		ev, err := r.store.Events(ctx, "", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range ev {
			if e.Action == "key.used" {
				n++
			}
		}
		return n
	}
	r.hub.LogKeyUse(ctx, c, "Listed everyone", false)
	r.hub.LogKeyUse(ctx, c, "Listed everyone", false)
	r.advance(9 * time.Minute)
	r.hub.LogKeyUse(ctx, c, "Listed everyone", false)
	if n := count(); n != 1 {
		t.Fatalf("repeated reads logged %d times, want 1", n)
	}
	// A different read, or another key's, is its own line.
	r.hub.LogKeyUse(ctx, c, "Read people counts", false)
	r.hub.LogKeyUse(ctx, Caller{KeyID: "k2", KeyName: "other"}, "Listed everyone", false)
	if n := count(); n != 3 {
		t.Fatalf("logged %d, want 3", n)
	}
	// A change is always logged, however often.
	r.hub.LogKeyUse(ctx, c, "Listed everyone", true)
	r.hub.LogKeyUse(ctx, c, "Listed everyone", true)
	if n := count(); n != 5 {
		t.Fatalf("changes: logged %d, want 5", n)
	}
	// Ten minutes after the first read, it's logged again.
	r.advance(2 * time.Minute)
	r.hub.LogKeyUse(ctx, c, "Listed everyone", false)
	if n := count(); n != 6 {
		t.Fatalf("after ten minutes: logged %d, want 6", n)
	}
	ev, _ := r.store.Events(ctx, "", 0, 1)
	if len(ev) != 1 || ev[0].Actor != "key svc" {
		t.Fatalf("newest event=%+v", ev)
	}
}

func TestAdminKeysMayHaveFileScopes(t *testing.T) {
	r, admin := keyRig(t)
	_, k, err := r.hub.CreateAdminKey(context.Background(), "photos", []string{"files:write", "files:read", "users:read"}, nil, admin)
	if err != nil || !reflect.DeepEqual(k.Scopes, []string{"files:read", "files:write", "users:read"}) {
		t.Fatalf("%+v %v", k.Scopes, err)
	}
	for _, bad := range []string{"files:photos:read", "files:delete", "files"} {
		if _, _, err := r.hub.CreateAdminKey(context.Background(), "x", []string{bad}, nil, admin); !isUserErr(err) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
