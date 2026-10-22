package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

const secretsDoc = `
id: movies
name: Movie Night
image: ghcr.io/rupaai/movies
tag: "sha-{commit}"
port: 8000
health: /healthz
data: [{name: db, path: /data}]
secrets: [TMDB_API_KEY]
generate_secrets: [ADMIN_TOKEN]
`

var (
	adminV   = core.Viewer{Login: "ada@example.test", PersonID: "admin", Session: true}
	secretsV = core.Viewer{Login: "secrets@example.test", PersonID: "secrets", Session: true}
)

// secretsRig is a rig with an owner, an admin who is not the owner, a person with the
// "secrets" role, a member, and the movies app (which has a generated secret) added.
func secretsRig(t *testing.T, doc string) *rig {
	t.Helper()
	ctx := context.Background()
	r := newRig(t)
	r.addPeople("secrets", "admin", "member")
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(doc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	return r
}

func secretSpec(t *testing.T, r *rig, doc, env string) core.EnvSpec {
	t.Helper()
	ctx := context.Background()
	a, err := r.store.App(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	rel := core.Release{Commit: "aaaaaaa1", Image: "ghcr.io/rupaai/movies", Tag: "sha-aaaaaaa1", Digest: "sha256:aaaaaaa1", Manifest: []byte(doc)}
	spec, err := r.hub.spec(ctx, a, env, rel)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestOwnerCanRevealAndItIsLogged(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner); err != nil {
		t.Fatal(err)
	}
	got, err := r.hub.RevealSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", owner)
	if err != nil || got != "tmdb-value-123" {
		t.Fatalf("reveal = %q, %v", got, err)
	}
	// A generated secret is revealed the same way, once it exists.
	secretSpec(t, r, secretsDoc, core.Prod)
	if v, err := r.hub.RevealSecret(ctx, "movies", core.Prod, "ADMIN_TOKEN", owner); err != nil || v == "" {
		t.Fatalf("reveal generated = %q, %v", v, err)
	}
	// Not set, reserved and unknown places are refused with a plain message.
	if _, err := r.hub.RevealSecret(ctx, "movies", core.Dev, "TMDB_API_KEY", owner); !isUserErr(err) {
		t.Errorf("reveal of an unset secret: %v", err)
	}
	if _, err := r.hub.RevealSecret(ctx, "movies", core.Prod, "HOMELAB_TOKEN", owner); !isUserErr(err) {
		t.Errorf("reveal of a reserved name: %v", err)
	}
	if _, err := r.hub.RevealSecret(ctx, "movies", "staging", "TMDB_API_KEY", owner); !isUserErr(err) {
		t.Errorf("reveal in an unknown env: %v", err)
	}
	events, err := r.hub.Events(ctx, "movies", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	viewed := 0
	for _, e := range events {
		if strings.Contains(e.Detail, "tmdb-value-123") {
			t.Fatalf("an activity event holds a value: %+v", e)
		}
		if e.Action == "secret.viewed" {
			viewed++
			if e.Actor != "labiba@example.com" || e.Env != core.Prod || e.App != "movies" {
				t.Errorf("event = %+v", e)
			}
		}
	}
	if viewed != 2 {
		t.Fatalf("%d view events, want 2", viewed)
	}
	if ev := r.eventDetails("secret.viewed"); !strings.Contains(ev[0], "TMDB_API_KEY") && !strings.Contains(ev[1], "TMDB_API_KEY") {
		t.Fatalf("view events = %v", ev)
	}
}

func (r *rig) eventDetails(action string) []string {
	evs, _ := r.hub.Events(context.Background(), "", 0, 200)
	var out []string
	for _, e := range evs {
		if e.Action == action {
			out = append(out, e.Detail)
		}
	}
	return out
}

func TestAdminWhoIsNotOwnerCantUseSecretValues(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner)
	_ = r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_original", owner)
	secretSpec(t, r, secretsDoc, core.Prod)
	before, _ := r.sec.Inject("movies", core.Prod, []string{"ADMIN_TOKEN"})

	for who, by := range map[string]core.Viewer{
		"admin":      adminV,
		"member":     {PersonID: "member", Session: true},
		"no person":  {PersonID: "ghost", Session: true},
		"no session": {PersonID: "owner", Login: "labiba@example.com"}, // as an API key would arrive
	} {
		calls := map[string]func() error{
			"reveal": func() error { _, err := r.hub.RevealSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", by); return err },
			"set":    func() error { return r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "changed", by) },
			"regenerate": func() error {
				return r.hub.RegenerateSecret(ctx, "movies", core.Prod, "ADMIN_TOKEN", by)
			},
			"delete":   func() error { return r.hub.DeleteSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", by) },
			"versions": func() error { _, err := r.hub.SecretVersions(ctx, "movies", core.Prod, "TMDB_API_KEY", by); return err },
			"restore":  func() error { return r.hub.RestoreSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", 1, by) },
			"platform reveal": func() error {
				_, err := r.hub.RevealPlatformSetting(ctx, "GITHUB_TOKEN", by)
				return err
			},
			"platform set":    func() error { return r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "changed", by) },
			"platform delete": func() error { return r.hub.DeletePlatformSetting(ctx, "GITHUB_TOKEN", by) },
			"platform versions": func() error {
				_, err := r.hub.PlatformSettingVersions(ctx, "GITHUB_TOKEN", by)
				return err
			},
			"platform restore": func() error { return r.hub.RestorePlatformSetting(ctx, "GITHUB_TOKEN", 1, by) },
		}
		for name, call := range calls {
			err := call()
			if !isUserErr(err) || err.Error() != msgSecretValues {
				t.Errorf("%s: %s = %v, want the refusal", who, name, err)
			}
		}
	}
	// Nothing changed.
	if got, _ := r.sec.Inject("movies", core.Prod, []string{"TMDB_API_KEY", "ADMIN_TOKEN"}); got["TMDB_API_KEY"] != "tmdb-value-123" || got["ADMIN_TOKEN"] != before["ADMIN_TOKEN"] {
		t.Fatalf("values changed: %v", got)
	}
	if v, _ := r.sec.LoadPlatform("GITHUB_TOKEN"); v != "ghp_original" {
		t.Fatalf("platform value changed: %q", v)
	}
	if got := r.eventDetails("secret.viewed"); len(got) != 0 {
		t.Fatalf("refused reveals were logged as views: %v", got)
	}
	// An admin still sees names and whether each is set.
	envs, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil || len(envs) != 2 || !envs[1].Secrets[0].Set {
		t.Fatalf("admin list = %+v, %v", envs, err)
	}
}

func TestSecretsRoleCanUseSecretValues(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	// The role is given by the owner.
	m := core.Person{ID: "member", Name: "Mo", Email: "member@example.test", Roles: []string{"member", RoleSecrets}, Grants: map[string]string{}}
	if err := r.hub.UpdatePerson(core.WithViewer(ctx, owner), m); err != nil {
		t.Fatal(err)
	}
	for _, by := range []core.Viewer{secretsV, {PersonID: "member", Session: true}} {
		if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "first-value", by); err != nil {
			t.Fatal(err)
		}
		if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "second-value", by); err != nil {
			t.Fatal(err)
		}
		if v, err := r.hub.RevealSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", by); err != nil || v != "second-value" {
			t.Fatalf("reveal = %q, %v", v, err)
		}
		vs, err := r.hub.SecretVersions(ctx, "movies", core.Prod, "TMDB_API_KEY", by)
		if err != nil || len(vs) < 2 {
			t.Fatalf("versions = %+v, %v", vs, err)
		}
		if err := r.hub.RestoreSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", vs[1].Version, by); err != nil {
			t.Fatal(err)
		}
		if v, _ := r.hub.RevealSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", by); v != "first-value" {
			t.Fatalf("after restore = %q", v)
		}
		secretSpec(t, r, secretsDoc, core.Prod)
		if err := r.hub.RegenerateSecret(ctx, "movies", core.Prod, "ADMIN_TOKEN", by); err != nil {
			t.Fatal(err)
		}
		if err := r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_value_123", by); err != nil {
			t.Fatal(err)
		}
		if v, err := r.hub.RevealPlatformSetting(ctx, "GITHUB_TOKEN", by); err != nil || v != "ghp_value_123" {
			t.Fatalf("platform reveal = %q, %v", v, err)
		}
		if err := r.hub.DeleteSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", by); err != nil {
			t.Fatal(err)
		}
		if err := r.hub.DeletePlatformSetting(ctx, "GITHUB_TOKEN", by); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVersionsAndRestoreAreLogged(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	for i := 1; i <= 7; i++ {
		if err := r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", fmt.Sprintf("value-%d-abc", i), owner); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := r.hub.SecretVersions(ctx, "movies", core.Prod, "TMDB_API_KEY", owner)
	if err != nil || len(vs) != 5 || vs[0].Version != 7 || vs[4].Version != 3 {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if b, _ := json.Marshal(vs); strings.Contains(string(b), "value-") {
		t.Fatalf("versions hold values: %s", b)
	}
	if err := r.hub.RestoreSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", 4, owner); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.hub.RevealSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", owner); v != "value-4-abc" {
		t.Fatalf("restored value = %q", v)
	}
	vs, _ = r.hub.SecretVersions(ctx, "movies", core.Prod, "TMDB_API_KEY", owner)
	if vs[0].Version != 8 {
		t.Fatalf("restoring didn't make a new version: %+v", vs)
	}
	if err := r.hub.RestoreSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", 1, owner); !isUserErr(err) {
		t.Fatalf("restored a version that is gone: %v", err)
	}
	ev := r.eventDetails("secret.restored")
	if len(ev) != 1 || !strings.Contains(ev[0], "version 4") || strings.Contains(ev[0], "value-") {
		t.Fatalf("restore events = %v", ev)
	}
	// Platform credentials keep versions too.
	_ = r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_one_111", owner)
	_ = r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_two_222", owner)
	if err := r.hub.RestorePlatformSetting(ctx, "GITHUB_TOKEN", 1, owner); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.hub.RevealPlatformSetting(ctx, "GITHUB_TOKEN", owner); v != "ghp_one_111" {
		t.Fatalf("platform restored = %q", v)
	}
	if ev := r.eventDetails("setting.viewed"); len(ev) != 1 || strings.Contains(ev[0], "ghp_") {
		t.Fatalf("setting view events = %v", ev)
	}
}

func TestSecretListNeverHasValues(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "EXTRA", "extra-value-456", owner)
	_ = r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_value_789", owner)
	spec := secretSpec(t, r, secretsDoc, core.Prod)
	envs, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	set, _ := r.hub.PlatformSettings(ctx)
	b, _ := json.Marshal(map[string]any{"envs": envs, "settings": set})
	for _, v := range []string{"tmdb-value-123", "extra-value-456", "ghp_value_789", spec.Secrets["ADMIN_TOKEN"]} {
		if v == "" || strings.Contains(string(b), v) {
			t.Fatalf("the list holds %q (or it is empty): %s", v, b)
		}
	}
	if strings.Contains(string(b), `"value"`) {
		t.Fatalf("the list has a value field: %s", b)
	}
	if !strings.Contains(string(b), `"generated":true`) || !strings.Contains(string(b), "EXTRA") {
		t.Fatalf("the list lost names: %s", b)
	}
}

func TestOnlyOwnerGivesTheSecretsRole(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	asAdmin := core.WithViewer(ctx, adminV)
	asOwner := core.WithViewer(ctx, owner)
	member := core.Person{ID: "member", Name: "Mo", Email: "member@example.test", Roles: []string{"member"}, Grants: map[string]string{}}
	withRole := member
	withRole.Roles = []string{"member", RoleSecrets}

	if err := r.hub.UpdatePerson(asAdmin, withRole); !isUserErr(err) {
		t.Fatalf("admin gave the role: %v", err)
	}
	if err := r.hub.UpdatePerson(ctx, withRole); !isUserErr(err) {
		t.Fatalf("a change with nobody behind it gave the role: %v", err)
	}
	// An admin can't give it to themselves either.
	self := core.Person{ID: "admin", Name: "admin", Email: "admin@example.test", Roles: []string{"admin", RoleSecrets}, Grants: map[string]string{}}
	if err := r.hub.UpdatePerson(asAdmin, self); !isUserErr(err) {
		t.Fatalf("admin gave themselves the role: %v", err)
	}
	if _, err := r.hub.InvitePerson(asAdmin, core.Person{Name: "New", Email: "new@example.test", Roles: []string{"member", RoleSecrets}}, testBase); !isUserErr(err) {
		t.Fatalf("admin invited someone with the role: %v", err)
	}
	if p, _ := r.store.Person(ctx, "member"); contains(p.Roles, RoleSecrets) {
		t.Fatalf("role was stored: %+v", p)
	}

	if err := r.hub.UpdatePerson(asOwner, withRole); err != nil {
		t.Fatalf("owner couldn't give the role: %v", err)
	}
	if p, _ := r.store.Person(ctx, "member"); !contains(p.Roles, RoleSecrets) {
		t.Fatalf("role not stored: %+v", p)
	}
	if !r.hub.CanSeeSecretValues(ctx, "member") {
		t.Fatal("the role doesn't allow seeing values")
	}
	// An admin can still change other things, but not take the role away.
	renamed := withRole
	renamed.Name = "Mo B"
	if err := r.hub.UpdatePerson(asAdmin, renamed); err != nil {
		t.Fatalf("admin couldn't rename: %v", err)
	}
	if err := r.hub.UpdatePerson(asAdmin, member); !isUserErr(err) {
		t.Fatalf("admin took the role away: %v", err)
	}
	if p, _ := r.store.Person(ctx, "member"); !contains(p.Roles, RoleSecrets) {
		t.Fatalf("role was taken away: %+v", p)
	}
	if err := r.hub.UpdatePerson(asOwner, member); err != nil {
		t.Fatalf("owner couldn't take the role away: %v", err)
	}
	if r.hub.CanSeeSecretValues(ctx, "member") {
		t.Fatal("still allowed after the role was taken away")
	}
	if _, err := r.hub.InvitePerson(asOwner, core.Person{Name: "New", Email: "new@example.test", Roles: []string{"admin", RoleSecrets}}, testBase); err != nil {
		t.Fatalf("owner couldn't invite with the role: %v", err)
	}
	// Unknown roles are still refused.
	bad := member
	bad.Roles = []string{"member", "wizard"}
	if err := r.hub.UpdatePerson(asOwner, bad); !isUserErr(err) {
		t.Fatalf("unknown role accepted: %v", err)
	}
}

func TestSecretsAsFilesLeavesSecretsOutOfEnv(t *testing.T) {
	ctx := context.Background()
	doc := secretsDoc + "secrets_as: files\n"
	r := secretsRig(t, doc)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner)
	spec := secretSpec(t, r, doc, core.Prod)
	if !spec.SecretsAsFiles {
		t.Fatal("files mode not passed on")
	}
	if spec.Secrets["TMDB_API_KEY"] != "tmdb-value-123" || spec.Secrets["ADMIN_TOKEN"] == "" {
		t.Fatalf("secrets = %v", spec.Secrets)
	}
	for k, v := range spec.Vars {
		if k == "TMDB_API_KEY" || k == "ADMIN_TOKEN" || v == "tmdb-value-123" || v == spec.Secrets["ADMIN_TOKEN"] {
			t.Errorf("secret %s is in the environment", k)
		}
	}
	if spec.Vars["HOMELAB_APP"] != "movies" || spec.Vars["HOMELAB_TOKEN"] == "" {
		t.Fatalf("Homelab's own settings are gone: %v", spec.Vars)
	}
}

func TestSecretsAsEnvStillGivesBoth(t *testing.T) {
	ctx := context.Background()
	for _, doc := range []string{secretsDoc, secretsDoc + "secrets_as: env\n"} {
		r := secretsRig(t, doc)
		_ = r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "tmdb-value-123", owner)
		spec := secretSpec(t, r, doc, core.Prod)
		if spec.SecretsAsFiles {
			t.Fatal("env mode asked for files only")
		}
		if spec.Vars["TMDB_API_KEY"] != "tmdb-value-123" || spec.Vars["ADMIN_TOKEN"] == "" || spec.Vars["ADMIN_TOKEN"] != spec.Secrets["ADMIN_TOKEN"] {
			t.Fatalf("vars = %v", spec.Vars)
		}
		if spec.Secrets["TMDB_API_KEY"] != "tmdb-value-123" {
			t.Fatalf("secrets = %v", spec.Secrets)
		}
	}
}

func TestLogsHideSecretValues(t *testing.T) {
	ctx := context.Background()
	r := secretsRig(t, secretsDoc)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", "abcdef", owner)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "EXTRA_KEY", "abcdef-and-more", owner)
	_ = r.hub.SetSecret(ctx, "movies", core.Prod, "SHORT", "abc12", owner) // under 6: left alone
	_ = r.hub.SetSecret(ctx, "movies", core.Dev, "TMDB_API_KEY", "dev-only-value", owner)
	_ = r.hub.SetPlatformSetting(ctx, "GITHUB_TOKEN", "ghp_platform_1", owner)
	r.dep.LogTail = "key=abcdef extra=abcdef-and-more short=abc12 dev=dev-only-value gh=ghp_platform_1 again abcdef\n"
	out, err := r.hub.Logs(ctx, "movies", core.Prod, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := "logs of movies/prod\nkey=[secret TMDB_API_KEY] extra=[secret EXTRA_KEY] short=abc12 dev=dev-only-value gh=[secret GITHUB_TOKEN] again [secret TMDB_API_KEY]\n"
	if out != want {
		t.Fatalf("logs = %q\nwant   %q", out, want)
	}
}
