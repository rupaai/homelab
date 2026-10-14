package manifest

import (
	"strings"
	"testing"
)

const movies = `
id: movies
name: Movie Night
image: ghcr.io/rupaai/smart-recommender-player
tag: "sha-{commit}"
build: {workflow: ci.yml}
port: 8000
health: /healthz
data:
  - {name: data, path: /data}
env: {CONFIG_PATH: /app/homelab/config.yaml}
env_for:
  dev: {LOG_LEVEL: debug}
secrets: [TMDB_API_KEY]
memory: 2g
`

func TestParseDefaults(t *testing.T) {
	m, err := Parse([]byte(movies), Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Build.Branch != "main" || m.Promotion != "auto-dev" || len(m.Environments) != 2 {
		t.Fatalf("defaults not applied: %+v", m)
	}
	if got := m.TagFor("abc"); got != "sha-abc" {
		t.Fatalf("TagFor = %q", got)
	}
	if v := m.VarsFor("dev"); v["LOG_LEVEL"] != "debug" || v["CONFIG_PATH"] == "" {
		t.Fatalf("VarsFor(dev) = %v", v)
	}
	if v := m.VarsFor("prod"); v["LOG_LEVEL"] != "" {
		t.Fatalf("VarsFor(prod) leaked a dev setting: %v", v)
	}
	if AddressName("movies", "prod") != "movies" || AddressName("movies", "dev") != "movies-dev" {
		t.Fatal("AddressName")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"bad id":        strings.Replace(movies, "id: movies", "id: Movies!", 1),
		"dev suffix":    strings.Replace(movies, "id: movies", "id: movies-dev", 1),
		"reserved":      strings.Replace(movies, "id: movies", "id: homelab", 1),
		"tagged image":  strings.Replace(movies, "smart-recommender-player", "smart-recommender-player:latest", 1),
		"tag no commit": strings.Replace(movies, `"sha-{commit}"`, "latest", 1),
		"no port":       strings.Replace(movies, "port: 8000", "port: 0", 1),
		"bad promotion": movies + "promotion: sometimes\n",
		"promote_after": movies + "promote_after: 10s\n",
		"empty smoke":   movies + "smoke_test: [python, \"\"]\n",
		"no id or repo": strings.Replace(movies, "id: movies", "", 1),
		"secret clash":  strings.Replace(movies, "[TMDB_API_KEY]", "[CONFIG_PATH]", 1),
		"bad memory":    strings.Replace(movies, "memory: 2g", "memory: lots", 1),
		"relative data": strings.Replace(movies, "path: /data", "path: data", 1),
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc), Defaults{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRegistryPortAllowed(t *testing.T) {
	doc := strings.Replace(movies, "ghcr.io/", "localhost:5000/", 1)
	if _, err := Parse([]byte(doc), Defaults{}); err != nil {
		t.Fatalf("a registry with a port should be allowed: %v", err)
	}
}

func TestDefaultsFromRepo(t *testing.T) {
	m, err := Parse([]byte("port: 8080\npromotion: auto\npromote_after: 2m\nsmoke_test: [python, -m, app.smoke]\n"),
		Defaults{Repo: "RupaAI/Homelab_Hello", ImageHost: "ghcr.io"})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "rupaai-homelab-hello" || m.Name != "Homelab Hello" || m.Image != "ghcr.io/rupaai/homelab_hello" {
		t.Fatalf("defaults: id=%q name=%q image=%q", m.ID, m.Name, m.Image)
	}
	if m.PromoteAfter.Minutes() != 2 || len(m.SmokeTest) != 3 || m.Promotion != "auto" {
		t.Fatalf("auto fields: %+v", m)
	}
	// A manifest's own id wins.
	m, err = Parse([]byte(movies), Defaults{Repo: "rupaai/smart-recommender-player", ImageHost: "ghcr.io"})
	if err != nil || m.ID != "movies" || m.PromoteAfter != DefaultPromoteAfter {
		t.Fatalf("own id: %v %+v", err, m)
	}
}

func TestIDFromRepo(t *testing.T) {
	for in, want := range map[string]string{
		"rupaai/homelab-hello":            "rupaai-homelab-hello",
		"a/b":                             "a-b",
		"9lives/app":                      "app-9lives-app",
		"x/" + strings.Repeat("long", 20): ("x-" + strings.Repeat("long", 20))[:59],
	} {
		if got := IDFromRepo(in); got != want || !idPattern.MatchString(got) {
			t.Errorf("IDFromRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
