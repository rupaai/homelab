package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/adapters/fake"
	"github.com/rupaai/homelab/internal/core"
)

// saveAsApp stands in for the app saving a secret through the secrets API.
func saveAsApp(r *rig, env, name, value string) {
	r.sec.Saved["movies/"+env+"/"+name] = fake.SavedSecret{Value: value, Version: 2, UpdatedAt: time.Unix(1_800_000_000, 0)}
}

func TestSavedSecretsAreListedByNameForEveryAdmin(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	saveAsApp(r, core.Prod, "google_token:u1", "tok-very-secret-1")
	envs, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil || len(envs) != 2 {
		t.Fatal(envs, err)
	}
	if len(envs[0].Saved) != 0 || len(envs[1].Saved) != 1 {
		t.Fatalf("saved = %+v / %+v", envs[0].Saved, envs[1].Saved)
	}
	if s := envs[1].Saved[0]; s.Name != "google_token:u1" || s.Version != 2 || s.UpdatedAt.Unix() != 1_800_000_000 {
		t.Fatalf("saved = %+v", s)
	}
	// They are not config secrets, and no value is in the list.
	for _, sv := range envs[1].Secrets {
		if sv.Name == "google_token:u1" {
			t.Fatal("a saved secret is listed as a config secret")
		}
	}
	if b := mustJSON(t, envs); strings.Contains(b, "tok-very-secret-1") {
		t.Fatalf("the list holds a value: %s", b)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOnlyPeopleWhoMaySeeSecretsCanRevealOrDeleteSavedSecrets(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	saveAsApp(r, core.Prod, "google_token:u1", "tok-very-secret-1")

	for who, by := range map[string]core.Viewer{
		"admin":      adminV,
		"member":     {PersonID: "member", Session: true},
		"no session": {PersonID: "owner", Login: "labiba@example.com"},
	} {
		if _, err := r.hub.RevealSavedSecret(ctx, "movies", core.Prod, "google_token:u1", by); !isUserErr(err) || err.Error() != msgSecretValues {
			t.Errorf("%s reveal = %v", who, err)
		}
		if err := r.hub.DeleteSavedSecret(ctx, "movies", core.Prod, "google_token:u1", by); !isUserErr(err) || err.Error() != msgSecretValues {
			t.Errorf("%s delete = %v", who, err)
		}
	}
	if got := r.eventDetails("secret.viewed"); len(got) != 0 {
		t.Fatalf("refused reveals were logged as views: %v", got)
	}
	if _, ok, _ := r.sec.GetSaved("movies", core.Prod, "google_token:u1"); !ok {
		t.Fatal("a refused delete removed it")
	}

	// The owner and a person with the secrets role can.
	for _, by := range []core.Viewer{owner, secretsV} {
		v, err := r.hub.RevealSavedSecret(ctx, "movies", core.Prod, "google_token:u1", by)
		if err != nil || v != "tok-very-secret-1" {
			t.Fatalf("reveal = %q, %v", v, err)
		}
	}
	views := r.eventDetails("secret.viewed")
	if len(views) != 2 || !strings.Contains(views[0], "google_token:u1") || !strings.Contains(views[0], "saved by the app") {
		t.Fatalf("views = %v", views)
	}
	for _, d := range views {
		if strings.Contains(d, "tok-very-secret-1") {
			t.Fatalf("a value is in the activity log: %q", d)
		}
	}
	// Unknown ones, other places.
	if _, err := r.hub.RevealSavedSecret(ctx, "movies", core.Dev, "google_token:u1", owner); !isUserErr(err) {
		t.Errorf("reveal in dev = %v", err)
	}
	if _, err := r.hub.RevealSavedSecret(ctx, "nope", core.Prod, "x", owner); !isUserErr(err) {
		t.Errorf("reveal in an unknown app = %v", err)
	}
	if _, err := r.hub.RevealSavedSecret(ctx, "movies", "staging", "x", owner); !isUserErr(err) {
		t.Errorf("reveal in an unknown env = %v", err)
	}
	// Delete is logged, and the app can save it again.
	if err := r.hub.DeleteSavedSecret(ctx, "movies", core.Prod, "google_token:u1", owner); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.sec.GetSaved("movies", core.Prod, "google_token:u1"); ok {
		t.Fatal("still there")
	}
	if got := r.eventDetails("secret.deleted"); len(got) != 1 || !strings.Contains(got[0], "google_token:u1") {
		t.Fatalf("deleted = %v", got)
	}
}

func TestSavedSecretsAreNeverGivenToADeploy(t *testing.T) {
	r := secretsRig(t, secretsDoc)
	saveAsApp(r, core.Prod, "TMDB_API_KEY", "saved-by-the-app")
	saveAsApp(r, core.Prod, "google_token:u1", "tok-very-secret-1")
	spec := secretSpec(t, r, secretsDoc, core.Prod)
	for k, v := range spec.Vars {
		if strings.Contains(v, "saved-by-the-app") || strings.Contains(v, "tok-very-secret-1") {
			t.Errorf("var %s holds a saved secret", k)
		}
	}
	for k, v := range spec.Secrets {
		if strings.Contains(v, "saved-by-the-app") || strings.Contains(v, "tok-very-secret-1") {
			t.Errorf("secret %s holds a saved secret", k)
		}
	}
}

func TestRemovingAnAppRemovesItsSavedSecrets(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	saveAsApp(r, core.Prod, "google_token:u1", "tok-very-secret-1")
	if err := r.hub.RemoveApp(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	if len(r.sec.Saved) != 0 {
		t.Fatalf("saved secrets are left: %v", r.sec.Saved)
	}
}

// ---- the secrets service is down ----

// downSecrets is a secrets store whose service is not running.
type downSecrets struct{ core.Secrets }

func (downSecrets) Missing(string, string, []string) ([]string, error) {
	return nil, core.ErrSecretsUnavailable
}
func (downSecrets) Inject(string, string, []string) (map[string]string, error) {
	return nil, core.ErrSecretsUnavailable
}
func (downSecrets) Set(string, string, string, string, string) error {
	return core.ErrSecretsUnavailable
}
func (downSecrets) List(string, string) ([]core.SecretInfo, error) {
	return nil, core.ErrSecretsUnavailable
}
func (downSecrets) ListSaved(string, string) ([]core.SecretInfo, error) {
	return nil, core.ErrSecretsUnavailable
}
func (downSecrets) Platform(string) (string, bool) { return "", false }

const plainDown = "Homelab's secrets service isn't running yet; try again in a minute."

func TestADeployFailsCleanlyWhileTheSecretsServiceIsDown(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner)
	a, err := r.store.App(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	rel := core.Release{Commit: "aaaaaaa1", Image: "ghcr.io/rupaai/movies", Tag: "sha-aaaaaaa1", Digest: "sha256:aaaaaaa1", Manifest: []byte(secretsDoc)}

	// Down for any of the three things a deploy asks of it: it fails with the plain
	// message, and no spec (nothing to start) comes back.
	r.hub.ad.Secrets = downSecrets{Secrets: r.sec}
	spec, err := r.hub.spec(ctx, a, core.Prod, rel)
	if err == nil || err.Error() != plainDown || !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("spec err = %v", err)
	}
	if spec.Release.Commit != "" || len(spec.Vars) != 0 || len(spec.Secrets) != 0 {
		t.Fatalf("a deploy spec was built without secrets: %+v", spec)
	}
	// Only the key step down.
	r.hub.ad.Secrets = downAfterInject{Secrets: r.sec}
	if _, err := r.hub.spec(ctx, a, core.Prod, rel); err == nil || err.Error() != plainDown {
		t.Fatalf("key step err = %v", err)
	}

	// And back up again, it works with the same secrets.
	r.hub.ad.Secrets = r.sec
	spec, err = r.hub.spec(ctx, a, core.Prod, rel)
	if err != nil || spec.Secrets["TMDB_API_KEY"] != "tmdb-value-123" {
		t.Fatalf("after: %v %v", spec.Secrets, err)
	}
}

// downAfterInject answers reads and then fails to save the app's key.
type downAfterInject struct{ core.Secrets }

func (d downAfterInject) Set(string, string, string, string, string) error {
	return core.ErrSecretsUnavailable
}
func (d downAfterInject) Inject(app, env string, names []string) (map[string]string, error) {
	if slices.Contains(names, AppTokenSecret) {
		return map[string]string{}, nil
	}
	return d.Secrets.Inject(app, env, names)
}
func (d downAfterInject) Missing(app, env string, names []string) ([]string, error) {
	return nil, nil
}

func TestANewBuildIsNotStartedWhileTheSecretsServiceIsDown(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	r.push("bbbbbbb2")
	r.hub.ad.Secrets = downSecrets{Secrets: r.sec}
	before := len(r.dep.Calls)
	r.hub.CheckBuilds(ctx)
	r.settle()
	for _, c := range r.dep.Calls[before:] {
		if strings.HasPrefix(c, "deploy ") {
			t.Fatalf("a deploy started without its secrets: %s", c)
		}
	}
	if got := r.running(core.Dev); got != "" {
		t.Fatalf("dev runs %q", got)
	}
	failed := r.eventDetails("deploy.failed")
	if len(failed) == 0 || !strings.Contains(failed[0], plainDown) {
		t.Fatalf("deploy.failed = %v", failed)
	}
	// Back up: the next try goes through.
	r.hub.ad.Secrets = r.sec
	r.advance(2 * time.Minute)
	r.hub.Reconcile(ctx)
	r.settle()
	if got := r.running(core.Dev); got != "bbbbbbb2" {
		t.Fatalf("dev runs %q after the service came back", got)
	}
}

func TestSecretScreensSayPlainlyWhenTheServiceIsDown(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	r.hub.ad.Secrets = downSecrets{Secrets: r.sec}
	if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner); !errors.Is(err, core.ErrSecretsUnavailable) || err.Error() != plainDown {
		t.Fatalf("set = %v", err)
	}
	if _, err := r.hub.AppSecrets(ctx, "movies"); !errors.Is(err, core.ErrSecretsUnavailable) {
		t.Fatalf("list = %v", err)
	}
	// Nothing was written anywhere else.
	if _, ok := r.sec.Values["movies/prod/TMDB_API_KEY"]; ok {
		t.Fatal("the value was kept somewhere while the service was down")
	}
}

// ---- the secrets API scopes of an app's key ----

func TestAppKeyScopesFollowSecretsAPIInTheManifest(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"", []string{ScopeUsersRead, ScopeSecretsRead}},
		{"secrets_api: read\n", []string{ScopeUsersRead, ScopeSecretsRead}},
		{"secrets_api: write\n", []string{ScopeUsersRead, ScopeSecretsRead, ScopeSecretsWrite}},
		{"secrets_api: none\n", []string{ScopeUsersRead}},
	} {
		doc := secretsDoc + tc.line
		r := secretsRig(t, doc)
		spec := secretSpec(t, r, doc, core.Prod)
		c, err := r.hub.Authenticate(ctx, spec.Vars[AppTokenSecret])
		if err != nil || !slices.Equal(c.Scopes, tc.want) {
			t.Errorf("%q: scopes %v, %v; want %v", tc.line, c.Scopes, err, tc.want)
		}
	}
}

func TestChangingSecretsAPIChangesTheScopesOfTheSameKey(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	tok := secretSpec(t, r, secretsDoc, core.Prod).Vars[AppTokenSecret]
	// A key made before the secrets API existed has only users:read.
	k, _ := r.store.APIKeyByHash(ctx, hashKey(tok))
	if err := r.store.SetAPIKeyScopes(ctx, k.ID, []string{ScopeUsersRead}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		line string
		want []string
	}{
		{"", []string{ScopeUsersRead, ScopeSecretsRead}},
		{"secrets_api: write\n", []string{ScopeUsersRead, ScopeSecretsRead, ScopeSecretsWrite}},
		{"secrets_api: none\n", []string{ScopeUsersRead}},
		{"secrets_api: write\n", []string{ScopeUsersRead, ScopeSecretsRead, ScopeSecretsWrite}},
	} {
		doc := secretsDoc + step.line
		got := secretSpec(t, r, doc, core.Prod).Vars[AppTokenSecret]
		if got != tok {
			t.Fatalf("the app's token changed when its scopes did")
		}
		c, err := r.hub.Authenticate(ctx, tok)
		if err != nil || !slices.Equal(c.Scopes, step.want) {
			t.Fatalf("%q: scopes %v, %v; want %v", step.line, c.Scopes, err, step.want)
		}
	}
	if got := r.eventDetails("key.scopes"); len(got) != 4 {
		t.Fatalf("scope changes logged: %v", got)
	}
}

func TestNoAdminKeyCanBeMadeWithSecretsScopes(t *testing.T) {
	ctx := context.Background()
	r, admin := keyRig(t)
	for _, scope := range []string{ScopeSecretsRead, ScopeSecretsWrite, "secrets:*", "secrets"} {
		if _, _, err := r.hub.CreateAdminKey(ctx, "svc", []string{scope}, nil, admin); err == nil {
			t.Errorf("an admin key was made with %s", scope)
		}
	}
}
