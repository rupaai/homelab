package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func testSpec() core.EnvSpec {
	return core.EnvSpec{
		App: "movies", Env: "dev",
		Release:  core.Release{Commit: "abc1234", Image: "ghcr.io/rupaai/movies", Tag: "sha-abc1234", Digest: "sha256:deadbeef"},
		Port:     8080,
		HostPort: 18080,
		Memory:   "2g",
		Mounts:   []core.Mount{{HostPath: "/data/movies", ContainerPath: "/data"}},
	}
}

func TestComposeYAML(t *testing.T) {
	b, err := composeYAML(testSpec(), "/srv/homelab/movies/prod")
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"image": "ghcr.io/rupaai/movies@sha256:deadbeef"`,
		`"pull_policy": "never"`,
		`"restart": "unless-stopped"`,
		`"127.0.0.1:18080:8080"`,
		`"mem_limit": "2g"`,
		`"max-size": "10m"`,
		`"max-file": "3"`,
		`"homelab.app": "movies"`,
		`"homelab.env": "dev"`,
		`"homelab.commit": "abc1234"`,
		`"homelab.digest": "sha256:deadbeef"`,
		`"source": "/data/movies"`,
		`".env"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compose file is missing %s:\n%s", want, got)
		}
	}
	again, _ := composeYAML(testSpec(), "/srv/homelab/movies/prod")
	if string(again) != got {
		t.Error("compose file isn't the same on a second run")
	}
}

func TestComposeYAMLOmitsEmptyAndEscapes(t *testing.T) {
	s := testSpec()
	s.Memory, s.Mounts = "", nil
	s.Release.Commit = "a$b"
	b, err := composeYAML(s, "/srv/homelab/movies/prod")
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "mem_limit") || strings.Contains(got, "volumes") {
		t.Errorf("empty memory and mounts should be left out:\n%s", got)
	}
	if !strings.Contains(got, `"a$$b"`) {
		t.Errorf("$ should be doubled:\n%s", got)
	}
}

func TestComposeYAMLRejects(t *testing.T) {
	bad := func(f func(*core.EnvSpec)) {
		t.Helper()
		s := testSpec()
		f(&s)
		if _, err := composeYAML(s, "/srv/homelab/movies/prod"); err == nil {
			t.Error("expected an error")
		}
	}
	bad(func(s *core.EnvSpec) { s.App = "../etc" })
	bad(func(s *core.EnvSpec) { s.Env = "Prod" })
	bad(func(s *core.EnvSpec) { s.Port = 0 })
	bad(func(s *core.EnvSpec) { s.Release.Image = "" })
	bad(func(s *core.EnvSpec) { s.Mounts = []core.Mount{{HostPath: "rel", ContainerPath: "/x"}} })
}

func TestEnvFile(t *testing.T) {
	b, err := envFile(map[string]string{"B": "two words # not a comment", "A": "it's $HOME", "C": ""})
	if err != nil {
		t.Fatal(err)
	}
	want := "A=\"it's \\$HOME\"\nB='two words # not a comment'\nC=''\n"
	if string(b) != want {
		t.Errorf("got %q, want %q", b, want)
	}
}

func TestEnvFileRejects(t *testing.T) {
	for _, vars := range []map[string]string{
		{"A": "line1\nline2"},
		{"A": "x\ry"},
		{"1BAD": "x"},
		{"A B": "x"},
		{"COMPOSE_PROJECT_NAME": "x"},
	} {
		if _, err := envFile(vars); err == nil {
			t.Errorf("expected an error for %v", vars)
		}
	}
}

func TestMapState(t *testing.T) {
	cases := []struct{ state, status, wantState, wantDetail string }{
		{"running", "Up 3 minutes", core.StateRunning, ""},
		{"created", "Created", core.StateExited, "never started"},
		{"restarting", "Restarting (1) 5 seconds ago", core.StateRestarting, "restarting after exiting with code 1"},
		{"exited", "Exited (1) 2 minutes ago", core.StateExited, "exited with code 1"},
		{"exited", "Exited (0) 2 minutes ago", core.StateExited, "exited with code 0"},
		{"exited", "Exited", core.StateExited, "exited"},
		{"dead", "", core.StateExited, "exited"},
	}
	for _, c := range cases {
		s, d := mapState(c.state, c.status)
		if s != c.wantState || d != c.wantDetail {
			t.Errorf("%s/%q: got %s,%q want %s,%q", c.state, c.status, s, d, c.wantState, c.wantDetail)
		}
	}
}

func TestSplitRefAndAuth(t *testing.T) {
	for in, want := range map[string][2]string{
		"ghcr.io/a/b@sha256:abc":         {"ghcr.io/a/b", "sha256:abc"},
		"localhost:5000/x/y:v1":          {"localhost:5000/x/y", "v1"},
		"localhost:5000/x/y":             {"localhost:5000/x/y", "latest"},
		"registry:2":                     {"registry", "2"},
		"127.0.0.1:5000/a@sha256:abc123": {"127.0.0.1:5000/a", "sha256:abc123"},
	} {
		if r, tag := splitRef(in); r != want[0] || tag != want[1] {
			t.Errorf("%s: got %s %s", in, r, tag)
		}
	}
	if got := encodeAuth(core.RegistryAuth{}, "x"); got != "e30=" {
		t.Errorf("empty auth: %q", got)
	}
	if got := encodeAuth(core.RegistryAuth{Username: "u", Password: "p"}, "ghcr.io/a/b"); !strings.HasPrefix(got, "eyJ") {
		t.Errorf("auth: %q", got)
	}
}

func TestLastLinesAndTail(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "line")
	}
	lines[49] = "last"
	got := lastLines([]byte(strings.Join(lines, "\n")+"\n"), 20)
	if n := len(strings.Split(got, "\n")); n != 20 || !strings.HasSuffix(got, "last") {
		t.Errorf("got %d lines: %q", n, got)
	}
	tb := &tailBuffer{max: 5}
	tb.Write([]byte("abc"))
	tb.Write([]byte("defg"))
	if string(tb.Bytes()) != "cdefg" {
		t.Errorf("got %q", tb.Bytes())
	}
}

func TestComposeYAMLServices(t *testing.T) {
	old := renderGroup
	defer func() { renderGroup = old }()
	renderGroup = func() string { return "993" }

	s := testSpec()
	s.Ports = []string{"6881-6891:6881-6891/udp"}
	s.Services = []core.Service{
		{Name: "jellyfin", Image: "jellyfin/jellyfin:10.11.11", Ports: []string{"8096:8096"}, Memory: "4g", GPU: true,
			Mounts: []core.Mount{{HostPath: "/data/jellyfin-config", ContainerPath: "/config"}}},
		{Name: "prowlarr", Image: "lscr.io/linuxserver/prowlarr:2.6", Vars: map[string]string{"PUID": "1000"}},
	}
	b, err := composeYAML(s, "/srv/homelab/movies/prod")
	if err != nil {
		t.Fatal(err)
	}
	var f composeFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	app, jf, pr := f.Services["app"], f.Services["jellyfin"], f.Services["prowlarr"]
	if len(app.Ports) != 2 || app.Ports[1] != "127.0.0.1:6881-6891:6881-6891/udp" {
		t.Errorf("app ports = %v", app.Ports)
	}
	if jf.Image != "jellyfin/jellyfin:10.11.11" || jf.Ports[0] != "127.0.0.1:8096:8096" || jf.MemLimit != "4g" ||
		jf.Volumes[0].Target != "/config" || jf.Devices[0] != "/dev/dri:/dev/dri" || jf.GroupAdd[0] != "993" {
		t.Errorf("jellyfin = %+v", jf)
	}
	if jf.Labels[labelApp] != "movies" || jf.Labels[labelService] != "jellyfin" || jf.Labels[labelDigest] != "" || len(jf.EnvFile) != 0 {
		t.Errorf("jellyfin labels and settings = %+v", jf)
	}
	if pr.Environment["PUID"] != "1000" || len(pr.Devices) != 0 {
		t.Errorf("prowlarr = %+v", pr)
	}

	s.OpenPorts = true
	renderGroup = func() string { return "" }
	b, _ = composeYAML(s, "/srv/homelab/movies/prod")
	f = composeFile{}
	json.Unmarshal(b, &f)
	if got := f.Services["jellyfin"]; got.Ports[0] != "8096:8096" || len(got.Devices) != 0 {
		t.Errorf("open ports, no graphics card: %+v", got)
	}

	s.Services = append(s.Services, core.Service{Name: "app", Image: "x:1"})
	if _, err := composeYAML(s, "/srv/homelab/movies/prod"); err == nil {
		t.Error("a service named app should be refused")
	}
}

func TestEnvRunning(t *testing.T) {
	c := func(service, state, status string) container {
		l := map[string]string{"com.docker.compose.service": service, labelApp: "movies", labelEnv: "prod"}
		if service == serviceName {
			l[labelCommit], l[labelDigest] = "abc", "sha256:abc"
		}
		return container{ID: service + "-id", State: state, Status: status, Labels: l}
	}
	cases := []struct {
		name               string
		cs                 []container
		wantState, wantDet string
		wantIncomplete     bool
	}{
		{"all running", []container{c("app", "running", "Up"), c("jellyfin", "running", "Up")}, core.StateRunning, "", false},
		// The bug: compose created the app but couldn't start it (port taken), and Homelab
		// waited forever for it to start.
		{"app never started", []container{c("app", "created", "Created"), c("jellyfin", "running", "Up")}, core.StateExited, "never started", false},
		// A service that is down never changes the app's state: only the app has a health check.
		{"service never started", []container{c("app", "running", "Up"), c("jellyfin", "created", "Created")}, core.StateRunning, "jellyfin never started", true},
		{"service stopped", []container{c("app", "running", "Up"), c("prowlarr", "exited", "Exited (1) 1 minute ago")}, core.StateRunning, "prowlarr exited with code 1", true},
		{"service restarting", []container{c("app", "running", "Up"), c("jellyfin", "restarting", "Restarting (2) 1 second ago")}, core.StateRunning, "", false},
		{"two services stopped", []container{c("app", "running", "Up"), c("jellyfin", "exited", "Exited (2)"), c("prowlarr", "exited", "Exited (1)")}, core.StateRunning, "jellyfin exited with code 2, prowlarr exited with code 1", true},
		{"app missing", []container{c("jellyfin", "running", "Up")}, core.StateExited, "the app's container is missing", true},
	}
	for _, tc := range cases {
		r, id := envRunning(tc.cs)
		if r.State != tc.wantState || r.Detail != tc.wantDet || r.Incomplete != tc.wantIncomplete {
			t.Errorf("%s: got %s %q incomplete=%v, want %s %q incomplete=%v", tc.name, r.State, r.Detail, r.Incomplete, tc.wantState, tc.wantDet, tc.wantIncomplete)
		}
		if tc.name != "app missing" && (id != "app-id" || r.Digest != "sha256:abc" || r.Commit != "abc") {
			t.Errorf("%s: version should come from the app container, got %q %+v", tc.name, id, r)
		}
	}
}

func TestComposeYAMLHostGateway(t *testing.T) {
	old := addHostGateway
	defer func() { addHostGateway = old }()
	for _, on := range []bool{false, true} {
		addHostGateway = on
		b, err := composeYAML(testSpec(), "/srv/homelab/movies/prod")
		if err != nil {
			t.Fatal(err)
		}
		var f composeFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		got := f.Services["app"].ExtraHosts
		if on != (len(got) == 1 && got[0] == "host.docker.internal:host-gateway") || (!on && len(got) != 0) {
			t.Errorf("addHostGateway=%v: extra_hosts %v", on, got)
		}
	}
}

func TestComposeMountsSecretsReadOnly(t *testing.T) {
	s := testSpec()
	s.Secrets = map[string]string{"TMDB_API_KEY": "tmdb-value-123"}
	b, err := composeYAML(s, "/srv/homelab/movies/dev")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Services map[string]struct {
			Volumes     []map[string]any  `json:"volumes"`
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, v := range f.Services["app"].Volumes {
		if v["target"] == "/run/secrets" {
			found = v["source"] == "/srv/homelab/movies/dev/secrets" && v["read_only"] == true && v["type"] == "bind"
		}
	}
	if !found {
		t.Fatalf("secrets folder isn't mounted read-only at /run/secrets:\n%s", b)
	}
	if strings.Contains(string(b), "tmdb-value-123") || len(f.Services["app"].Environment) != 0 {
		t.Fatalf("a secret is in the compose file:\n%s", b)
	}
	// Without secrets there is nothing to mount.
	b, _ = composeYAML(testSpec(), "/srv/homelab/movies/dev")
	if strings.Contains(string(b), "/run/secrets") {
		t.Fatalf("mounted secrets when there are none:\n%s", b)
	}
}

func TestWriteSecretFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := writeSecretFiles(dir, map[string]string{"A": "one", "B": "two"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("folder = %v, %v", info, err)
	}
	for name, want := range map[string]string{"A": "one", "B": "two"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", name, b, err)
		}
		if info, _ := os.Stat(filepath.Join(dir, name)); info.Mode().Perm() != 0o444 {
			t.Errorf("%s mode = %v", name, info.Mode().Perm())
		}
	}
	// A second deploy replaces values and removes files of secrets that are gone.
	_ = os.WriteFile(filepath.Join(dir, ".stale.tmp"), []byte("x"), 0o600)
	if err := writeSecretFiles(dir, map[string]string{"A": "uno"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "A" {
		t.Fatalf("files = %v", entries)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "A")); string(b) != "uno" {
		t.Fatalf("A = %q", b)
	}
	if err := writeSecretFiles(dir, map[string]string{"../evil": "x"}); err == nil {
		t.Fatal("accepted a name that could leave the folder")
	}
	if err := writeSecretFiles(dir, nil); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
}
