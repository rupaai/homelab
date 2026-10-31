# Plugins

This page covers the plugins: small libraries in `plugins/` that let app code use Homelab's services in a few lines. It covers the three languages, the environment variables they read, how they are tested in CI, and how to add a call to all three. For the big picture, see [how Homelab works](../how-it-works.md) and [the map](../map.md). For the web API they call, see [platform-services.md](../platform-services.md).

## In short

- There are three plugins, one per language: TypeScript (`plugins/typescript`), Python (`plugins/python`) and Rust (`plugins/rust`).
- They cover five services: files, secrets, databases (read only), releases (the app's own versions) and logs search.
- A plugin only makes web calls to the Homelab API. The real work happens in `homelabd` and `homelab-services`.
- In a Homelab app, `HOMELAB_API_URL` and `HOMELAB_TOKEN` are already set, so the plugin needs no setup.
- CI tests all three in the `plugins` job of `.github/workflows/ci.yml`.

## How it works

1. An app's code creates a client. The client reads `HOMELAB_API_URL` and `HOMELAB_TOKEN` from the environment, unless you pass them in (`plugins/typescript/src/http.ts`, `plugins/python/src/homelab/client.py`, `plugins/rust/src/client.rs`).
2. A call such as `files.upload` becomes one HTTP request. Every request sends `Authorization: Bearer <token>`. The plugin never follows redirects, so the token cannot go to another place.
3. The API address is the app door that Homelab sets for each app. `homelabd` checks the key and its scopes, then forwards the call to `homelab-services` on `127.0.0.1` (see [services program](services-program.md)).
4. `homelab-services` stores the bytes, reads the info, and checks access. It is the one place with the rules.
5. The plugin turns the answer into its own types. Errors become one error type, `HomelabError` (TypeScript and Python) or `Error` (Rust), with a `status` and a `message`. `status` is 0 for a network error or a bad input that was refused before sending.
6. Unknown fields in an answer are kept or ignored, never an error. An older Homelab keeps working with a newer plugin.

Environment variables the plugins read:

| Variable | Set by Homelab? | Used by |
|---|---|---|
| `HOMELAB_API_URL` | Yes, in every app | The address of the API. Required unless passed in. |
| `HOMELAB_TOKEN` | Yes, in every app | The app's key. Required unless passed in. Never printed. |
| `HOMELAB_APP`, `HOMELAB_ENV` | Yes, in every app | Default app and environment for `db` calls. |
| `HOMELAB_VERSION` | Yes, when Homelab gave the build a version | `version()` (releases). Missing means `null`. |

Outside an app (for example a server elsewhere), pass the companion's address and a key from **Settings → API keys** in the companion. The keys are listed in [platform-services.md](../platform-services.md#api-keys).

Secrets have one more source. Without a version and without `fresh`, `secrets.get` first reads the file `<dir>/<name>`, where `dir` defaults to `/run/secrets` (`DEFAULT_SECRETS_DIR` in `plugins/typescript/src/secrets.ts` and `plugins/python/src/homelab/secrets.py`). If there is no file, it asks Homelab.

What each service covers:

- **Files**: folders, upload, list, info, update, download and delete (`files.ts`, `files.py`, `files.rs`).
- **Secrets**: get, set, delete, list and versions. `set` and `delete` need the `secrets:write` scope (`secrets_api: write` in `homelab.yaml`).
- **Databases**: list, info, tables, rows and read-only SQL (`db.ts`, `db.py`, `db.rs`). The plugins never send `write`. Writing is for people in the companion.
- **Releases**: `version()` and the list of the app's own releases (`releases.ts`, `releases.py`, `releases.rs`). Needs the `releases:read` scope.
- **Logs**: search the app's lines and Homelab's events about it (`logs.ts`, `logs.py`, `logs.rs`). Needs the `logs:read` scope. Python also has `logs.setup()` for JSON log lines.

The full list of calls, with the paths and rules for each service, is in [plugins/README.md](../../plugins/README.md#files-api-surface-names-per-language-style). The request and answer shapes are in [docs/files.openapi.yaml](../files.openapi.yaml), [docs/secrets.openapi.yaml](../secrets.openapi.yaml) and [docs/logs.openapi.yaml](../logs.openapi.yaml).

## Deep dive

**Files**

| File | What it does |
|---|---|
| `plugins/README.md` | The shared rules, the install lines, and every call with its path. |
| `plugins/typescript/README.md`, `plugins/python/README.md`, `plugins/rust/README.md` | Quick start and every call for each language. |
| `plugins/typescript/src/index.ts` | The `Homelab` class. Creates the services and exports the types. |
| `plugins/typescript/src/http.ts` | The shared transport: URL and token, timeouts, no redirects. |
| `plugins/typescript/src/errors.ts` | `HomelabError`. |
| `plugins/typescript/src/files.ts`, `secrets.ts`, `db.ts`, `releases.ts`, `logs.ts` | One file per service. |
| `plugins/typescript/src/types.ts` | The answer types. |
| `plugins/typescript/test/*.test.ts` | Tests for db, files, logs, releases and secrets, against a fake server. |
| `plugins/typescript/package.json`, `tsconfig.json`, `tsconfig.build.json`, `vitest.config.ts` | Build, type check and test setup. |
| `plugins/python/src/homelab/__init__.py` | The public names. |
| `plugins/python/src/homelab/client.py` | The `Homelab` class. Creates the services. |
| `plugins/python/src/homelab/_http.py` | The shared transport (standard library only). |
| `plugins/python/src/homelab/errors.py` | `HomelabError`. |
| `plugins/python/src/homelab/files.py`, `secrets.py`, `db.py`, `releases.py`, `logs.py` | One file per service. |
| `plugins/python/src/homelab/types.py` | The answer types. |
| `plugins/python/tests/test_*.py`, `tests/conftest.py` | Tests for each service. `conftest.py` has the fake server. |
| `plugins/python/pyproject.toml` | Package name `homelab-sdk`, Python 3.9 or newer, and the `dev` extra. |
| `plugins/rust/src/lib.rs` | Module list and the public names. Its doc comment has examples. |
| `plugins/rust/src/client.rs` | `Homelab` with `from_env()` and `new()`, and the accessors `files()`, `secrets()`, `db()`, `releases()` and `logs()`. |
| `plugins/rust/src/error.rs` | `Error` and `Result`. |
| `plugins/rust/src/types.rs` | The answer types. |
| `plugins/rust/src/files.rs`, `secrets.rs`, `db.rs`, `releases.rs`, `logs.rs` | One file per service. |
| `plugins/rust/tests/*.rs`, `tests/common/mod.rs` | Tests for each service. `common/mod.rs` has the fake server. |
| `plugins/rust/Cargo.toml` | Crate `homelab-sdk`, library name `homelab`. |
| `.github/workflows/ci.yml` (`plugins` job) | Runs all three in CI. |

**Routes**

The plugins call these web routes. Each one has a server handler in `internal/api/` or `services/homelab-services/src/`.

- `/api/files/...`: folders, upload (`POST /api/files/{folder}`, raw bytes), list, info, download and delete. Handled by `services/homelab-services/src/files.rs`.
- `/api/secrets/...`: get, set, delete, list and versions. Handled by `services/homelab-services/src/secrets_api.rs`.
- `/api/db/...`: list, info, tables, rows and query. Handled by `services/homelab-services/src/dbs_api.rs`.
- `/api/releases`: the app's releases. Handled by `services/homelab-services/src/releases_api.rs`.
- `/api/logs`: search. Handled by `services/homelab-services/src/logs/api.rs`.

**Edge cases and rules**

- The token is never printed. It is kept out of errors, debug output and `repr`. The plugins also blank the value from an error if the server ever echoed it back.
- Names are checked before anything is sent. Folder, app, environment and table names that are `.` or `..` are refused. Secret names must match the server's rule (1 to 200 of `A-Z a-z 0-9 _ . : -`).
- Limits are checked before sending: `db` rows `limit` 1 to 200, logs `limit` 1 to 500 (default 100), and a version must be a whole number from 1.
- Timeouts: normal calls default to 60 seconds. Upload and download bodies have no timeout (`http.ts`, `_http.py`).
- A missing `HOMELAB_APP` for a `db` call, or a missing `HOMELAB_API_URL` or `HOMELAB_TOKEN`, raises a clear error before any call.
- An app key sees only its own app's data. Another app or environment gives `404`.
- Admin keys and people get `403` on the releases routes. `homelab-services` refuses them again, so the rule holds even if `homelabd` changes.

**To change it**

- To add a call to all three plugins, follow the steps in "Add a call to all three" below.
- To change how errors look, edit `errors.ts`, `errors.py` and `error.rs`. Then check every service's tests, because each one asserts on messages.
- To change the shared transport (headers, timeouts, redirects), edit `http.ts`, `_http.py` and the `Inner` type in `client.rs`. Change all three, or the languages will behave differently.
- To change a service's rules that the server enforces, change the server first (`services/homelab-services/src/`), then the plugins.

**Add a call to all three**

1. Check that the route exists on the server. Look in the matching `*_api.rs` file (or `logs/api.rs`), and in the OpenAPI file in `docs/`. If it is missing, add it on the server first (see [services program](services-program.md)).
2. TypeScript: add the method to the service class in `plugins/typescript/src/<service>.ts`, add any new type to `types.ts`, and add a test in `plugins/typescript/test/<service>.test.ts`.
3. Python: add the method to `plugins/python/src/homelab/<service>.py`, add any new type to `types.py`, export it in `__init__.py` if it is public, and add a test in `plugins/python/tests/test_<service>.py`.
4. Rust: add the method to `plugins/rust/src/<service>.rs`, add any new type to `types.rs`, and add a test in `plugins/rust/tests/<service>.rs`. Export a new public name from `lib.rs` if needed.
5. Use each language's naming style (for example `getOrNull` in TypeScript, `get_or_none` in Python and `get_opt` in Rust, as in `plugins/README.md`).
6. Update the call list in `plugins/README.md` and the language README. Then run the three test commands below.
7. Add a note in `handover/notes/<feature>/` (see [handover/README.md](../../handover/README.md)).

**Test it**

These are the commands CI runs in the `plugins` job of `.github/workflows/ci.yml`:

- TypeScript: `cd plugins/typescript && npm ci && npm run check && npm test && npm run build`. `check` is `tsc --noEmit`, `test` is Vitest.
- Python: `python -m venv <a folder> && . <a folder>/bin/activate && pip install -e "plugins/python[dev]" && pytest plugins/python`.
- Rust: `cd plugins/rust && rustup component add rustfmt clippy && cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked`.

The Makefile has no plugin target. `make test` does not run these.
