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
		"bad id":             strings.Replace(movies, "id: movies", "id: Movies!", 1),
		"dev suffix":         strings.Replace(movies, "id: movies", "id: movies-dev", 1),
		"reserved":           strings.Replace(movies, "id: movies", "id: homelab", 1),
		"tagged image":       strings.Replace(movies, "smart-recommender-player", "smart-recommender-player:latest", 1),
		"tag no commit":      strings.Replace(movies, `"sha-{commit}"`, "latest", 1),
		"no port":            strings.Replace(movies, "port: 8000", "port: 0", 1),
		"bad promotion":      movies + "promotion: sometimes\n",
		"promote_after":      movies + "promote_after: 10s\n",
		"empty smoke":        movies + "smoke_test: [python, \"\"]\n",
		"no id or repo":      strings.Replace(movies, "id: movies", "", 1),
		"secret clash":       strings.Replace(movies, "[TMDB_API_KEY]", "[CONFIG_PATH]", 1),
		"generated clash":    strings.Replace(movies, "secrets: [TMDB_API_KEY]", "secrets: [TMDB_API_KEY]\ngenerate_secrets: [TMDB_API_KEY]", 1),
		"generated env":      strings.Replace(movies, "secrets: [TMDB_API_KEY]", "generate_secrets: [CONFIG_PATH]", 1),
		"generated reserved": strings.Replace(movies, "secrets: [TMDB_API_KEY]", "generate_secrets: [HOMELAB_X]", 1),
		"generated bad name": strings.Replace(movies, "secrets: [TMDB_API_KEY]", "generate_secrets: [\"a-b\"]", 1),
		"bad memory":         strings.Replace(movies, "memory: 2g", "memory: lots", 1),
		"relative data":      strings.Replace(movies, "path: /data", "path: data", 1),
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

const withServices = `
folders:
  - {name: media, path: /media}
ports: ["6881-6891:6881-6891", "6881-6891:6881-6891/udp"]
services:
  - name: jellyfin
    image: jellyfin/jellyfin:10.11.11
    data: [{name: jellyfin-config, path: /config}]
    folders: [{name: media, path: /media}]
    ports: ["8096:8096"]
    gpu: true
    memory: 4g
  - name: prowlarr
    image: lscr.io/linuxserver/prowlarr:version-2.6.5.5623
    env: {PUID: "1000"}
`

func TestServices(t *testing.T) {
	m, err := Parse([]byte(movies+withServices), Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Services) != 2 || !m.Services[0].GPU || m.Services[1].Env["PUID"] != "1000" || m.Folders[0].Name != "media" || len(m.Ports) != 2 {
		t.Errorf("parsed %+v", m)
	}
	bad := map[string]string{
		"untagged image":  strings.Replace(withServices, "jellyfin/jellyfin:10.11.11", "jellyfin/jellyfin", 1),
		"latest":          strings.Replace(withServices, "jellyfin:10.11.11", "jellyfin:latest", 1),
		"named app":       strings.Replace(withServices, "name: prowlarr", "name: app", 1),
		"same name":       strings.Replace(withServices, "name: prowlarr", "name: jellyfin", 1),
		"homelab port":    strings.Replace(withServices, "8096:8096", "7100:8096", 1),
		"low port":        strings.Replace(withServices, "8096:8096", "80:8096", 1),
		"uneven range":    strings.Replace(withServices, "6881-6891:6881-6891\"", "6881-6891:6881-6890\"", 1),
		"bad port":        strings.Replace(withServices, "8096:8096", "8096", 1),
		"relative folder": strings.Replace(withServices, "{name: media, path: /media}]", "{name: media, path: media}]", 1),
		"path twice":      strings.Replace(withServices, "path: /config}", "path: /media}", 1),
		"bad env":         strings.Replace(withServices, "PUID:", "1PUID:", 1),
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(movies+doc), Defaults{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestGenerateSecrets(t *testing.T) {
	m, err := Parse([]byte(strings.Replace(movies, "secrets: [TMDB_API_KEY]", "secrets: [TMDB_API_KEY]\ngenerate_secrets: [ADMIN_TOKEN]", 1)), Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.GenerateSecrets, ","); got != "ADMIN_TOKEN" {
		t.Fatalf("GenerateSecrets = %s", got)
	}
}

func TestSecretsAs(t *testing.T) {
	m, err := Parse([]byte(movies), Defaults{})
	if err != nil || m.SecretsAs != SecretsAsEnv {
		t.Fatalf("default = %q, %v", m.SecretsAs, err)
	}
	for _, mode := range []string{"env", "files"} {
		m, err := Parse([]byte(movies+"secrets_as: "+mode+"\n"), Defaults{})
		if err != nil || m.SecretsAs != mode {
			t.Fatalf("%s: got %q, %v", mode, m.SecretsAs, err)
		}
	}
	for _, mode := range []string{"file", "Files", "both", `""x`} {
		if _, err := Parse([]byte(movies+"secrets_as: "+mode+"\n"), Defaults{}); err == nil {
			t.Errorf("secrets_as: %s was accepted", mode)
		}
	}
}

func TestSecretsAPI(t *testing.T) {
	m, err := Parse([]byte(movies), Defaults{})
	if err != nil || m.SecretsAPI != SecretsAPIRead {
		t.Fatalf("default = %q, %v", m.SecretsAPI, err)
	}
	for _, mode := range []string{"read", "write", "none"} {
		m, err := Parse([]byte(movies+"secrets_api: "+mode+"\n"), Defaults{})
		if err != nil || m.SecretsAPI != mode {
			t.Fatalf("%s: got %q, %v", mode, m.SecretsAPI, err)
		}
	}
	for _, mode := range []string{"Write", "rw", "yes", "true", "readwrite", "nope"} {
		_, err := Parse([]byte(movies+"secrets_api: "+mode+"\n"), Defaults{})
		if err == nil || !strings.Contains(err.Error(), "secrets_api must be read, write or none") {
			t.Errorf("secrets_api: %s gave %v", mode, err)
		}
	}
}

func TestLogsMinLevel(t *testing.T) {
	m, err := Parse([]byte(movies), Defaults{})
	if err != nil || m.Logs.MinLevel != "" || m.LogMinLevel("dev") != "debug" || m.LogMinLevel("prod") != "info" {
		t.Fatalf("defaults: %+v %v", m.Logs, err)
	}
	for _, lvl := range []string{"debug", "info", "warn", "error", "fatal"} {
		m, err := Parse([]byte(movies+"logs:\n  min_level: "+lvl+"\n"), Defaults{})
		if err != nil || m.LogMinLevel("dev") != lvl || m.LogMinLevel("prod") != lvl {
			t.Fatalf("%s: %+v %v", lvl, m.Logs, err)
		}
	}
	for _, lvl := range []string{"Info", "warning", "trace", "unknown", "5"} {
		_, err := Parse([]byte(movies+"logs:\n  min_level: "+lvl+"\n"), Defaults{})
		if err == nil || !strings.Contains(err.Error(), "logs: min_level must be one of") {
			t.Errorf("min_level %s gave %v", lvl, err)
		}
	}
}

func TestDatabase(t *testing.T) {
	for _, engine := range []string{"postgres", "sqlite"} {
		m, err := Parse([]byte(movies+"database: "+engine+"\n"), Defaults{})
		if err != nil || m.Database != engine {
			t.Fatalf("%s: %+v %v", engine, m, err)
		}
	}
	if m, err := Parse([]byte(movies), Defaults{}); err != nil || m.Database != "" {
		t.Fatalf("none: %+v %v", m, err)
	}
	svc := "services:\n  - {name: db, image: \"mariadb:11\"}\n"
	cases := map[string]struct{ doc, want string }{
		"unknown engine":      {movies + "database: mysql\n", "database must be postgres or sqlite"},
		"postgres db service": {movies + "database: postgres\n" + svc, "the name db is used by the database"},
		"password secret":     {movies + "database: postgres\ngenerate_secrets: [DATABASE_PASSWORD]\n", "DATABASE_PASSWORD"},
		"url secret":          {strings.Replace(movies, "[TMDB_API_KEY]", "[TMDB_API_KEY, DATABASE_URL]", 1) + "database: postgres\n", "DATABASE_URL"},
		"url setting":         {strings.Replace(movies, "dev: {LOG_LEVEL: debug}", "prod: {DATABASE_URL: x}", 1) + "database: postgres\n", "DATABASE_URL"},
		"sqlite url setting":  {strings.Replace(movies, "env: {", "env: {DATABASE_URL: x, ", 1) + "database: sqlite\n", "DATABASE_URL"},
		"sqlite path setting": {strings.Replace(movies, "env: {", "env: {DATABASE_PATH: x, ", 1) + "database: sqlite\n", "DATABASE_PATH"},
		"sqlite folder name":  {strings.Replace(movies, "name: data,", "name: homelab-db,", 1) + "database: sqlite\n", "homelab-db"},
		"sqlite folder path":  {strings.Replace(movies, "path: /data", "path: /homelab/db", 1) + "database: sqlite\n", "/homelab/db"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.doc), Defaults{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
	if _, err := Parse([]byte(movies+"database: sqlite\n"+svc), Defaults{}); err != nil {
		t.Errorf("sqlite with a service named db: %v", err)
	}
	// Without a database the names are free, as they always were.
	if _, err := Parse([]byte(movies+"generate_secrets: [DATABASE_URL]\n"+svc), Defaults{}); err != nil {
		t.Errorf("no database: %v", err)
	}
}

func TestDatabaseMapForms(t *testing.T) {
	ok := map[string]struct{ doc, engine, path string }{
		"map postgres":  {"database: {engine: postgres}\n", "postgres", ""},
		"map sqlite":    {"database: {engine: sqlite}\n", "sqlite", ""},
		"adopt a file":  {"database: {engine: sqlite, path: /data/catalogue_full.db}\n", "sqlite", "/data/catalogue_full.db"},
		"adopt deeper":  {"database: {engine: sqlite, path: /data/db/app.sqlite}\n", "sqlite", "/data/db/app.sqlite"},
		"block form":    {"database:\n  engine: sqlite\n  path: /data/x.db\n", "sqlite", "/data/x.db"},
		"empty is none": {"database:\n", "", ""},
	}
	for name, c := range ok {
		m, err := Parse([]byte(movies+c.doc), Defaults{})
		if err != nil || m.Database != c.engine || m.DatabasePath != c.path {
			t.Errorf("%s: %q %q %v", name, m.Database, m.DatabasePath, err)
		}
	}
	m, _ := Parse([]byte(movies+"database: {engine: sqlite, path: /data/db/app.sqlite}\n"), Defaults{})
	if in, folder, rel, found := m.SQLiteFile(); !found || in != "/data/db/app.sqlite" || folder != "data" || rel != "db/app.sqlite" || m.OwnsDatabaseFolder() {
		t.Errorf("adopted: %q %q %q %v", in, folder, rel, found)
	}
	m, _ = Parse([]byte(movies+"database: sqlite\n"), Defaults{})
	if in, folder, rel, found := m.SQLiteFile(); !found || in != "/homelab/db/app.db" || folder != "homelab-db" || rel != "app.db" || !m.OwnsDatabaseFolder() {
		t.Errorf("own folder: %q %q %q %v", in, folder, rel, found)
	}
	// The deepest data mount wins when they nest.
	nested := strings.Replace(movies, "- {name: data, path: /data}", "- {name: data, path: /data}\n  - {name: big, path: /data/big}", 1)
	m, err := Parse([]byte(nested+"database: {engine: sqlite, path: /data/big/x.db}\n"), Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	if _, folder, rel, _ := m.SQLiteFile(); folder != "big" || rel != "x.db" {
		t.Errorf("nested: %q %q", folder, rel)
	}
}

func TestDatabaseMapErrors(t *testing.T) {
	svcData := "services:\n  - {name: jelly, image: \"j:1\", data: [{name: jd, path: /jd}]}\n"
	folders := strings.Replace(movies, "data:\n", "folders:\n  - {name: media, path: /media}\ndata:\n", 1)
	cases := map[string]struct{ doc, want string }{
		"no engine":          {movies + "database: {path: /data/x.db}\n", "database.engine must be postgres or sqlite"},
		"unknown engine":     {movies + "database: {engine: mysql}\n", "database must be postgres or sqlite"},
		"unknown key":        {movies + "database: {engine: sqlite, file: /data/x.db}\n", `"file" isn't known`},
		"path for postgres":  {movies + "database: {engine: postgres, path: /data/x.db}\n", "only for sqlite"},
		"relative path":      {movies + "database: {engine: sqlite, path: data/x.db}\n", "absolute"},
		"dotdot":             {movies + "database: {engine: sqlite, path: /data/../etc/x.db}\n", "no .."},
		"outside data":       {movies + "database: {engine: sqlite, path: /elsewhere/x.db}\n", "inside one of the app's data folders"},
		"the mount itself":   {movies + "database: {engine: sqlite, path: /data}\n", "inside one of the app's data folders"},
		"similar prefix":     {movies + "database: {engine: sqlite, path: /database/x.db}\n", "inside one of the app's data folders"},
		"in a shared folder": {folders + "database: {engine: sqlite, path: /media/x.db}\n", "inside one of the app's data folders"},
		"in a service":       {movies + svcData + "database: {engine: sqlite, path: /jd/x.db}\n", "inside one of the app's data folders"},
		"list":               {movies + "database: [sqlite]\n", "database must be postgres or sqlite"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.doc), Defaults{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
}

func TestDatabasePathSetting(t *testing.T) {
	adopt := "database: {engine: sqlite, path: /data/catalogue_full.db}\n"
	with := func(env string) string { return strings.Replace(movies, "env: {", env, 1) + adopt }
	// The same path is fine, in env and in env_for.
	same := with("env: {DATABASE_PATH: /data/catalogue_full.db, ")
	if _, err := Parse([]byte(same), Defaults{}); err != nil {
		t.Errorf("same path in env: %v", err)
	}
	sameFor := strings.Replace(movies, "dev: {LOG_LEVEL: debug}", "dev: {DATABASE_PATH: /data/catalogue_full.db}", 1) + adopt
	if _, err := Parse([]byte(sameFor), Defaults{}); err != nil {
		t.Errorf("same path in env_for: %v", err)
	}
	// A different path is an error that says which one to use.
	for name, doc := range map[string]string{
		"env":     with("env: {DATABASE_PATH: /data/other.db, "),
		"env_for": strings.Replace(movies, "dev: {LOG_LEVEL: debug}", "dev: {DATABASE_PATH: /data/other.db}", 1) + adopt,
	} {
		_, err := Parse([]byte(doc), Defaults{})
		if err == nil || !strings.Contains(err.Error(), "must be /data/catalogue_full.db") {
			t.Errorf("different path in %s: %v", name, err)
		}
	}
	// DATABASE_URL and the secrets lists stay refused.
	if _, err := Parse([]byte(with("env: {DATABASE_URL: x, ")), Defaults{}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("url: %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(movies, "[TMDB_API_KEY]", "[TMDB_API_KEY, DATABASE_PATH]", 1)+adopt), Defaults{}); err == nil || !strings.Contains(err.Error(), "remove it from secrets") {
		t.Errorf("DATABASE_PATH as a secret: %v", err)
	}
	// Without a path of its own, the app may not set DATABASE_PATH at all.
	if _, err := Parse([]byte(strings.Replace(movies, "env: {", "env: {DATABASE_PATH: /homelab/db/app.db, ", 1)+"database: sqlite\n"), Defaults{}); err == nil {
		t.Error("DATABASE_PATH with the default sqlite folder was accepted")
	}
}
