# How Homelab works

For current state and open work, read [HANDOVER.md](../HANDOVER.md). This page does not repeat status. It explains the parts.

There are three levels. Read as far as you need.

- Level 1: one sentence per part.
- Level 2: one short paragraph per part, and how parts connect.
- Level 3: where the code is, and how to test it.

## Level 1, in one minute

- **Daemon (`homelabd`)**: the one program that runs Homelab.
- **Apps and environments**: an app is a repo that runs in two copies, dev and prod.
- **Deploys from GitHub**: a push builds an image, and Homelab runs that exact build.
- **Companion web app**: the website where you manage everything.
- **Users and sign-in**: people sign in with a passkey or a password, and get access per app.
- **Secrets**: passwords and keys, kept encrypted and shown only to allowed people.
- **Files**: apps and admins store files in folders on the device's disks.
- **Databases**: one line in `homelab.yaml` gives each environment its own Postgres or SQLite.
- **Plugins**: small libraries so app code can use Homelab in one line.
- **Self-update**: Homelab on the device updates itself from GitHub releases.
- **Alerts and resources**: push notifications and a screen for CPU, memory and disks.
- **Services program (`homelab-services`)**: a second program, in Rust, that does the files, databases and secrets work.

## Level 2, the parts and how they connect

**Daemon.** `homelabd` is one Go program. It keeps the wanted state (which apps exist, which version runs where) in a SQLite file. A loop called the reconciler (it compares wanted state with what is really running) makes Docker and GitHub match. It also serves the companion and the API. The core logic (the "hub") only talks to small interfaces. Each outside system sits behind an adapter, which keeps the core easy to test with fakes.

**Apps and environments.** An app is a GitHub repo with a Dockerfile and a `homelab.yaml`. Each app has two environments: dev and prod. Each has its own containers, private network, data and secrets. Promoting copies the exact build from dev to prod. Prod data is copied first. If the new version does not stay healthy, Homelab rolls back on its own. Auto-promote waits until dev has been healthy for a set time, and can run an app's smoke test first.

**Deploys from GitHub.** The app's CI pushes an image tagged with the commit. Homelab watches the repo, checks the build, and deploys with Docker Compose. It also keeps a full mirror of each app's code.

**Companion web app.** A SvelteKit site in `web/`. It is built to static files and embedded in the `homelabd` binary. It talks to the daemon over the JSON API. It shows apps, people, files, databases, activity, resources and settings.

**Users and sign-in.** Homelab has an owner, admins, builders and users. Access is also granted per app. Sign-in uses passkeys through Pocket ID, or a password when Homelab serves other devices on the home network. Apps get signed identity headers so they know who is calling. The owner can use `homelabd recover` to get back in.

**Secrets.** Secret values are stored encrypted. The key is a file, `master.key`. Apps get their secrets as files in `/run/secrets` (and as environment variables unless `secrets_as: files` is set). Only the owner, and people the owner allows, can see values. Apps can also save secrets of their own through the API.

**Files.** Folders belong to an app environment or to the admins. The Rust service stores the bytes on disk and reads photo, video, audio and PDF info. Apps reach it with their app key. Admins reach it in the companion or with admin keys.

**Databases.** `database: postgres` adds a Postgres container next to the app. `database: sqlite` uses a file. Dev and prod never share one. Homelab can copy, restore and import a database. A copy is made before every promote.

**Plugins.** TypeScript, Python and Rust libraries in `plugins/`. They only make web calls to the Homelab API. They use `HOMELAB_API_URL` and `HOMELAB_TOKEN`, which Homelab sets in every app.

**Self-update.** Every merge to `main` makes a numbered GitHub release. On the device, Homelab downloads a newer one, swaps to it, and goes back to the old one if the new one does not stay up. Every PR also gets a test version that can be tried from the companion.

**Alerts and resources.** Homelab sends web push notifications (app down, app back, rolled back, disk nearly full). The Resources screen shows the device (CPU, memory, disks, updates) and each app environment.

**Services program.** `homelabd` starts `homelab-services` next to itself and restarts it if it stops. It listens only on `127.0.0.1`. Nothing calls it directly. `homelabd` checks who is calling, signs the call, and passes it on.

**API keys and the app door.** Apps call Homelab on a separate address (`HOMELAB_API_URL`), using an app key. Admin keys are for servers outside Homelab. See [platform-services.md](platform-services.md).

## Level 3, where the code lives

The commands are in the [Makefile](../Makefile). `make test` runs the Go and Rust checks. It does not run the Docker test, the companion checks or the plugin checks. CI (`.github/workflows/ci.yml`) runs all of them.

| Block | Code | Key files | How to test |
|---|---|---|---|
| Daemon | `cmd/homelabd`, `internal/hub`, `internal/core` | `cmd/homelabd/main.go` (commands), `run.go` (wiring), `internal/hub/hub.go` (reconciler), `internal/core/contracts.go` (interfaces) | `go test ./cmd/... ./internal/hub/...`. Fake adapters are in `internal/adapters/fake` |
| State | `internal/state` | `store.go`, `people.go`, `apikeys.go` | `go test ./internal/state/...` |
| Apps and manifest | `internal/manifest`, `internal/hub` | `manifest/manifest.go` (`homelab.yaml`), `hub/auto.go` (auto-promote), `hub/manage.go` | `go test ./internal/manifest/... ./internal/hub/...` |
| Deploys, Docker | `internal/adapters/docker`, `internal/adapters/github`, `internal/adapters/gitmirror`, `internal/adapters/httphealth` | `docker/compose.go`, `docker/smoke.go`, `github/github.go`, `gitmirror/gitmirror.go` | `go test ./internal/adapters/...`, and `make test-docker` (needs Docker) |
| Companion | `web/`, `internal/companion`, `internal/api` | `web/src/routes/`, `web/src/lib/api.ts`, `web/scripts/embed.mjs`, `internal/api/api.go`, `internal/api/static.go` | `cd web && npm ci && npm run check && npm test`, and `go test ./internal/api/...` |
| Users and sign-in | `internal/identity`, `internal/adapters/pocketid`, `internal/adapters/smtp`, `internal/hub`, `internal/api` | `identity/identity.go`, `hub/access.go`, `hub/people.go`, `api/oidc.go`, `api/password.go`, `api/appauth.go`, `cmd/homelabd/password.go`, `cmd/homelabd/recover.go` | `go test ./internal/identity/... ./internal/hub/... ./internal/api/... ./internal/adapters/pocketid/...` |
| Secrets | `internal/adapters/secretstore`, `internal/adapters/services`, `services/homelab-services/src/secrets/` | `secretstore/secretstore.go`, `adapters/services/secrets.go`, `services/homelab-services/src/secrets_api.rs`, `internal/api/secrets_test.go` | `go test ./internal/adapters/secretstore/... ./internal/adapters/services/... ./internal/api/...`, and `make check-services` |
| Files | `services/homelab-services`, `internal/adapters/services`, `internal/hub`, `web/src/routes/files` | `src/files.rs`, `src/meta.rs`, `src/disk.rs` (all in `services/homelab-services/`), `internal/hub/disks.go`, `docs/files.openapi.yaml` | `make check-services`, and `go test ./internal/adapters/services/...` |
| Databases | `services/homelab-services`, `internal/adapters/docker`, `internal/adapters/services`, `internal/hub` | `src/dbs.rs`, `src/dbs_api.rs`, `src/dbs_sql.rs`, `internal/adapters/docker/database.go`, `internal/adapters/services/databases.go`, `internal/hub/dbcopies.go`, `internal/hub/dbimport.go` | `make check-services`, and `go test ./internal/hub/... ./internal/api/...` |
| Plugins | `plugins/typescript`, `plugins/python`, `plugins/rust` | each folder's `src/` and README | TypeScript: `cd plugins/typescript && npm ci && npm run check && npm test`. Python: `pip install -e "plugins/python[dev]"`, then `pytest plugins/python`. Rust: `cd plugins/rust && cargo test --locked` |
| Self-update and releases | `internal/adapters/selfupdate`, `cmd/homelabd/setup.go`, `scripts/`, `.github/workflows/` | `selfupdate/selfupdate.go`, `selfupdate/test.go` (PR test versions), `scripts/build-release.sh`, `release.yml`, `test-build.yml` | `go test ./internal/adapters/selfupdate/... ./cmd/...` |
| Alerts and resources | `internal/adapters/webpush`, `internal/adapters/sysres`, `internal/adapters/sysinfo`, `internal/hub`, `web/src/service-worker.ts` | `webpush/webpush.go`, `sysres/sysres.go`, `sysinfo/sysinfo.go`, `internal/hub/alerts.go`, `internal/hub/device.go` | `go test ./internal/hub/... ./internal/adapters/sysres/... ./internal/adapters/sysinfo/...` |
| Services program | `services/` (the crate is `services/homelab-services`) | `src/main.rs`, `src/app.rs`, `src/caller.rs` (checks signed calls), `src/config.rs`; `internal/adapters/services/services.go` starts it | `make check-services`, and the `services` job in `ci.yml` for the end-to-end test |

The old milestone docs are history. Trust the code when they differ.
