package hub

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/manifest"
)

func TestGeneratedSecretFormat(t *testing.T) {
	re := regexp.MustCompile(`^[a-hjkmnp-z2-9]{5}(-[a-hjkmnp-z2-9]{5}){3}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		v, err := newGeneratedSecret()
		if err != nil || !re.MatchString(v) {
			t.Fatalf("bad value %q: %v", v, err)
		}
		seen[v] = true
	}
	if len(seen) != 200 {
		t.Fatal("values repeat")
	}
}

func TestGeneratedSecrets(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.addPeople()
	doc := strings.Replace(manifestDoc, "secrets: [TMDB_API_KEY]", "secrets: [TMDB_API_KEY]\ngenerate_secrets: [ADMIN_TOKEN]", 1)
	r.src.Push(repo, "main", "aaaaaaa1", "change", []byte(doc))
	r.reg.Put("ghcr.io/rupaai/movies", "sha-aaaaaaa1", "sha256:aaaaaaa1")
	r.build.Set(repo, core.Build{Commit: "aaaaaaa1", Status: core.BuildPassed})
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		t.Fatal(err)
	}
	r.hub.CheckBuilds(ctx)
	r.settle()

	// Before any deploy in prod it is not made yet, and it never counts as missing.
	envs, err := r.hub.AppSecrets(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	var prod SecretView
	for _, s := range envs[1].Secrets {
		if s.Name == "ADMIN_TOKEN" {
			prod = s
		}
	}
	if !prod.Generated || prod.Set {
		t.Fatalf("prod before deploy: %+v", prod)
	}
	if e := r.env(core.Dev); len(e.MissingSecrets) != 1 || e.MissingSecrets[0] != "TMDB_API_KEY" {
		t.Fatalf("dev missing = %v", e.MissingSecrets)
	}
	p, err := r.hub.PreviewPromote(ctx, "movies")
	if err != nil || len(p.MissingSecrets) != 1 || p.MissingSecrets[0] != "TMDB_API_KEY" {
		t.Fatalf("preview: %+v %v", p, err)
	}

	// Dev's deploy made it and passed it to the container.
	v := r.dep.Specs["movies/dev"].Vars["ADMIN_TOKEN"]
	if v == "" {
		t.Fatal("dev did not get ADMIN_TOKEN")
	}
	if envs, _ = r.hub.AppSecrets(ctx, "movies"); !envs[0].Secrets[1].Generated || !envs[0].Secrets[1].Set {
		t.Fatalf("dev view: %+v", envs[0].Secrets)
	}

	// Running it again keeps the value; prod gets its own.
	m, err := manifest.Parse([]byte(doc), manifest.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.hub.ensureGenerated(m, "movies", core.Dev); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.sec.Inject("movies", core.Dev, []string{"ADMIN_TOKEN"}); got["ADMIN_TOKEN"] != v {
		t.Fatalf("value changed: %q != %q", got["ADMIN_TOKEN"], v)
	}
	if err := r.hub.ensureGenerated(m, "movies", core.Prod); err != nil {
		t.Fatal(err)
	}
	pv, _ := r.sec.Inject("movies", core.Prod, []string{"ADMIN_TOKEN"})
	if pv["ADMIN_TOKEN"] == "" || pv["ADMIN_TOKEN"] == v {
		t.Fatalf("prod value: %q", pv["ADMIN_TOKEN"])
	}
	if err := r.hub.ensureGenerated(m, "movies", core.Prod); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.sec.Inject("movies", core.Prod, []string{"ADMIN_TOKEN"}); again["ADMIN_TOKEN"] != pv["ADMIN_TOKEN"] {
		t.Fatal("ensureGenerated overwrote a value")
	}

	// Make new replaces it; logs mask it.
	if err := r.hub.RegenerateSecret(ctx, "movies", core.Prod, "ADMIN_TOKEN", owner); err != nil {
		t.Fatal(err)
	}
	nv, _ := r.sec.Inject("movies", core.Prod, []string{"ADMIN_TOKEN"})
	if nv["ADMIN_TOKEN"] == pv["ADMIN_TOKEN"] {
		t.Fatal("not replaced")
	}
	if err := r.hub.RegenerateSecret(ctx, "movies", core.Prod, "TMDB_API_KEY", owner); err == nil {
		t.Fatal("regenerated a normal secret")
	}
	a, _ := r.store.App(ctx, "movies")
	if out := r.hub.mask(ctx, a, core.Prod, "token "+nv["ADMIN_TOKEN"]); strings.Contains(out, nv["ADMIN_TOKEN"]) {
		t.Fatalf("not masked: %s", out)
	}
}
