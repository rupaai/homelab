package docker

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIntegrationAppDoor checks the app door's path on Linux: a container on its own
// network reaches a server listening on Docker's bridge address as host.docker.internal.
func TestIntegrationAppDoor(t *testing.T) {
	if os.Getenv("HOMELAB_DOCKER_TESTS") != "1" {
		t.Skip("set HOMELAB_DOCKER_TESTS=1 to run against real Docker")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the bridge address is only on this computer on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := New(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	gw, err := d.BridgeGateway(ctx)
	if err != nil || net.ParseIP(gw).To4() == nil {
		t.Fatalf("bridge gateway: %q %v", gw, err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(gw, "0"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte("ok"))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	const network = "homelab-itest-door"
	exec.Command("docker", "network", "rm", network).Run()
	sh(t, "", "network", "create", network)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })
	out := sh(t, "", "run", "--rm", "--network", network, "--add-host", "host.docker.internal:"+gw,
		"mirror.gcr.io/library/busybox:1.37", "wget", "-qO-", "--header", "Authorization: Bearer test",
		"http://host.docker.internal:"+port+"/")
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("container got %q", out)
	}
}
