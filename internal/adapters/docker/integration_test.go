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
	"testing"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Set HOMEHUB_DOCKER_TESTS=1 to run this against real Docker. It starts a throwaway
// registry on 127.0.0.1:5000, pushes two tiny images and removes everything afterwards.
const (
	testRegistry = "127.0.0.1:5000"
	testImage    = testRegistry + "/homehub-itest/web"
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

func TestIntegrationFullCycle(t *testing.T) {
	if os.Getenv("HOMEHUB_DOCKER_TESTS") != "1" {
		t.Skip("set HOMEHUB_DOCKER_TESTS=1 to run against real Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	exec.Command("docker", "rm", "-f", "homehub-itest-registry").Run()
	sh(t, "", "run", "-d", "--name", "homehub-itest-registry", "-p", testRegistry+":5000", "mirror.gcr.io/library/registry:2")
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "-v", "homehub-itest-registry").Run()
	})
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if resp, err := http.Get("http://" + testRegistry + "/v2/"); err == nil {
			resp.Body.Close()
			break
		}
	}
	d1, d2 := buildAndPush(t, "v1"), buildAndPush(t, "v2")
	t.Cleanup(func() {
		exec.Command("docker", "rmi", "-f", testImage+"@"+d1, testImage+"@"+d2).Run()
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
	if len(runs) != 1 || runs[0].State != core.StateRunning || runs[0].Digest != d2 {
		t.Fatalf("after redeploy: %+v", runs)
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
