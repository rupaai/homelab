# Logs

This page covers the logs: what is kept, how a line gets there, and where to change it. See [the map](../map.md) for the other topics.

## In short

- Keeps the printed output of every app's containers, plus Homelab's own lines about files, databases, secrets and deploys.
- Admins and people see everything on the **Logs** screen (`/logs`). An app's own key sees only its own app and environment.
- Lines are kept 14 days, and the store is capped at 2048 MB by default.
- Secret values are hidden before a line is stored.
- Searched with `GET /api/logs`, or on the companion's Logs screen.

## How it works

1. **homelabd** (Go) looks for running app containers every 5 seconds. It only picks containers with the `homelab.app` and `homelab.env` labels. Every call to Docker is a read (GET). The follower is in [`internal/adapters/docker/logs.go`](../../internal/adapters/docker/logs.go).
2. For each container it reads the log since the last saved place (its cursor). A container with no cursor is read back 14 days. Cursors are kept in `<home>/logs-cursors.json`.
3. Lines go in batches of up to 500, or every second. The batches go to `IngestLogs` in [`internal/hub/logs.go`](../../internal/hub/logs.go).
4. The hub hides secret values, and looks up the app's `min_level`. Both are cached for 5 seconds.
5. [`internal/adapters/services/logs.go`](../../internal/adapters/services/logs.go) sends the batch to **homelab-services** (Rust) at `/internal/logs/ingest`. A batch goes in calls of up to 2000 lines or 3 MB.
6. homelab-services ([`services/homelab-services/src/logs/`](../../services/homelab-services/src/logs/)) reads each line in `parse.rs`. It finds the level (debug, info, warn, error, fatal) and drops lines below the minimum. Lines with no clear level are always kept.
7. Lines are stored in `<home>/services/logs.db`, an SQLite file with full-text search. Each line keeps its app, environment, service, stream (stdout or stderr), time, and any extra JSON fields.
8. Homelab's own events (for example "file saved") go through `sendLogEvent` in [`internal/hub/logs.go`](../../internal/hub/logs.go). They are sent in the background to `/internal/logs/event`. At most 32 are on their way at once. Extra ones are dropped, because the activity log has them too.
9. Retention: an hourly job (`spawn_retention` in `mod.rs`) deletes lines older than 14 days. While the file is over its cap, it deletes the oldest lines first.
10. Search: `GET /api/logs` goes to `homelabd`, which forwards it to homelab-services (`/api/logs` is in `servicePaths` in [`internal/api/services.go`](../../internal/api/services.go)). Who may search is decided in `scope_of` in [`services/homelab-services/src/logs/api.rs`](../../services/homelab-services/src/logs/api.rs).
11. The live view of one app (`GET /api/apps/{id}/envs/{env}/logs?lines=`) reads straight from Docker. It is masked, but it is not stored. The handler is in [`internal/api/device.go`](../../internal/api/device.go).

**Settings:**

- `homelab.yaml`, `logs: min_level:` keeps that level and louder for one app. Default: debug on dev, info on prod. Code: `LogsConfig` and `LogMinLevel` in [`internal/manifest/manifest.go`](../../internal/manifest/manifest.go).
- `config.yaml`, `logs:` sets Homelab's own lines. `min_level` (default info), `services:` (a level per service, e.g. `files: debug`), and `max_mb` (default 2048). Code: `LogsConfig` in [`cmd/homelabd/config.go`](../../cmd/homelabd/config.go). It reaches homelab-services as `HOMELAB_SERVICES_LOGS` ([`internal/adapters/services/services.go`](../../internal/adapters/services/services.go)).
- A bad value stops Homelab from starting, and the error names the setting.

**Who may do what:**

- The owner and admins in the companion see everything.
- An admin key needs `logs:read` (`ScopeLogsRead` in [`internal/hub/keys.go`](../../internal/hub/keys.go)). With it, it sees every app.
- An app's own key always has `logs:read` (`appKeyScopes` in [`internal/hub/keys.go`](../../internal/hub/keys.go)). It sees its own app and environment, and Homelab's events about them.
- A key without `logs:read` gets 403.
- Only Homelab itself can post lines (`/internal/logs`).

Full list of what each service writes, and the level table: [platform-services.md, Logs](../platform-services.md#logs).

## Deep dive

- **Files**

| File | What it does |
|---|---|
| [`internal/adapters/docker/logs.go`](../../internal/adapters/docker/logs.go) | Follows the running app containers and reads their logs. Keeps the cursors. |
| [`internal/adapters/docker/logs_test.go`](../../internal/adapters/docker/logs_test.go) | Tests for the follower: frames, TTY text, cursors, batches, failing sink. |
| [`internal/hub/logs.go`](../../internal/hub/logs.go) | Hides secrets, applies `min_level`, sends the lines and Homelab's events. |
| [`internal/hub/logs_test.go`](../../internal/hub/logs_test.go) | Tests for ingest, masking, `min_level` and events. |
| [`internal/hub/actions.go`](../../internal/hub/actions.go) | `Logs` (the live view of one app, from Docker). |
| [`internal/hub/manage.go`](../../internal/hub/manage.go) | `masker`: the secret values to hide. `minMaskLen` is 6. |
| [`internal/manifest/manifest.go`](../../internal/manifest/manifest.go) | `LogsConfig`, `LogMinLevel`, and the check of `logs: min_level`. |
| [`internal/core/logs.go`](../../internal/core/logs.go) | The level names and the `Logs` interface. |
| [`internal/adapters/services/logs.go`](../../internal/adapters/services/logs.go) | Calls homelab-services for ingest and events. |
| [`internal/adapters/services/logs_test.go`](../../internal/adapters/services/logs_test.go) | Tests for ingest batches (2000 lines per call), events and failures. |
| [`internal/api/device.go`](../../internal/api/device.go) | The live app log handler (`s.logs`). |
| [`internal/api/services.go`](../../internal/api/services.go) | Forwards `/api/logs` to homelab-services. |
| [`cmd/homelabd/run.go`](../../cmd/homelabd/run.go) | Starts the follower (`FollowLogs`, with `logs-cursors.json`). |
| [`cmd/homelabd/config.go`](../../cmd/homelabd/config.go) | `LogsConfig` in `config.yaml`. |
| [`services/homelab-services/src/logs/mod.rs`](../../services/homelab-services/src/logs/mod.rs) | The store, search, `ingest`, `event`, `retain`, and the hourly job. |
| [`services/homelab-services/src/logs/api.rs`](../../services/homelab-services/src/logs/api.rs) | The routes, `scope_of` (who may read), and ingest limits. |
| [`services/homelab-services/src/logs/parse.rs`](../../services/homelab-services/src/logs/parse.rs) | Reads one line: JSON or plain text, level, time, 16 KB cut. |
| [`services/homelab-services/src/logs/tests.rs`](../../services/homelab-services/src/logs/tests.rs) | Tests for the store, search, retention and the routes. |
| [`services/homelab-services/src/main.rs`](../../services/homelab-services/src/main.rs) | Opens `logs.db` and starts the hourly job. |
| [`web/src/routes/logs/+page.svelte`](../../web/src/routes/logs/+page.svelte) | The Logs screen. |
| [`web/src/lib/logs.ts`](../../web/src/lib/logs.ts), [`web/src/lib/logs.test.ts`](../../web/src/lib/logs.test.ts) | Level names, filters, query strings. |
| [`web/src/lib/api.ts`](../../web/src/lib/api.ts) | `searchLogs`. |
| [`plugins/typescript/src/logs.ts`](../../plugins/typescript/src/logs.ts), [`plugins/typescript/test/logs.test.ts`](../../plugins/typescript/test/logs.test.ts) | The TypeScript `logs.search`. |
| [`plugins/python/src/homelab/logs.py`](../../plugins/python/src/homelab/logs.py), [`plugins/python/tests/test_logs.py`](../../plugins/python/tests/test_logs.py) | The Python `Logs.search`, and the logging formatter. |
| [`plugins/rust/src/logs.rs`](../../plugins/rust/src/logs.rs), [`plugins/rust/tests/logs.rs`](../../plugins/rust/tests/logs.rs) | The Rust `logs` search. |
| [`docs/logs.openapi.yaml`](../../docs/logs.openapi.yaml) | The full API description for `/api/logs`. |

- **Routes**
  - `GET /api/logs`: search (people and keys). Query: `q`, `app`, `env`, `service`, `level`, `min_level`, `since`, `until`, `before`, `after`, `limit`.
  - `GET /api/apps/{id}/envs/{env}/logs?lines=`: the live view of one app, straight from Docker, masked, not stored. Up to 2000 lines, 200 by default.
  - `POST /internal/logs/ingest` and `POST /internal/logs/event` (homelab-services only, from homelabd).

- **Edge cases and rules**
  - A cursor file that cannot be read starts again from the beginning, with a warning. Code: `openCursors` in [`internal/adapters/docker/logs.go`](../../internal/adapters/docker/logs.go).
  - If homelab-services does not take a batch, the lines are kept and tried again. The buffer holds 10,000 lines. Past that, the oldest are dropped, with a warning at most once a minute. Code: `follow` in [`internal/adapters/docker/logs.go`](../../internal/adapters/docker/logs.go).
  - Cursors of containers that are gone are removed after 7 days. Code: `prune` in the same file.
  - A line longer than 16 KB is cut, and `…` is added. Code: `parse.rs` and `logMaxLine` in [`internal/adapters/docker/logs.go`](../../internal/adapters/docker/logs.go).
  - Empty lines are skipped. Lines for an app Homelab does not know are dropped (`IngestLogs` in [`internal/hub/logs.go`](../../internal/hub/logs.go)).
  - Activity events about a failed action, a health check, a smoke test, or a key use are not linked to the app. Only admins see them. Code: `logEventUntrusted` in [`internal/hub/logs.go`](../../internal/hub/logs.go).
  - Secret values shorter than 6 characters are not hidden. Code: `minMaskLen` in [`internal/hub/manage.go`](../../internal/hub/manage.go).
  - Lines are hidden with the secret values that the hub knew 5 seconds earlier at most (`logMaskTTL`, tested as "remembers the secrets for five seconds"). A secret saved a moment ago can therefore show in a line for a few seconds.
  - Search returns at most 500 lines per page (`MAX_LIMIT` in `mod.rs`).

- **To change it**
  - To change how a line's level is read, edit `parse.rs`, then run `make check-services`.
  - To change how often containers are checked, the batch size or the buffer, edit the constants at the top of [`internal/adapters/docker/logs.go`](../../internal/adapters/docker/logs.go), then run the Go tests below.
  - To change which Homelab events reach the logs, edit `sendLogEvent` or `logEventUntrusted` in [`internal/hub/logs.go`](../../internal/hub/logs.go), then run the Go tests below.
  - To change the 14 day keep, edit `KEEP_DAYS` in [`services/homelab-services/src/logs/mod.rs`](../../services/homelab-services/src/logs/mod.rs), then run `make check-services`. If the rule changes for users, update the Logs section of `platform-services.md` in the same PR.
  - To change the Logs screen, edit [`web/src/routes/logs/+page.svelte`](../../web/src/routes/logs/+page.svelte) and [`web/src/lib/logs.ts`](../../web/src/lib/logs.ts), then run the web tests below.

- **Test it**

```sh
make check-services
go test ./internal/adapters/docker/... ./internal/hub/... ./internal/adapters/services/... ./internal/api/...
cd web && npm ci && npm run check && npm test
cd plugins/typescript && npm ci && npm run check && npm test
pytest plugins/python
cd plugins/rust && cargo test --locked
```

The Docker follower also has an end-to-end test, which needs Docker running: `make test-docker`.
