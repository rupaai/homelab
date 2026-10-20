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

// Promotion modes (C5): manual (people deploy and promote), auto-dev (new builds go to dev
// by themselves) and auto (dev is also promoted once it has been healthy for a while).
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
	Manifest []byte    `json:"-"`        // homelab.yaml at this commit
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
	HostPort int    // published on 127.0.0.1 only; homelabd forwards the address to it
	Memory   string // e.g. "2g"; empty means no limit
	Mounts   []Mount
	Vars     map[string]string // settings and secrets, as environment variables
	Auth     RegistryAuth
	HostIP   string // if set, the container reaches this address as host.docker.internal (the app door)
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
	Login    string // e.g. labiba@example.com; empty when unknown
	Name     string // display name
	Local    bool   // connected from this machine (127.0.0.1)
	Network  bool   // came over the plain network (addresses: ip); signs in with the password
	Tagged   bool   // a tagged Tailscale node (a server, not a person)
	PersonID string
}

// Person is one household account. Roles and app grants are independent sets.
type Person struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Email      string            `json:"email,omitempty"`
	IdentityID string            `json:"identityId,omitempty"`
	Status     string            `json:"status"`
	Roles      []string          `json:"roles"`
	Grants     map[string]string `json:"grants"`
	LastSignIn *time.Time        `json:"lastSignIn,omitempty"`
	// Remote access (milestone 4).
	TailscaleLogin  string     `json:"tailscaleLogin,omitempty"`
	RemoteInviteID  string     `json:"-"`
	RemoteInviteURL string     `json:"remoteInviteUrl,omitempty"`
	ConnectedAt     *time.Time `json:"connectedAt,omitempty"`
}

// IdentityUser is a person observed at the configured identity provider.
type IdentityUser struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Groups     []string   `json:"groups"`
	Disabled   bool       `json:"disabled"`
	LastSignIn *time.Time `json:"lastSignIn,omitempty"`
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

// Actor for things Homelab does by itself.
const ActorHomelab = "homelab"

// RemoteInvite is a pending invitation to join the tailnet.
type RemoteInvite struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// AccessPolicy is the tailnet's rules, in Homelab's terms. Groups map a group name (without
// the "group:" prefix) to logins; Grants say which groups reach which tags.
type AccessPolicy struct {
	Groups    map[string][]string `json:"groups"`
	TagOwners []string            `json:"tagOwners"` // every tag Homelab uses
	Grants    []AccessGrant       `json:"grants"`
}

// AccessGrant lets the members of Groups reach every node carrying one of Tags.
type AccessGrant struct {
	Groups []string `json:"groups"`
	Tags   []string `json:"tags"`
}

// RemoteState is what the tailnet looks like now.
type RemoteState struct {
	Users   []RemoteUser   `json:"users"`
	Devices []RemoteDevice `json:"devices"`
	Invites []RemoteInvite `json:"invites"`
}

// RemoteUser is a person on the tailnet.
type RemoteUser struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// RemoteDevice is one device on the tailnet.
type RemoteDevice struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Login    string     `json:"login"` // owner; empty for tagged devices
	Tags     []string   `json:"tags"`
	LastSeen *time.Time `json:"lastSeen,omitempty"`
	Online   bool       `json:"online"`
}

// ErrGone means a push subscription no longer exists at the push service.
var ErrGone = errors.New("push subscription is gone")

// PushSubscription is one device's web push endpoint and keys, as the browser gave them.
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Notification is what a push shows. URL is the companion path it opens.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag,omitempty"` // a newer notification with the same tag replaces the older one
}

// DeviceUsage is the computer's current load.
type DeviceUsage struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsed    uint64  `json:"memUsed"`
	MemTotal   uint64  `json:"memTotal"`
	DiskUsed   uint64  `json:"diskUsed"`
	DiskTotal  uint64  `json:"diskTotal"`
}

// AppUsage is one app environment's current load.
type AppUsage struct {
	App        string  `json:"app"`
	Env        string  `json:"env"`
	CPUPercent float64 `json:"cpuPercent"`
	MemUsed    uint64  `json:"memUsed"`
}

// SystemInfo is what Homelab knows about the device (milestone 6).
type SystemInfo struct {
	Hostname        string     `json:"hostname,omitempty"`
	OS              string     `json:"os,omitempty"` // e.g. "Ubuntu 24.04.4 LTS"
	Kernel          string     `json:"kernel,omitempty"`
	UptimeSeconds   uint64     `json:"uptimeSeconds,omitempty"`
	TemperatureC    float64    `json:"temperatureC,omitempty"` // hottest sensor; 0 when unknown
	Disks           []DiskInfo `json:"disks"`
	RebootRequired  bool       `json:"rebootRequired"`
	UpdatesPending  int        `json:"updatesPending"`       // -1 when unknown
	SecurityPending int        `json:"securityPending"`      // -1 when unknown
	ReportedAt      *time.Time `json:"reportedAt,omitempty"` // when the root report last ran; nil if never
}

// DiskInfo is one mounted disk.
type DiskInfo struct {
	Mount  string `json:"mount"`
	Device string `json:"device"`
	Used   uint64 `json:"used"`
	Total  uint64 `json:"total"`
	// Health is "ok", "failing" or "" when unknown (needs the root report and smartctl).
	Health string `json:"health,omitempty"`
}

// UpdateStatus is Homelab's own version and the newest one available (milestone 6).
type UpdateStatus struct {
	Managed   bool       `json:"managed"` // installed by `homelabd setup`, so it can update itself
	Current   string     `json:"current"`
	Latest    string     `json:"latest,omitempty"`
	Available bool       `json:"available"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// LastResult says how the last update went, e.g. "Updated to build-12" or "build-12
	// didn't start, so Homelab went back to build-11".
	LastResult string `json:"lastResult,omitempty"`
	Note       string `json:"note,omitempty"`
	// Testing is set while a test version (a pull request's build) runs.
	Testing *TestRun `json:"testing,omitempty"`
}

// TestVersion is a build of an open Homelab pull request that can be tried on the device.
type TestVersion struct {
	Version string `json:"version"` // such as pr-12-1a2b3c4
	PR      int    `json:"pr"`
	Title   string `json:"title"`
}

// TestRun is a test version running now. Homelab saved its own data before switching, so
// going back puts both the program and that data back as they were.
type TestRun struct {
	TestVersion
	From string    `json:"from"` // the version to go back to
	At   time.Time `json:"at"`
}
