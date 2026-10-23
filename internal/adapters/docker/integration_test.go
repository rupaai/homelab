package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Set HOMELAB_DOCKER_TESTS=1 to run this against real Docker. It starts a throwaway
// registry on 127.0.0.1:5000, pushes two tiny images and removes everything afterwards.
const (
	testRegistry = "127.0.0.1:5000"
	testImage    = testRegistry + "/homelab-itest/web"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// buildAndPush builds a busybox image that serves "<version>" at /healthz and returns its digest.
func buildAndPush(t *testing.T, version string) string {
	dir := t.TempDir()
	df := fmt.Sprintf(`FROM mirror.gcr.io/library/busybox
RUN mkdir /www && echo %s > /www/healthz
CMD ["sh", "-c", "echo hello from %s greeting=$GREETING; exec httpd -f -vv -p 8080 -h /www"]
`, version, version)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := testImage + ":" + version
	sh(t, dir, "build", "-q", "-t", tag, ".")
	out := sh(t, dir, "push", tag)
	i := strings.Index(out, "digest: sha256:")
	if i < 0 {
		t.Fatalf("no digest in push output:\n%s", out)
	}
	digest := strings.Fields(out[i+len("digest: "):])[0]
	sh(t, dir, "rmi", "-f", tag) // so Prepare really has to download it
	return digest
}

// buildSide builds and pushes a service image that just sleeps, and returns its reference.
func buildSide(t *testing.T) string {
	dir := t.TempDir()
	df := "FROM mirror.gcr.io/library/busybox\nCMD [\"sleep\", \"3600\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := testRegistry + "/homelab-itest/side:1"
	sh(t, dir, "build", "-q", "-t", ref, ".")
	sh(t, dir, "push", ref)
	sh(t, dir, "rmi", "-f", ref)
	return ref
}

// itestRuns keeps only the environments this test made; other Homelab apps may run here.
func itestRuns(runs []core.Running) []core.Running {
	var out []core.Running
	for _, r := range runs {
		if r.App == "itest" {
			out = append(out, r)
		}
	}
	return out
}

// sideContainer returns the id and state of the service container.
func sideContainer(t *testing.T, project string) (id, state string) {
	t.Helper()
	id = sh(t, "", "ps", "-a", "-q", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=homelab.service=side")
	if id == "" {
		t.Fatal("no service container")
	}
	return id, sh(t, "", "inspect", "-f", "{{.State.Status}}", id)
}

func fileOwner(t *testing.T, path string) uint32 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t).Uid
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitBody(t *testing.T, port int, want string) {
	t.Helper()
	var last string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
		if err != nil {
			last = err.Error()
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if last = strings.TrimSpace(string(b)); last == want {
			return
		}
	}
	t.Fatalf("port %d never answered %q (last: %q)", port, want, last)
}

// startRegistry runs the throwaway registry and removes it when the test ends.
func startRegistry(t *testing.T) {
	t.Helper()
	exec.Command("docker", "rm", "-f", "homelab-itest-registry").Run()
	sh(t, "", "run", "-d", "--name", "homelab-itest-registry", "-p", testRegistry+":5000", "mirror.gcr.io/library/registry:2")
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "-v", "homelab-itest-registry").Run()
	})
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if resp, err := http.Get("http://" + testRegistry + "/v2/"); err == nil {
			resp.Body.Close()
			break
		}
	}
}

func TestIntegrationFullCycle(t *testing.T) {
	if os.Getenv("HOMELAB_DOCKER_TESTS") != "1" {
		t.Skip("set HOMELAB_DOCKER_TESTS=1 to run against real Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	startRegistry(t)
	d1, d2 := buildAndPush(t, "v1"), buildAndPush(t, "v2")
	side := buildSide(t)
	t.Cleanup(func() {
		exec.Command("docker", "rmi", "-f", testImage+"@"+d1, testImage+"@"+d2, side).Run()
	})

	root := t.TempDir()
	dep, err := New(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	const app, env = "itest", "dev"
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", project(app, env), "down", "-v", "--remove-orphans").Run()
	})

	port := freePort(t)
	data := t.TempDir()
	spec := func(version, digest string) core.EnvSpec {
		return core.EnvSpec{
			App: app, Env: env,
			Release:  core.Release{Commit: "commit-" + version, Image: testImage, Tag: version, Digest: digest},
			Port:     8080,
			HostPort: port,
			Memory:   "64m",
			Mounts:   []core.Mount{{HostPath: data, ContainerPath: "/data"}},
			Vars:     map[string]string{"GREETING": `it's $HOME "ok" # fine`},
			Services: []core.Service{{Name: "side", Image: side}},
		}
	}

	// Prepare downloads by digest, and a second time finds the image already here.
	if err := dep.Prepare(ctx, spec("v1", d1)); err != nil {
		t.Fatal("prepare:", err)
	}
	if have, err := dep.api.hasImage(ctx, testImage+"@"+d1); err != nil || !have {
		t.Fatalf("image should be here after Prepare: %v %v", have, err)
	}
	if err := dep.Prepare(ctx, spec("v1", d1)); err != nil {
		t.Fatal("second prepare:", err)
	}
	if err := dep.Prepare(ctx, core.EnvSpec{App: app, Env: env, Release: core.Release{Image: testImage, Digest: "sha256:" + strings.Repeat("0", 64)}}); err == nil {
		t.Error("downloading an unknown digest should fail")
	}

	if err := dep.Deploy(ctx, spec("v1", d1)); err != nil {
		t.Fatal("deploy v1:", err)
	}
	waitBody(t, port, "v1")
	if st, err := os.Stat(filepath.Join(root, app, env, ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf(".env should be mode 0600: %v %v", st, err)
	}

	runs, err := dep.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runs = itestRuns(runs)
	if len(runs) != 1 || runs[0].App != app || runs[0].Env != env || runs[0].State != core.StateRunning ||
		runs[0].Commit != "commit-v1" || runs[0].Digest != d1 || runs[0].StartedAt.IsZero() {
		t.Fatalf("observe: %+v", runs)
	}

	logs, err := dep.Logs(ctx, app, env, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, `hello from v1 greeting=it's $HOME "ok" # fine`) {
		t.Errorf("logs don't show the app output and settings:\n%s", logs)
	}

	if err := dep.Stop(ctx, app, env); err != nil {
		t.Fatal("stop:", err)
	}
	runs, _ = dep.Observe(ctx)
	runs = itestRuns(runs)
	if len(runs) != 1 || runs[0].State != core.StateExited || !strings.HasPrefix(runs[0].Detail, "exited with code") {
		t.Fatalf("after stop: %+v", runs)
	}
	if err := dep.Stop(ctx, "nothing", "dev"); err != nil {
		t.Errorf("stopping an unknown environment should do nothing: %v", err)
	}

	// Redeploy a new release over the stopped one, then the same one again.
	if err := dep.Deploy(ctx, spec("v2", d2)); err != nil {
		t.Fatal("deploy v2:", err)
	}
	waitBody(t, port, "v2")
	if err := dep.Deploy(ctx, spec("v2", d2)); err != nil {
		t.Fatal("repeat deploy v2:", err)
	}
	runs, _ = dep.Observe(ctx)
	runs = itestRuns(runs)
	if len(runs) != 1 || runs[0].State != core.StateRunning || runs[0].Digest != d2 {
		t.Fatalf("after redeploy: %+v", runs)
	}

	// Stopping only the app leaves the service running, and deploying again doesn't touch it.
	sideID, state := sideContainer(t, project(app, env))
	if state != "running" {
		t.Fatalf("service is %s", state)
	}
	if err := dep.StopApp(ctx, app, env); err != nil {
		t.Fatal("stop app:", err)
	}
	if _, state := sideContainer(t, project(app, env)); state != "running" {
		t.Errorf("StopApp stopped the service: %s", state)
	}
	if runs, _ = dep.Observe(ctx); itestRuns(runs)[0].State != core.StateExited {
		t.Errorf("with the app stopped the environment should be exited: %+v", runs)
	}
	if err := dep.Deploy(ctx, spec("v2", d2)); err != nil {
		t.Fatal("deploy after StopApp:", err)
	}
	waitBody(t, port, "v2")
	if id, state := sideContainer(t, project(app, env)); id != sideID || state != "running" {
		t.Errorf("the service was replaced by a deploy: %s %s", id, state)
	}

	// Old images: v1 is no longer used and goes; v2 (kept) and the service image stay.
	if err := dep.RemoveOldImages(ctx, testImage, []string{d2}); err != nil {
		t.Fatal("remove old images:", err)
	}
	for ref, want := range map[string]bool{testImage + "@" + d1: false, testImage + "@" + d2: true, side: true} {
		if have, err := dep.api.hasImage(ctx, ref); err != nil || have != want {
			t.Errorf("image %s here = %v (%v), want %v", ref, have, err, want)
		}
	}

	// Data folders are copied, moved and removed with owners kept.
	if err := dep.CheckFiles(ctx); err != nil {
		t.Fatal("check files:", err)
	}
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(src, "private", "secret")
	if err := os.WriteFile(secret, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(secret, 12345, 12345); err != nil {
		t.Logf("not root, so owners can't be set: %v", err)
	}
	cp := filepath.Join(t.TempDir(), "copy")
	if err := dep.Copy(ctx, src, cp); err != nil {
		t.Fatal("copy:", err)
	}
	if err := dep.Copy(ctx, src, cp); err == nil {
		t.Error("copying onto an existing folder should fail")
	}
	moved := cp + "-moved"
	if err := dep.Move(ctx, cp, moved); err != nil {
		t.Fatal("move:", err)
	}
	got := filepath.Join(moved, "private", "secret")
	if b, err := os.ReadFile(got); err != nil || string(b) != "s3cret" {
		t.Fatalf("copied file: %q %v", b, err)
	}
	a, b := fileOwner(t, secret), fileOwner(t, got)
	if a != b {
		t.Errorf("owner not kept: %d became %d", a, b)
	}
	if err := dep.Remove(ctx, moved); err != nil {
		t.Fatal("remove:", err)
	}
	if _, err := os.Stat(moved); !os.IsNotExist(err) {
		t.Errorf("folder still there: %v", err)
	}

	// A failing deploy explains itself.
	bad := spec("v2", d2)
	bad.Vars = map[string]string{"X": "a\nb"}
	if err := dep.Deploy(ctx, bad); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Errorf("newline value: %v", err)
	}
	if err := dep.Stop(ctx, app, env); err != nil {
		t.Fatal(err)
	}
}

// psql runs SQL in the environment's database container and returns what it prints.
func psql(t *testing.T, dep *Deployer, app, env, sql string) string {
	t.Helper()
	var out strings.Builder
	err := dep.dbExec(context.Background(), app, env, strings.NewReader(sql), &out, "psql", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1", "-tA")
	if err != nil {
		t.Fatalf("psql: %v", err)
	}
	return strings.TrimSpace(out.String())
}

// A real postgres next to the app: it starts before the app, listens on 127.0.0.1 only, and
// a dump taken while it runs can be put back after the data changed.
func TestIntegrationPostgres(t *testing.T) {
	if os.Getenv("HOMELAB_DOCKER_TESTS") != "1" {
		t.Skip("set HOMELAB_DOCKER_TESTS=1 to run against real Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	startRegistry(t)
	digest := buildAndPush(t, "v1")
	t.Cleanup(func() { exec.Command("docker", "rmi", "-f", testImage+"@"+digest).Run() })

	dep, err := New(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const app, env = "itest", "pg"
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", project(app, env), "down", "-v", "--remove-orphans").Run()
	})
	// Postgres makes the files in its folder its own, so the helper removes them again.
	dataDir, err := os.MkdirTemp("", "homelab-itest-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dep.Remove(context.Background(), dataDir) })

	port, dbPort := freePort(t), freePort(t)
	spec := core.EnvSpec{
		App: app, Env: env,
		Release:  core.Release{Commit: "commit-v1", Image: testImage, Tag: "v1", Digest: digest},
		Port:     8080,
		HostPort: port,
		Memory:   "64m",
		Secrets: map[string]string{
			core.DatabasePassword: "itest-password-1",
			core.DatabaseURL:      "postgres://app:itest-password-1@db:5432/app",
		},
		Postgres:  &core.Postgres{Port: dbPort, DataDir: dataDir},
		OpenPorts: true, // the database stays on 127.0.0.1 anyway
	}
	// Prepare downloads the database image too.
	if err := dep.Prepare(ctx, spec); err != nil {
		t.Fatal("prepare:", err)
	}
	if have, err := dep.api.hasImage(ctx, core.DatabaseImage); err != nil || !have {
		t.Fatalf("the database image should be here after Prepare: %v %v", have, err)
	}
	if err := dep.Deploy(ctx, spec); err != nil {
		t.Fatal("deploy:", err)
	}
	waitBody(t, port, "v1")

	// Both containers belong to the environment, and the app waited for the database.
	runs, err := dep.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if runs = itestRuns(runs); len(runs) != 1 || runs[0].State != core.StateRunning || runs[0].Incomplete {
		t.Fatalf("observe: %+v", runs)
	}
	id := sh(t, "", "ps", "-q", "--filter", "label=com.docker.compose.project="+project(app, env), "--filter", "label=homelab.service=db")
	if id == "" {
		t.Fatal("no database container")
	}
	if bound := sh(t, "", "port", id, "5432/tcp"); !strings.Contains(bound, fmt.Sprintf("127.0.0.1:%d", dbPort)) || strings.Contains(bound, "0.0.0.0") {
		t.Errorf("the database is published as %q, want 127.0.0.1 only", bound)
	}
	if got := sh(t, "", "inspect", "-f", "{{.HostConfig.Memory}}", id); got != "268435456" {
		t.Errorf("database memory = %s", got)
	}

	psql(t, dep, app, env, "create table notes (id int); insert into notes values (1), (2);")
	file := filepath.Join(t.TempDir(), "homelab-db.dump")
	if err := dep.DumpDatabase(ctx, app, env, file); err != nil {
		t.Fatal("dump:", err)
	}
	if b, err := os.ReadFile(file); err != nil || !strings.HasPrefix(string(b), "PGDMP") {
		t.Fatalf("the dump isn't a custom-format dump: %v", err)
	}
	if err := dep.DumpDatabase(ctx, app, env, file); err == nil {
		t.Error("dumping onto an existing file should fail")
	}
	if left, _ := filepath.Glob(file + ".tmp"); len(left) != 0 {
		t.Errorf("left over: %v", left)
	}

	// Change the data, then put the dump back: only what the dump holds is left.
	psql(t, dep, app, env, "insert into notes values (3); create table later (x int); create schema extra; create table extra.t (y int);")
	if err := dep.StopApp(ctx, app, env); err != nil {
		t.Fatal("stop app:", err)
	}
	if err := dep.RestoreDatabase(ctx, app, env, file); err != nil {
		t.Fatal("restore:", err)
	}
	if got := psql(t, dep, app, env, "select count(*) from notes"); got != "2" {
		t.Errorf("notes has %s rows after the restore, want 2", got)
	}
	if got := psql(t, dep, app, env, "select coalesce(to_regclass('later')::text, '') || coalesce(to_regclass('extra.t')::text, '')"); got != "" {
		t.Errorf("what the dump doesn't hold is still there: %q", got)
	}
	if err := dep.StartApp(ctx, app, env); err != nil {
		t.Fatal("start app:", err)
	}
	waitBody(t, port, "v1")
	if runs, _ = dep.Observe(ctx); itestRuns(runs)[0].State != core.StateRunning {
		t.Errorf("after StartApp: %+v", runs)
	}

	// With the environment stopped there is no database to dump.
	if err := dep.Stop(ctx, app, env); err != nil {
		t.Fatal(err)
	}
	if err := dep.DumpDatabase(ctx, app, env, file+"2"); err == nil || !strings.Contains(err.Error(), "no running database") {
		t.Errorf("dump of a stopped database: %v", err)
	}
}
