package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

const serviceDoc = manifestDoc + `
services:
  - name: jellyfin
    image: jellyfin/jellyfin:10.11.11
    data: [{name: jellyfin-config, path: /config}]
`

// promoteWithService puts aaaaaaa1 on prod, with a service whose config holds data.
func (r *rig) promoteWithService() (appFile, serviceFile string) {
	ctx := context.Background()
	r.src.Push(repo, "main", "aaaaaaa1", "first", []byte(serviceDoc))
	if _, err := r.hub.AddApp(ctx, repo, "", owner); err != nil {
		r.t.Fatal(err)
	}
	r.sec.Set("movies", core.Prod, "TMDB_API_KEY", "k", "t")
	r.pushWith("aaaaaaa1", serviceDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		r.t.Fatal(err)
	}
	r.settle()
	data := filepath.Join(r.dir, "apps", "movies", "prod", "data")
	appFile, serviceFile = filepath.Join(data, "db", "catalogue.db"), filepath.Join(data, "jellyfin-config", "library.db")
	for f, body := range map[string]string{appFile: "v1 data", serviceFile: "jellyfin"} {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	return appFile, serviceFile
}

// A promote stops only the app, copies only the app's own data, and a failed promote puts
// back only that, so the services keep running and keep their files.
func TestPromoteLeavesServicesAlone(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	appFile, serviceFile := r.promoteWithService()
	r.dep.Calls = nil
	healthy := true
	r.health.SetFailWhen(func(url string) bool {
		if strings.HasSuffix(url, ":17011/healthz") && r.running(core.Prod) == "bbbbbbb2" && !healthy {
			_ = os.WriteFile(appFile, []byte("changed by bbbbbbb2"), 0o644)
			_ = os.WriteFile(serviceFile, []byte("jellyfin, later"), 0o644)
			return true
		}
		return false
	})
	healthy = false
	r.pushWith("bbbbbbb2", serviceDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if slices.Contains(r.dep.Calls, "stop movies/prod") || !slices.Contains(r.dep.Calls, "stop-app movies/prod") {
		t.Fatalf("a promote should stop only the app: %v", r.dep.Calls)
	}
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q after the failed promote", got)
	}
	if b, _ := os.ReadFile(appFile); string(b) != "v1 data" {
		t.Errorf("app data after going back: %q", b)
	}
	if b, _ := os.ReadFile(serviceFile); string(b) != "jellyfin, later" {
		t.Errorf("the service's files must not be put back: %q", b)
	}
	backups, _ := filepath.Glob(filepath.Join(r.dir, "backups", "movies", "prod", "*", "*"))
	for _, b := range backups {
		if strings.HasSuffix(b, "jellyfin-config") {
			t.Errorf("service data was copied: %s", b)
		}
	}
}

// If the copy can't work, nothing is stopped and prod keeps running.
func TestPromoteChecksCopyBeforeStopping(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.promoteWithService()
	r.pushWith("bbbbbbb2", serviceDoc)
	r.hub.CheckBuilds(ctx)
	r.settle()
	r.dep.Calls = nil
	r.dep.FilesErr = errors.New("no helper")
	if err := r.hub.Promote(ctx, "movies", owner); err != nil {
		t.Fatal(err)
	}
	r.settle()
	for _, c := range r.dep.Calls {
		if strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "deploy") {
			t.Fatalf("nothing should change when the copy can't work: %v", r.dep.Calls)
		}
	}
	if got := r.running(core.Prod); got != "aaaaaaa1" {
		t.Fatalf("prod runs %q", got)
	}
	if evs := r.events("promote.failed"); len(evs) != 1 || !strings.Contains(evs[0], "Nothing changed") {
		t.Fatalf("promote.failed events: %v", evs)
	}
}

// After a deploy, old images of the app are removed except the versions still needed.
func TestDeployRemovesOldImages(t *testing.T) {
	r := newRig(t)
	r.promoteBoth("aaaaaaa1", "bbbbbbb2")
	var last string
	for _, c := range r.dep.Calls {
		if strings.HasPrefix(c, "remove-old-images ") {
			last = c
		}
	}
	want := "remove-old-images ghcr.io/rupaai/movies keep=sha256:aaaaaaa1,sha256:bbbbbbb2"
	if last != want {
		t.Fatalf("last image cleanup:\n got %q\nwant %q", last, want)
	}
}
