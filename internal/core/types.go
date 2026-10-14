package core

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by adapters when the thing asked for doesn't exist (yet).
var ErrNotFound = errors.New("not found")

// Environments. Milestone 1 has exactly these two.
const (
	Dev  = "dev"
	Prod = "prod"
)

// Promotion modes (C5). Milestone 1 implements manual and auto-dev; auto arrives in milestone 2.
const (
	PromotionManual  = "manual"
	PromotionAutoDev = "auto-dev"
	PromotionAuto    = "auto"
)

// Commit is one commit in an app's repo.
type Commit struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"` // first line only
	Author  string    `json:"author"`
	When    time.Time `json:"when"`
	URL     string    `json:"url"`
}

// Build statuses.
const (
	BuildPassed  = "passed"
	BuildFailed  = "failed"
	BuildRunning = "running"
)

// Build is one finished or running build of a commit.
type Build struct {
	Commit string `json:"commit"`
	Status string `json:"status"`
	URL    string `json:"url"` // link to the build's logs
}

// RegistryAuth is a username and password (or token) for an image registry.
type RegistryAuth struct {
	Server   string
	Username string
	Password string
}

// Release is one exact build of an app: a commit, the image built from it, and the
// manifest at that commit. Promote copies a release from dev to prod unchanged (D4).
type Release struct {
	Commit   string    `json:"commit"`
	Image    string    `json:"image"`  // e.g. ghcr.io/rupaai/smart-recommender-player
	Tag      string    `json:"tag"`    // e.g. sha-<commit>
	Digest   string    `json:"digest"` // sha256:...; the image is always run by digest
	Message  string    `json:"message"`
	Author   string    `json:"author"`
	When     time.Time `json:"when"`     // commit time
	BuildURL string    `json:"buildUrl"` // where the build's logs are
	Manifest []byte    `json:"-"`        // homehub.yaml at this commit
}

// ImageRef is how the image is pulled and run: by digest when known.
func (r Release) ImageRef() string {
	if r.Digest != "" {
		return r.Image + "@" + r.Digest
	}
	return r.Image + ":" + r.Tag
}

// Short is the first 7 characters of the commit, for people.
func (r Release) Short() string {
	if len(r.Commit) > 7 {
		return r.Commit[:7]
	}
	return r.Commit
}

// Mount is a host folder made available inside the app's containers.
type Mount struct {
	HostPath      string
	ContainerPath string
}

// EnvSpec is everything the deploy adapter needs to run one app environment.
type EnvSpec struct {
	App      string
	Env      string
	Release  Release
	Port     int    // the port the app listens on inside its container
	HostPort int    // published on 127.0.0.1 only; homehubd forwards the address to it
	Memory   string // e.g. "2g"; empty means no limit
	Mounts   []Mount
	Vars     map[string]string // settings and secrets, as environment variables
	Auth     RegistryAuth
}

// Container states as the deploy adapter reports them.
const (
	StateRunning    = "running"
	StateStarting   = "starting"
	StateExited     = "exited"
	StateRestarting = "restarting"
)

// Running is what the deploy adapter actually finds for one environment.
type Running struct {
	App       string    `json:"app"`
	Env       string    `json:"env"`
	Commit    string    `json:"commit"`
	Digest    string    `json:"digest"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
	Detail    string    `json:"detail"` // e.g. "exited with code 1"
}

// Address is one name served by the Addresses adapter.
type Address struct {
	Name  string `json:"name"`
	URL   string `json:"url"` // empty until known
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}

// Health statuses (O1).
const (
	Healthy  = "healthy"
	Degraded = "degraded" // running, but the health check fails
	Down     = "down"     // not running
	Unknown  = "unknown"  // not checked yet
)

// HealthResult is one health probe.
type HealthResult struct {
	OK      bool
	Code    int
	Detail  string
	Latency time.Duration
}

// Viewer is the person (or device) behind a request, as the Addresses adapter saw it.
type Viewer struct {
	Login  string // e.g. labiba@example.com; empty when unknown
	Name   string // display name
	Local  bool   // connected from this machine (127.0.0.1)
	Tagged bool   // a tagged Tailscale node (a server, not a person)
}

// Label is how the viewer appears in the activity log.
func (v Viewer) Label() string {
	switch {
	case v.Login != "":
		return v.Login
	case v.Local:
		return "owner (on this computer)"
	default:
		return "unknown"
	}
}

type viewerKey struct{}

// WithViewer returns ctx carrying v.
func WithViewer(ctx context.Context, v Viewer) context.Context {
	return context.WithValue(ctx, viewerKey{}, v)
}

// ViewerFrom returns the viewer stored by WithViewer.
func ViewerFrom(ctx context.Context) (Viewer, bool) {
	v, ok := ctx.Value(viewerKey{}).(Viewer)
	return v, ok
}

// Actor for things Homehub does by itself.
const ActorHomehub = "homehub"
