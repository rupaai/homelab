# Knowledge map

Find your topic in the table below, then open the files in its row. Read the topic page for the full story.
To check a change, run the test command in the row, or `make test` for everything (see [how it works, level 3](how-it-works.md#level-3-where-the-code-lives)).

## Topics

| Topic | What it is | Screen | API | Core | Rust | Plugin | Test command |
|---|---|---|---|---|---|---|---|
| [Logs](topics/logs.md) | App and Homelab log lines, kept 14 days, searchable | `web/src/routes/logs/`, `web/src/routes/apps/[id]/logs/[env]/` | `internal/api/services.go`, `internal/api/device.go` | `internal/hub/logs.go`, `internal/adapters/docker/logs.go`, `internal/adapters/services/logs.go` | `services/homelab-services/src/logs/` | `plugins/typescript/src/logs.ts`, `plugins/python/src/homelab/logs.py`, `plugins/rust/src/logs.rs` | `make check-services`, `go test ./internal/hub/... ./internal/adapters/docker/...` |
| [Files](topics/files.md) | Folders of photos, videos and other files on the device's disks | `web/src/routes/files/` | `internal/api/services.go`, `internal/api/api.go` (`GET /api/storage`) | `internal/hub/disks.go`, `internal/adapters/services/services.go` | `services/homelab-services/src/files.rs`, `meta.rs`, `disk.rs`, `db.rs` | `plugins/typescript/src/files.ts`, `plugins/python/src/homelab/files.py`, `plugins/rust/src/files.rs` | `make check-services`, `go test ./internal/adapters/services/... ./internal/hub/...` |
| [Databases](topics/databases.md) | Postgres or SQLite per app environment, copies, import, SQL box | `web/src/routes/databases/` | `internal/api/apps.go`, `internal/api/services.go` (`copyBeforeWrite`) | `internal/hub/dbcopies.go`, `internal/hub/dbimport.go`, `internal/hub/backupdata.go`, `internal/adapters/docker/database.go`, `internal/core/database.go` | `services/homelab-services/src/dbs.rs`, `dbs_api.rs`, `dbs_sql.rs` | `plugins/typescript/src/db.ts`, `plugins/python/src/homelab/db.py`, `plugins/rust/src/db.rs` | `make check-services`, `go test ./internal/hub/... ./internal/api/...` |
| [Secrets](topics/secrets.md) | Encrypted passwords and keys, given to apps as files or env vars | `web/src/routes/apps/[id]/secrets/` | `internal/api/secrets.go` | `internal/hub/manage.go`, `internal/hub/access.go`, `internal/hub/generated.go`, `internal/adapters/secretstore/secretstore.go`, `internal/adapters/services/secrets.go` | `services/homelab-services/src/secrets.rs`, `secrets_api.rs` | `plugins/typescript/src/secrets.ts`, `plugins/python/src/homelab/secrets.py`, `plugins/rust/src/secrets.rs` | `make check-services`, `go test ./internal/adapters/secretstore/... ./internal/adapters/services/... ./internal/hub/... ./internal/api/...` |
| [API keys](topics/api-keys.md) | App keys, admin keys, scopes, and the app door (port 7085) | `web/src/routes/settings/keys/` | `internal/api/keys.go`, `internal/api/services.go` | `internal/hub/keys.go`, `internal/state/apikeys.go`, `internal/adapters/services/services.go` (signing) | `services/homelab-services/src/caller.rs` | none (plugins send the key) | `make check-services`, `go test ./internal/hub/... ./internal/state/... ./internal/api/...`, `make check-web` |
| [People](topics/people.md) | Owner, roles, invites, passkey and password sign-in, recovery | `web/src/routes/people/`, `web/src/routes/invite/[token]/` | `internal/api/people.go`, `internal/api/oidc.go`, `internal/api/password.go`, `internal/api/setup.go`, `internal/api/appauth.go` | `internal/hub/people.go`, `internal/hub/access.go`, `internal/hub/invite_email.go`, `internal/identity/identity.go`, `internal/adapters/pocketid/pocketid.go` | none | none (apps read the signed identity headers) | `go test ./internal/identity/... ./internal/hub/... ./internal/api/... ./internal/adapters/pocketid/... ./cmd/...` |
| [Deploys](topics/deploys.md) | Add an app, build, deploy to dev, promote to prod, roll back, health checks | `web/src/routes/apps/[id]/`, `web/src/lib/components/PromoteSheet.svelte` | `internal/api/apps.go` | `internal/hub/actions.go`, `internal/hub/health.go`, `internal/hub/auto.go`, `internal/hub/hub.go`, `internal/manifest/manifest.go`, `internal/adapters/docker/docker.go`, `internal/adapters/docker/compose.go` | none | none | `make check`, `go test ./internal/manifest/... ./internal/hub/... ./internal/adapters/docker/...` |
| [Releases](topics/releases.md) | Version numbers for Homelab and for each app, from PR titles | `web/src/routes/apps/[id]/`, `web/src/lib/components/ReleaseLine.svelte` | `internal/api/services.go` (`/api/releases`) | `internal/hub/versions.go`, `internal/adapters/services/versions.go` | `services/homelab-services/src/releases.rs`, `releases_api.rs`, `releases_github.rs` | `plugins/typescript/src/releases.ts`, `plugins/python/src/homelab/releases.py`, `plugins/rust/src/releases.rs` | `make check-services`, `make check`, `go test ./internal/hub/... ./internal/adapters/services/...` |
| [Self-update](topics/self-update.md) | Homelab updates itself on the device, tests PR versions, goes back if broken | `web/src/routes/resources/` | `internal/api/device.go` | `internal/adapters/selfupdate/`, `internal/hub/device.go`, `cmd/homelabd/setup.go`, `cmd/homelabd/run.go` | none | none | `go test ./internal/adapters/selfupdate/... ./cmd/...` |
| [Companion](topics/companion.md) | The web app (SvelteKit) that you use to manage Homelab | `web/src/routes/`, `web/src/lib/` | `internal/api/static.go`, `internal/api/api.go` | `internal/companion/companion.go` | none | none | `make check-web`, `go test ./internal/api/...` |
| [Plugins](topics/plugins.md) | Small TypeScript, Python and Rust libraries for app code | none | none (they call `/api/...`) | none | none | `plugins/typescript/`, `plugins/python/`, `plugins/rust/` | `cd plugins/typescript && npm test`, `pytest plugins/python`, `cd plugins/rust && cargo test --locked` |
| [Alerts and resources](topics/alerts-resources.md) | Push notifications, the Resources screen, the device card | `web/src/routes/resources/`, `web/src/routes/me/` | `internal/api/device.go` | `internal/hub/alerts.go`, `internal/adapters/webpush/webpush.go`, `internal/adapters/sysres/sysres.go`, `internal/adapters/sysinfo/sysinfo.go` | none | none | `go test ./internal/hub/... ./internal/adapters/sysres/... ./internal/adapters/sysinfo/...`, `make check-web` |
| [Services program](topics/services-program.md) | `homelab-services`, the Rust program that holds files, databases, secrets, versions and logs | none (the device card on `web/src/routes/resources/` shows its state) | `internal/api/services.go` | `internal/adapters/services/services.go`, `cmd/homelabd/run.go` | `services/homelab-services/src/main.rs`, `app.rs`, `caller.rs`, `config.rs`, `api.rs` | none | `make check-services`, `go test ./internal/adapters/services/... ./internal/api/...` |

## I want to...

- To add a field to `homelab.yaml`: open `internal/manifest/manifest.go` (the `Manifest` struct and `validate()`) and `internal/manifest/manifest_test.go`. Then read [Deploys](topics/deploys.md). Test with `go test ./internal/manifest/...`.
- To add a screen to the companion: open a new folder with `+page.svelte` under `web/src/routes/`, and the menu link in `web/src/routes/+layout.svelte`. Then read [Companion](topics/companion.md). Test with `make check-web`.
- To add an API route for the companion: open `internal/api/api.go` (the route table) and the handler file, such as `internal/api/apps.go`. Then read [Companion](topics/companion.md). Test with `go test ./internal/api/...`.
- To add an API route for apps (app keys): open `internal/api/services.go` (`servicePaths`), the Rust route file in `services/homelab-services/src/` (for example `files.rs` or `secrets_api.rs`), and the matching file in `docs/`. Then read [API keys](topics/api-keys.md). Test with `make check-services` and `go test ./internal/api/...`.
- To add a new platform service end to end: open `services/homelab-services/src/` (`lib.rs`, `app.rs`, `main.rs`, and a new `<name>.rs` and `<name>_api.rs`), `internal/adapters/services/<name>.go`, `internal/core/contracts.go`, and `internal/api/services.go`. Then read [Services program](topics/services-program.md), which lists the steps. Test with `make check-services` and `go test ./internal/adapters/services/...`.
- To change who can see secrets: open `internal/hub/access.go` (`ActionSecretValues`) and `internal/hub/people.go` (who may give the `secrets` role). Then read [Secrets](topics/secrets.md). Test with `go test ./internal/hub/... ./internal/api/...`.
- To change log retention: open `KEEP_DAYS` in `services/homelab-services/src/logs/mod.rs` (14 days), and `max_mb` in `cmd/homelabd/config.go` (the size cap). Then read [Logs](topics/logs.md). Test with `make check-services`.
- To change the health check: open `checkEnv` in `internal/hub/health.go`, `HealthEvery` in `internal/hub/hub.go` (30 seconds), and `internal/adapters/httphealth/httphealth.go`. Then read [Deploys](topics/deploys.md). Test with `go test ./internal/hub/... ./internal/adapters/httphealth/...`.
- To add an alert: open `internal/hub/alerts.go` (`checkAlerts`, `notify`, and `OpenAlert`, `MarkAlertSent`, `CloseAlert`). Then read [Alerts and resources](topics/alerts-resources.md). Test with `go test ./internal/hub/...`.
- To change how versions are picked: open `classify`, `bump` and `plan` in `services/homelab-services/src/releases.rs`, and add a case to `releases_tests.rs`. Then read [Releases](topics/releases.md). Test with `make check-services`.
- To add a plugin call in all three languages: open `plugins/typescript/src/<service>.ts`, `plugins/python/src/homelab/<service>.py` and `plugins/rust/src/<service>.rs`, plus the tests in each `test` or `tests` folder. Then read [Plugins](topics/plugins.md). Test with `cd plugins/typescript && npm test`, `pytest plugins/python` and `cd plugins/rust && cargo test --locked`.
- To change a `config.yaml` setting: open `cmd/homelabd/config.go` (the `Config` struct, defaults and checks), then the code that uses the setting. Then read the topic for that setting, for example [API keys](topics/api-keys.md) for `app_api_port`, or [Services program](topics/services-program.md) for `services_port`. Test with `go test ./cmd/...`.
- To change sign-in: open `internal/api/oidc.go` (passkeys), `internal/api/password.go` (passwords) and `internal/hub/access.go`. Then read [People](topics/people.md). Test with `go test ./internal/api/... ./internal/adapters/pocketid/...`.
- To change how an app's container gets its secrets (files or env vars): open `internal/hub/hub.go` (the deploy spec) and `internal/adapters/docker/docker.go` (the files). Then read [Secrets](topics/secrets.md). Test with `go test ./internal/hub/... ./internal/adapters/docker/...`.
- To change how long dev must be healthy before auto-promote: open `DefaultPromoteAfter` in `internal/manifest/manifest.go` (30 minutes) and `internal/hub/auto.go`. Then read [Deploys](topics/deploys.md). Test with `go test ./internal/manifest/... ./internal/hub/...`.
- To change how many database copies are kept: open `KeepBackups` in `internal/hub/hub.go` (5 by default). Then read [Databases](topics/databases.md). Test with `go test ./internal/hub/...`.
- To change which SQL box statements need a confirm: open `risky` in `services/homelab-services/src/dbs_sql.rs` and add a test in the same file. Then read [Databases](topics/databases.md). Test with `make check-services`.
- To change the upload limit or add a storage disk: open `defaultMaxUploadMB` and `storage` in `cmd/homelabd/config.go`, and `services/homelab-services/src/config.rs`. Then read [Files](topics/files.md). Test with `make check-services` and `go test ./internal/adapters/services/...`.
- To change what an app key may do by default: open `appKeyScopes` in `internal/hub/keys.go`. Then read [API keys](topics/api-keys.md). Test with `go test ./internal/hub/... ./internal/state/...` and `make check-services`.
- To change how long a new Homelab version must run before it is kept: open `ProveAfter` and `maxTries` in `internal/adapters/selfupdate/layout.go`. Then read [Self-update](topics/self-update.md). Test with `go test ./internal/adapters/selfupdate/...`.
- To work on a screen with no Homelab running: run `cd web && npm ci && npm run dev`, then open the address with `?mock` at the end. The screen shows fake data from `web/src/lib/mock.ts`. Then read [Companion](topics/companion.md). Test with `make check-web`.

## Folder by folder

- `.github/`: CI, release and PR-title workflows, and the `build-dist` action. Topics: [Releases](topics/releases.md), [Plugins](topics/plugins.md) (the `plugins` job in `ci.yml`).
- `cmd/homelabd/`: the `homelabd` program: commands, `config.yaml`, and the wiring in `run.go`. Topics: [Self-update](topics/self-update.md), [People](topics/people.md) (`recover.go`, `password.go`), [Services program](topics/services-program.md).
- `docs/`: the reference docs and OpenAPI files. Topics: all. Start with `platform-services.md`.
- `docs/history/`: old milestone docs. History only, do not edit.
- `docs/topics/`: the topic pages in this map.
- `handover/`: feature notes and their rules (`handover/README.md`). Topic: none (process).
- `internal/`: the Go code. Topics are listed per subfolder below.
- `plugins/`: the TypeScript, Python and Rust libraries. Topic: [Plugins](topics/plugins.md).
- `scripts/`: `build-release.sh` (releases, self-update), `handover.py` and `test_handover.py` (handover notes). Topics: [Releases](topics/releases.md), [Self-update](topics/self-update.md).
- `services/`: the Rust workspace, with the crate `services/homelab-services`. Topic: [Services program](topics/services-program.md).
- `web/`: the companion (SvelteKit). Topic: [Companion](topics/companion.md).

Inside `internal/`:

- `internal/adapters/`: one folder per outside system. Each is listed below.
- `internal/api/`: the HTTP routes for the companion and the app door. Topics: all. See the table above for the file per topic.
- `internal/companion/`: holds the built web app, embedded in `homelabd`. Topic: [Companion](topics/companion.md).
- `internal/core/`: the shared types and the interfaces between the hub and the adapters. Topics: all.
- `internal/hub/`: the business rules. Topics: all. See the table above.
- `internal/identity/`: signs the headers that tell an app who is calling. Topic: [People](topics/people.md).
- `internal/manifest/`: reads `homelab.yaml`. Topics: [Deploys](topics/deploys.md), and the settings in [Logs](topics/logs.md), [Databases](topics/databases.md) and [Secrets](topics/secrets.md).
- `internal/sharedir/`: lets app containers write to their data folders. Topic: [Deploys](topics/deploys.md).
- `internal/state/`: Homelab's own SQLite store (`homelab.db`). Topics: [People](topics/people.md), [API keys](topics/api-keys.md), [Alerts and resources](topics/alerts-resources.md) (push subscriptions).

Inside `internal/adapters/`:

- `docker/`: runs the app containers with Docker Compose, the log follower, the database containers and the smoke test. Topics: [Deploys](topics/deploys.md), [Logs](topics/logs.md), [Databases](topics/databases.md).
- `fake/`: in-memory fakes of every core interface, for tests. Topic: none.
- `github/`: reads builds, commits, files and image digests from GitHub. Topic: [Deploys](topics/deploys.md).
- `gitmirror/`: keeps a read-only git copy of each app's repo. Topic: [Deploys](topics/deploys.md).
- `httphealth/`: the HTTP probe for health checks. Topic: [Deploys](topics/deploys.md).
- `local/`: gives each app address its own port on the device. Topic: [Deploys](topics/deploys.md).
- `pocketid/`: talks to Pocket ID, the passkey service. Topic: [People](topics/people.md).
- `secretstore/`: makes and checks the master key file. Topic: [Secrets](topics/secrets.md).
- `selfupdate/`: downloads, switches, guards and tests new Homelab versions. Topic: [Self-update](topics/self-update.md).
- `services/`: starts `homelab-services` and calls it (files, databases, secrets, versions, logs). Topic: [Services program](topics/services-program.md).
- `smtp/`: sends email (invites and tests). Topic: [People](topics/people.md).
- `sysinfo/`: device details and the root report. Topic: [Alerts and resources](topics/alerts-resources.md).
- `sysres/`: CPU, memory, disk and app usage readings. Topic: [Alerts and resources](topics/alerts-resources.md).
- `webpush/`: sends push notifications. Topic: [Alerts and resources](topics/alerts-resources.md).

Inside `services/homelab-services/src/`:

- `logs/`: log store, search, routes and line parsing. Topic: [Logs](topics/logs.md).
- `secrets/`: store tests. Topic: [Secrets](topics/secrets.md).
- The other files are one module each: `files.rs` (Files), `dbs.rs`, `dbs_api.rs`, `dbs_sql.rs` (Databases), `secrets.rs`, `secrets_api.rs` (Secrets), `releases*.rs` (Releases), and the shared files listed below.

## Shared pieces

Many topics use these files. Read them before you change a shared rule.

- `internal/core/contracts.go`: the interfaces between the hub and the adapters. Add a method here when a new adapter needs one.
- `internal/core/types.go`: the shared types, such as apps, releases and environments.
- `internal/state/store.go`: Homelab's SQLite store, for the desired state and history.
- `internal/api/api.go`: the route table, and the check of what each route needs.
- `services/homelab-services/src/api.rs`: shared helpers for handlers: errors, replies, `forbidden()` and `valid_place()`.
- `services/homelab-services/src/app.rs`: the router, the shared state, the caller check and the per-module refusals.
- `services/homelab-services/src/caller.rs`: checks the signed caller headers from `homelabd`.
- `internal/adapters/fake/`: the in-memory fakes that tests use in place of Docker, GitHub and the others.
