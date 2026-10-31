# Services program (homelab-services)

This page covers `homelab-services`, the second Homelab program. It is written in Rust. It keeps files, databases, secrets, release versions and logs. It is started and checked by `homelabd`, and nothing else calls it directly. For the big picture, see [how Homelab works](../how-it-works.md) and [the map](../map.md). For the web API that reaches it, see [platform-services.md](../platform-services.md#homelab-services).

## In short

- `homelab-services` stores files, database records, secrets, release versions and logs. The Go program (`homelabd`) does the rest.
- It listens on `127.0.0.1:7086` only (`services_port` in `config.yaml`). Nothing outside the device can reach it.
- Every call it gets must be signed by `homelabd`. Calls that are unsigned, old, or signed for another request are refused.
- `homelabd` starts it from next to itself, restarts it when it stops, and tells the companion when it is down.
- To add a module, you touch the Rust crate and the Go adapter, and then each plugin. The steps are at the end of this page.

## How it works

1. `homelabd` starts up (`cmd/homelabd/run.go`). It makes a new signing key (ed25519) in memory. The key is new every time `homelabd` starts.
2. `internal/adapters/services/services.go` (`New`) prepares the proxy and the signing. `Run` starts `homelab-services` with its settings as environment variables. If the program is not installed next to `homelabd`, the services are marked as off, and the device card says so.
3. The program reads its settings (`services/homelab-services/src/config.rs`). It refuses to start if a setting is wrong. It opens its files in the data folder: `files.db`, `dbs.db`, `logs.db` and `secrets.db`.
4. `homelabd` waits for `/healthz` to answer (up to 20 seconds in `waitHealthy`). Then it marks the services as up.
5. A call from the companion or an app reaches the API. The routes in `internal/api/services.go` (`servicePaths`) forward `/api/files`, `/api/db`, `/api/data`, `/api/secrets`, `/api/releases` and `/api/logs`. `homelabd` checks the caller first.
6. The proxy (`Rewrite` in `services.go`) removes any `X-Homelab-*` headers, cookies and the key from the call. It then adds the caller's identity and signs it: `X-Homelab-Caller` (the JSON, base64url) and `X-Homelab-Signature` (an ed25519 signature over `homelab-services/v1\n` and the caller text).
7. `homelab-services` checks the signature and the caller (`src/caller.rs`, function `verify`). Then it checks each module's rules (`refuse_path` in `src/app.rs`). Then the route runs.
8. If the program stops, `Run` starts it again after a pause. The pause is 1 second at first, doubles up to 30 seconds, and goes back to 1 second after a minute of steady running.
9. While the program is down, the calls get `503` with a short message (`unavailable` in `services.go`). The device card on the Resources screen says the services are not running.

Settings the program reads (set by `homelabd`, not by you):

| Variable | What it is |
|---|---|
| `HOMELAB_SERVICES_LISTEN` | The loopback address to listen on. Must be a loopback IP and port (`127.0.0.1:7086`). |
| `HOMELAB_SERVICES_DATA` | The folder for its own files: `<home>/services`. |
| `HOMELAB_SERVICES_KEY` | `homelabd`'s public key (32 bytes, base64url, no padding). |
| `HOMELAB_SERVICES_STORAGE` | JSON list of disks, for example `[{"name":"main","path":"/mnt/storage"}]`. The first one is the default. Built from `storage` in `config.yaml`. |
| `HOMELAB_SERVICES_MAX_UPLOAD` | The biggest upload in bytes. Default 10 GiB. Set from `max_upload_mb`. |
| `HOMELAB_SERVICES_SECRETS_KEY` | The path of the master key file, `<home>/secrets/master.key`. Without it the secrets routes answer with the reason. The key itself is never passed in the environment. |
| `HOMELAB_SERVICES_SECRETS_IMPORT` | The path of the old `<home>/secrets/secrets.db`, imported once. |
| `HOMELAB_SERVICES_LOGS` | JSON of the log settings (`logs:` in `config.yaml`). |
| `GITHUB_API_URL` | Optional. Where release calls go. Default `https://api.github.com`. |

Where its files are. `<home>` is the Homelab folder: `~/.homelab` by default, or `/var/lib/homelab` on a device (`--home` changes it):

- `<home>/services/files.db`: file records (`src/files.rs`).
- `<home>/services/dbs.db`: the database registry (`src/dbs.rs`).
- `<home>/services/logs.db`: the log lines (`src/logs/mod.rs`). Lines older than 14 days are removed (`KEEP_DAYS`).
- `<home>/services/secrets.db`: the encrypted secret values (`src/secrets.rs`). The key is in `<home>/secrets/master.key`, made by `homelabd`.
- `<disk>/homelab-files/`: the file bytes on each storage disk (`src/disk.rs`). Temporary uploads are in `.tmp` in that folder.

Who may do what:

- An app key can use its own app's secrets and release versions, and sees its own app's database and logs. The exact rules are in each `refuse_path`.
- People and admin keys are refused on `/api/secrets` and `/api/releases`. They can use `/api/logs` and the files routes.
- Only `homelabd` (caller kind `system`) can use the `/internal/...` routes. Any other caller gets `403` (`refuse_path` in `dbs_api.rs`, `secrets_api.rs`, `releases_api.rs` and `logs/api.rs`).
- The plugins use whichever key you give them. The calls each key may make are in [plugins.md](plugins.md).

## Deep dive

**Files**

| File | What it does |
|---|---|
| `services/Cargo.toml` | The Rust workspace. The member is `homelab-services`. |
| `services/homelab-services/Cargo.toml` | The crate's dependencies. |
| `services/homelab-services/src/main.rs` | Start-up. Runs the server, or the `version` and `release` commands. Opens the stores, starts the purge and retention tasks, and stops on Ctrl-C or SIGTERM. |
| `services/homelab-services/src/lib.rs` | The module list. |
| `services/homelab-services/src/app.rs` | The router (`app`), the shared state (`AppState`), `/healthz`, the signed-caller check (`require_caller`), the per-module refusals, and the JSON error shape. |
| `services/homelab-services/src/api.rs` | What handlers share: `ApiError`, `err()`, `Reply`, `forbidden()` (logs the refusal, returns 403), and `valid_place()` (app and env names). |
| `services/homelab-services/src/caller.rs` | Checks the signed caller (`verify`). Defines the caller kinds (`person`, `admin`, `app`, `system`). |
| `services/homelab-services/src/config.rs` | Reads and checks the settings. |
| `services/homelab-services/src/disk.rs` | Storage disks, the `homelab-files` folder, and name checks (`valid_name`). |
| `services/homelab-services/src/files.rs`, `files_tests.rs` | Files: folders, upload, list, download and delete. |
| `services/homelab-services/src/meta.rs` | Reads the info of photos, video, audio and PDFs. |
| `services/homelab-services/src/dbs.rs`, `dbs_api.rs`, `dbs_sql.rs`, `dbs_api_tests.rs` | Databases: the registry, routes, the SQL reader for write mode (`dbs_sql.rs`, which finds risky statements), and tests. |
| `services/homelab-services/src/db.rs` | The SQLite helper for the files database (folders and file records). |
| `services/homelab-services/src/secrets.rs`, `secrets_api.rs`, `secrets/tests.rs` | Secrets: the encrypted store, its routes (tests for the routes are in `secrets_api.rs`), and the store's tests. |
| `services/homelab-services/src/releases.rs`, `releases_api.rs`, `releases_github.rs`, `releases_tests.rs` | Release versions, the routes, and the GitHub REST calls (the token comes from the caller and is never printed). |
| `services/homelab-services/src/logs/mod.rs`, `api.rs`, `parse.rs`, `tests.rs` | Logs: store, search, routes, and line parsing. |
| `services/homelab-services/src/testutil.rs` | Test helpers: a test key, a signer that acts like `homelabd`, and a test `AppState`. |
| `services/homelab-services/tests/cli.rs`, `tests/fixtures/` | Runs the real binary to check the command line and start-up errors. |
| `internal/adapters/services/services.go` | Starts the program, signs the calls, forwards them, restarts it, and checks it is up. |
| `internal/adapters/services/secrets.go` | The shared `call` helper that `homelabd` uses for its own calls, and the secrets client. |
| `internal/adapters/services/databases.go`, `logs.go`, `versions.go` | The other clients: databases, logs ingest, and releases (`Publish` and `List`). |
| `internal/api/services.go` | The forwarded paths (`servicePaths`) and `personToServices`, which checks the person first. |
| `cmd/homelabd/run.go` | Creates the services object with the home folders and the master key path, runs it, and hands it to the hub. |
| `cmd/homelabd/config.go` | `services_port` (default 7086), `storage`, `max_upload_mb`, and `logs`. |

**Routes**

- `GET /healthz`: open to anyone on the device. Shows the version. No caller needed.
- `/api/files/...`: files (see `src/files.rs`).
- `/api/data`: forwarded by `homelabd`, but no routes exist behind it yet ("Data comes next" in [platform-services.md](../platform-services.md#homelab-services)).
- `/api/db/...`: databases (see `src/dbs_api.rs`). `/internal/db/...` is for `homelabd` only.
- `/api/secrets/...`: secrets. `/internal/secrets/...` is for `homelabd` only.
- `/api/releases...`: release versions for an app's own key.
- `/api/logs...`: search, for people and keys. `homelabd`'s own calls go to `/internal/logs/...`, such as `/internal/logs/ingest`.
- Each module's route list is in its `*_api.rs` file (`routes()`). The request and answer shapes are in the OpenAPI files in `docs/` (`files.openapi.yaml`, `secrets.openapi.yaml`, `logs.openapi.yaml`) and in [platform-services.md](../platform-services.md).

**Edge cases and rules**

- The signature must match the exact bytes of the caller header. The header is at most 8 KiB (`MAX_CALLER_LEN`).
- The time in the caller header may differ from the program's clock by at most 30 seconds (`MAX_CLOCK_SKEW`).
- The header must appear once. A repeated header is refused.
- The caller's method and path (with the query) must match the request exactly. A changed query is refused.
- The caller's version must be 1. Unknown fields in the caller are refused (`deny_unknown_fields`).
- `/healthz` with `GET` needs no caller. Any other method on it needs one.
- Unknown routes answer `404` only after the caller check passes (`not_found` in `app.rs`).
- Files: upload size is limited by `max_upload_mb`. Temporary files are cleaned up (`clean_tmp` in `files.rs`).
- Secrets: without a master key the secrets routes answer with a reason, and the files routes still work.
- `homelabd` does not forward a call from the system caller (`Forward` in `services.go` answers `403`).
- If `homelabd` cannot find the program next to it, it tries to fetch it (`run.go`) when the device is managed by self-update. Otherwise the services are marked as off, with a message on the device card.

**To change it**

- To change a setting the program reads, change `src/config.rs`, then the matching variable in `runOnce` in `internal/adapters/services/services.go`. Test both.
- To change how the program is restarted, change `Run` in `services.go` (the pauses are `minPause`, `maxPause` and `steady`).
- To change which paths are forwarded, change `servicePaths` in `internal/api/services.go`. Then check the rules in `personToServices` for people.
- To change the signature rules, change `caller.rs` and `services.go` (`sign`) together. They must agree on the exact bytes.

**To add a new service module, end to end**

Use `releases` or `logs` as the model. They are the most recent modules.

1. Rust store: add `services/homelab-services/src/<name>.rs` with the storage code. Add a file for its own state under the data folder, as `data_dir.join("<name>.db")`.
2. Rust routes: add `services/homelab-services/src/<name>_api.rs` with `pub fn routes() -> Router<AppState>` and `pub fn refuse_path(caller, path) -> Option<ApiError>`. Use `Reply`, `err`, `forbidden` and `valid_place` from `api.rs`. Put internal routes under `/internal/<name>` and refuse everyone but `Kind::System` there.
3. Wire it in: add `pub mod <name>;` and `pub mod <name>_api;` in `src/lib.rs`, add the store to `AppState` in `src/app.rs`, `.merge(<name>_api::routes())` in `app()`, and a `refuse_path` call in `require_caller`.
4. Start-up: open the store in `serve` in `src/main.rs`, and pass it into `AppState`.
5. Tests: add `src/<name>_tests.rs` (declared with `#[cfg(test)] mod` in `lib.rs`, as `releases_tests.rs` is). Add the new `AppState` field in `src/testutil.rs` (`test_env`).
6. Go adapter: add `internal/adapters/services/<name>.go` with a type that holds `*Services` and calls `s.call("<name>", errNoServices, timeout, method, path, query, in)`. Add the matching interface in `internal/core/contracts.go`.
7. Wire the adapter: add the method on `*Services` in `<name>.go`, pass it to the hub in `cmd/homelabd/run.go` (the `hub.Config` fields, around line 227).
8. Public path: if apps or people need it, add `/api/<name>` to `servicePaths` in `internal/api/services.go`, and decide the rule for people in `personToServices`.
9. Plugins: add the calls to `plugins/typescript`, `plugins/python` and `plugins/rust`. See [plugins.md](plugins.md#add-a-call-to-all-three).
10. Docs and handover: add an OpenAPI file in `docs/`, a section in [platform-services.md](../platform-services.md), and a note in `handover/notes/<feature>/`.

**Test it**

- `make check-services`: `cargo fmt --check`, `cargo clippy --all-targets -- -D warnings`, and `cargo test` in `services/`. CI runs the same in the `services` job.
- `go test ./internal/adapters/services/...` and `go test ./internal/api/...`: the Go side.
- The end-to-end test: `cd services && cargo build --locked --release`, then from the repo root `HOMELAB_SERVICES_BIN=$PWD/services/target/release/homelab-services go test ./internal/adapters/services -run Integration -v`. This is the step in the `services` job of `.github/workflows/ci.yml`.
- `make test` runs `make check-services` with the rest.
