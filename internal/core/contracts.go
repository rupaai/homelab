// Package core holds the types every block shares and the contracts (interfaces) between
// homehubd's core and its adapters. The core only ever talks to these interfaces, so any
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
	// Observe reports every environment Homehub runs, including stopped ones.
	Observe(ctx context.Context) ([]Running, error)
	// Logs returns the last lines of an environment's logs, oldest first.
	Logs(ctx context.Context, app, env string, lines int) (string, error)
}

// Addresses gives each app environment (and the companion) its own name people can open
// (Tailscale nodes now; a domain through Caddy later).
type Addresses interface {
	// Ensure serves handler at name ("movies", "movies-dev", "homehub"). Calling it again
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

// Secrets hands out secret values. Milestone 1 reads files on the Mac; the encrypted
// store replaces it in milestone 2 behind this same contract.
type Secrets interface {
	// Missing lists which of names have no value for app/env. Never returns values.
	Missing(app, env string, names []string) ([]string, error)
	// Inject returns the values for app/env, for the deploy adapter only.
	Inject(app, env string, names []string) (map[string]string, error)
	// Platform returns a Homehub credential such as GITHUB_TOKEN or TS_AUTHKEY.
	Platform(name string) (string, bool)
}

// Clock lets tests control time.
type Clock interface{ Now() time.Time }
