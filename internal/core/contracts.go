// Package core holds the types every block shares and the contracts (interfaces) between
// homelabd's core and its adapters. The core only ever talks to these interfaces, so any
// adapter (GitHub, Docker Compose, Tailscale...) can be swapped without touching the rest.
//
// Rules for every adapter:
//   - Methods are idempotent: calling them twice with the same input is safe.
//   - Methods never block forever: they honour ctx.
//   - Adapters keep no desired state of their own. The core stores it and the reconciler
//     calls the adapter again until reality matches.
package core

import (
	"context"
	"net/http"
	"time"
)

// Source is where an app's code lives (GitHub now; Forgejo or any Git host later).
type Source interface {
	// ReadFile returns a file from the repo at a branch, tag or commit.
	// It returns ErrNotFound when the file doesn't exist.
	ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error)
	// Commit describes one commit.
	Commit(ctx context.Context, repo, sha string) (Commit, error)
	// Compare lists the commits in head that aren't in base, oldest first.
	Compare(ctx context.Context, repo, base, head string) ([]Commit, error)
	// WebURL links to a commit (head only) or a comparison (base and head) for people.
	WebURL(repo, base, head string) string
}

// Builder knows which commits have finished, passing builds (GitHub Actions now).
type Builder interface {
	// LatestBuild returns the newest finished build of the branch. Status is BuildPassed
	// only when the app's checks all passed. Returns ErrNotFound when there is none yet.
	LatestBuild(ctx context.Context, repo, branch, workflow string) (Build, error)
}

// Registry stores the images builds produce (ghcr.io now; a registry on the device later).
type Registry interface {
	// Resolve returns the content digest ("sha256:...") of image:tag, or ErrNotFound.
	Resolve(ctx context.Context, image, tag string) (string, error)
	// PullAuth returns the credentials needed to pull image. Empty means anonymous.
	PullAuth(ctx context.Context, image string) (RegistryAuth, error)
}

// Deployer runs app environments (Docker Compose now).
type Deployer interface {
	// Prepare downloads everything spec needs (the image) without touching what runs, so
	// the switch-over that follows is short.
	Prepare(ctx context.Context, spec EnvSpec) error
	// Deploy makes the environment run exactly spec, replacing what ran before.
	// It returns once the containers are started, not once they are healthy.
	Deploy(ctx context.Context, spec EnvSpec) error
	// Stop stops an environment's containers and keeps its data.
	Stop(ctx context.Context, app, env string) error
	// Observe reports every environment Homelab runs, including stopped ones.
	Observe(ctx context.Context) ([]Running, error)
	// Logs returns the last lines of an environment's logs, oldest first.
	Logs(ctx context.Context, app, env string, lines int) (string, error)
}

// Addresses gives each app environment (and the companion) its own name people can open
// (Tailscale nodes now; a domain through Caddy later).
type Addresses interface {
	// Ensure serves handler at name ("movies", "movies-dev", "homelab"). Calling it again
	// with the same name swaps the handler in place. It returns without waiting for the
	// address to be ready; Observe says when it is.
	//
	// The adapter puts who is connecting into each request's context (see WithViewer).
	Ensure(ctx context.Context, name string, handler http.Handler) error
	// Remove stops serving name.
	Remove(ctx context.Context, name string) error
	// Observe reports every address and whether it's ready.
	Observe() map[string]Address
	// Close stops everything; used on shutdown.
	Close() error
}

// HealthChecker probes an app's health endpoint.
type HealthChecker interface {
	Check(ctx context.Context, url string) HealthResult
}

// Secrets keeps secret values (S1-S9): an encrypted store inside Homelab. App secrets are
// scoped to one app environment; platform credentials (GITHUB_TOKEN, TS_AUTHKEY) use app "".
// Values are never returned to people, only to adapters that need them (Inject, Platform).
type Secrets interface {
	// Missing lists which of names have no value for app/env. Never returns values.
	Missing(app, env string, names []string) ([]string, error)
	// Inject returns the values for app/env, for the deploy adapter only.
	Inject(app, env string, names []string) (map[string]string, error)
	// Platform returns a Homelab credential such as GITHUB_TOKEN or TS_AUTHKEY.
	Platform(name string) (string, bool)
	// Set stores (or replaces) a value. app "" means a platform credential; env is then "".
	Set(app, env, name, value, by string) error
	// Delete removes a value. Deleting one that isn't set is not an error.
	Delete(app, env, name string) error
	// List describes what is stored for app/env (app "" for platform), never the values.
	List(app, env string) ([]SecretInfo, error)
	// DeleteApp removes every value of an app, in every environment.
	DeleteApp(app string) error
}

// SecretInfo describes one stored secret without its value.
type SecretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// SmokeTest is one run of an app's own smoke test (C6) against one of its environments.
// The test runs inside the release's image, never on the host, and can only reach that
// environment, at HOMELAB_TARGET_URL.
type SmokeTest struct {
	App     string
	Env     string // the environment it tests (dev)
	Release Release
	Command []string // from homelab.yaml smoke_test
	Port    int      // the port the app listens on inside its container
	Auth    RegistryAuth
	Timeout time.Duration
}

// SmokeTester runs smoke tests (Docker now).
type SmokeTester interface {
	// Smoke runs the test. passed is false when the test ran and failed; err is for when it
	// couldn't run at all. output is the end of what it printed, for people.
	Smoke(ctx context.Context, t SmokeTest) (passed bool, output string, err error)
}

// Mirror keeps a full, read-only copy of each app's repo on this computer (G2, G3).
type Mirror interface {
	// Sync clones repo the first time and fetches every branch and tag after that.
	Sync(ctx context.Context, repo string) error
	// Status says when repo was last synced and what went wrong last time, if anything.
	Status(repo string) MirrorStatus
}

// MirrorStatus is the state of one repo's mirror.
type MirrorStatus struct {
	Path     string     `json:"path"`               // where the mirror lives on disk
	LastSync *time.Time `json:"lastSync,omitempty"` // last successful sync
	Error    string     `json:"error,omitempty"`    // the last sync's error, "" if it worked
}

// Clock lets tests control time.
type Clock interface{ Now() time.Time }

// Identity mirrors Homelab people to the sign-in provider.
type Identity interface {
	Ensure(ctx context.Context, person Person, groups []string) (string, error)
	Disable(ctx context.Context, personID string) error
	LoginLink(ctx context.Context, personID string, ttl time.Duration) (string, error)
	Observe(ctx context.Context) ([]IdentityUser, error)
}

// Email sends optional household messages. The default implementation is a no-op.
type Email interface {
	Send(ctx context.Context, to, subject, body string) error
}

// RemoteAccess manages who can reach Homelab from anywhere (R1-R3): people on the tailnet,
// their devices, the access rules, and keys for Homelab's own addresses.
type RemoteAccess interface {
	// Invite returns a link that lets the person join the tailnet.
	Invite(ctx context.Context, email string) (RemoteInvite, error)
	// CancelInvite withdraws an invite that hasn't been accepted.
	CancelInvite(ctx context.Context, inviteID string) error
	// SetPolicy replaces the tailnet's access rules.
	SetPolicy(ctx context.Context, p AccessPolicy) error
	// AuthKey mints a single-use key for one of Homelab's own addresses, with these tags.
	AuthKey(ctx context.Context, tags []string) (string, error)
	// Observe lists the tailnet's people and devices.
	Observe(ctx context.Context) (RemoteState, error)
	// RemoveDevices deletes every device that belongs to login.
	RemoveDevices(ctx context.Context, login string) (int, error)
	// RemoveUser takes login off the tailnet.
	RemoveUser(ctx context.Context, login string) error
}

// Notifier delivers push notifications to people's devices (milestone 5, O4).
type Notifier interface {
	// Send delivers n to one subscribed device. ErrGone means the device unsubscribed and its
	// subscription should be forgotten.
	Send(ctx context.Context, sub PushSubscription, n Notification) error
}

// Resources reports what the device and the apps use right now (O6). No history is kept.
type Resources interface {
	// Device reports the computer's CPU, memory and the disk holding dataDir.
	Device(ctx context.Context, dataDir string) (DeviceUsage, error)
	// Apps reports each running app environment's CPU and memory.
	Apps(ctx context.Context) ([]AppUsage, error)
}
