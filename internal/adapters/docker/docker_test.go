package docker

import (
	"encoding/json"
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
	b, err := composeYAML(testSpec())
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
	again, _ := composeYAML(testSpec())
	if string(again) != got {
		t.Error("compose file isn't the same on a second run")
	}
}

func TestComposeYAMLOmitsEmptyAndEscapes(t *testing.T) {
	s := testSpec()
	s.Memory, s.Mounts = "", nil
	s.Release.Commit = "a$b"
	b, err := composeYAML(s)
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
		if _, err := composeYAML(s); err == nil {
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
		{"created", "Created", core.StateStarting, ""},
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
	b, err := composeYAML(s)
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
	b, _ = composeYAML(s)
	f = composeFile{}
	json.Unmarshal(b, &f)
	if got := f.Services["jellyfin"]; got.Ports[0] != "8096:8096" || len(got.Devices) != 0 {
		t.Errorf("open ports, no graphics card: %+v", got)
	}

	s.Services = append(s.Services, core.Service{Name: "app", Image: "x:1"})
	if _, err := composeYAML(s); err == nil {
		t.Error("a service named app should be refused")
	}
}
