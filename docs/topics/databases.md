# Databases

This page covers the databases: how each app gets one, copies and restores, import, and the SQL box. See [the map](../map.md) for the other topics.

## In short

- One line in `homelab.yaml` (`database: postgres` or `database: sqlite`) gives each app environment its own database. Dev and prod never share one.
- Copies are made before every promote, before an import, before a restore and before a change from the SQL box.
- Admins copy, restore and import from the **Databases** screen (`/databases`). Copy prod to dev is there too.
- The SQL box reads by default. Writes are only for a signed-in person, only on Postgres, and only on prod after a copy is made.
- Apps read their database address from `DATABASE_URL`.

## How it works

1. **homelabd** (Go) decides the engine from `database:` in `homelab.yaml` ([`internal/manifest/manifest.go`](../../internal/manifest/manifest.go)). For `postgres` it runs a `postgres:17-alpine` container named `db` next to the app, with 256 MB of memory ([`internal/core/database.go`](../../internal/core/database.go), [`internal/adapters/docker/database.go`](../../internal/adapters/docker/database.go)). For `sqlite` the file is `/homelab/db/app.db`, kept in a folder the app mounts.
2. The password for Postgres is the secret `DATABASE_PASSWORD`. `DATABASE_URL` is set for the app. Homelab keeps both, and apps can't list them as their own secrets.
3. homelabd tells **homelab-services** where each database is (`Register` in [`internal/adapters/services/databases.go`](../../internal/adapters/services/databases.go), `/internal/db/...`). homelab-services keeps that list in `<home>/services/dbs.db` ([`services/homelab-services/src/dbs.rs`](../../services/homelab-services/src/dbs.rs)), so it knows them after a restart.
4. Copies are folders named `<time>-<label>` under `<home>/backups/<app>/<env>/` ([`internal/hub/backupdata.go`](../../internal/hub/backupdata.go), [`internal/hub/dbcopies.go`](../../internal/hub/dbcopies.go)). A Postgres copy is a `pg_dump -Fc` file (`dbDumpFile`). A SQLite copy is a consistent snapshot of the file, taken by homelab-services (`Snapshot`).
5. Only the newest `KeepBackups` copies are kept per environment (default 5). Copies made before SQL changes are counted on their own, so they never push out the others (`pruneCopies`).
6. Copy, restore and import all stop the app, replace the database, and start the app again. If the load fails, the copy taken just before is put back (`replaceDatabase` in [`internal/hub/dbcopies.go`](../../internal/hub/dbcopies.go)).
7. Promote copies prod's data first, then deploys. If the new version does not stay healthy, the copy is restored ([`internal/hub/actions.go`](../../internal/hub/actions.go)).
8. Import ([`internal/hub/dbimport.go`](../../internal/hub/dbimport.go)) reads the uploaded file to disk, checks what it is, and loads it. What it is decides the load: a SQLite file, a `pg_dump -Fc` dump, or a plain `.sql` script. A plain script runs in one transaction.
9. The SQL box and the Databases screen read through homelab-services (`/api/db/...`), which connects to Postgres over loopback as a read-only role, or opens the SQLite file read only.

**Settings:**

- `homelab.yaml`, `database:` is `postgres`, `sqlite`, or `{engine: sqlite, path: ...}` to use a SQLite file the app already keeps in one of its `data:` folders. Code: `readDatabase` in [`internal/manifest/manifest.go`](../../internal/manifest/manifest.go).
- `config.yaml`, copies kept per environment: `KeepBackups`, default 5 ([`internal/hub/hub.go`](../../internal/hub/hub.go)).

**Who may do what:**

| Caller | Can |
|---|---|
| The owner or an admin, in the companion | Everything: copies, restore, import, copy prod to dev, SQL reads, SQL writes on Postgres |
| An admin key with `db:read` | Look inside every database (read only). `ScopeDBRead` in [`internal/hub/keys.go`](../../internal/hub/keys.go) |
| An app's own key | Its own app and environment only. Other databases answer 404. Apps can't copy, import or write |

Copying, restoring and importing are admin actions. Each one goes in the activity log. Import is only in the companion, not in the API.

Full list of calls: [db.openapi.yaml](../db.openapi.yaml). Full description: [platform-services.md, Databases](../platform-services.md#databases).

## Deep dive

- **Files**

| File | What it does |
|---|---|
| [`internal/manifest/manifest.go`](../../internal/manifest/manifest.go) | Reads `database:` in both forms, and checks it (`readDatabase`, `checkDatabase`, `SQLiteFile`). |
| [`internal/core/database.go`](../../internal/core/database.go) | The engine names, the image, the memory, the password and URL names, and the paths in the app. |
| [`internal/adapters/docker/database.go`](../../internal/adapters/docker/database.go) | Runs `pg_dump`, restore and `psql` inside the `db` container with `docker compose exec`. |
| [`internal/adapters/docker/compose.go`](../../internal/adapters/docker/compose.go) | Writes the `db` service into the app's compose file, and makes the app wait for it. |
| [`internal/adapters/services/databases.go`](../../internal/adapters/services/databases.go) | Calls homelab-services for register, forget and snapshot. |
| [`internal/adapters/services/databases_test.go`](../../internal/adapters/services/databases_test.go) | Tests for those calls. |
| [`internal/hub/dbcopies.go`](../../internal/hub/dbcopies.go) | Copies (`copyNow`), restore, copy prod to dev, `replaceDatabase`, and the copy list. |
| [`internal/hub/dbimport.go`](../../internal/hub/dbimport.go) | Import: the upload, the kind of file, the load. `MaxImport` is 50 GB. |
| [`internal/hub/backupdata.go`](../../internal/hub/backupdata.go) | Copy folders, the file copy with the app's data, and `pruneCopies`. |
| [`internal/hub/actions.go`](../../internal/hub/actions.go) | Promote and rollback: prod's data is copied first, and restored if the new version fails. |
| [`internal/hub/hub.go`](../../internal/hub/hub.go) | `KeepBackups` and the hub's other settings. |
| [`internal/hub/database_test.go`](../../internal/hub/database_test.go) | Tests for Postgres spec, promote, copies, restore, copy prod to dev. |
| [`internal/hub/dbadopt_test.go`](../../internal/hub/dbadopt_test.go) | Tests for the SQLite file the app already keeps, and for import. |
| [`internal/api/apps.go`](../../internal/api/apps.go) | The companion's calls: `appDatabase`, `makeDBCopy`, `restoreDBCopy`, `copyProdToDev`, `importDB`. |
| [`internal/api/services.go`](../../internal/api/services.go) | `copyBeforeWrite`: copies prod before a write from the SQL box, then forwards `/api/db`. |
| [`internal/api/api.go`](../../internal/api/api.go) | The routes for the companion's database calls. |
| [`services/homelab-services/src/dbs.rs`](../../services/homelab-services/src/dbs.rs) | The registry (`dbs.db`), Postgres and SQLite connections, `query`, row limits, `snapshot`. Tests in the same file. |
| [`services/homelab-services/src/dbs_api.rs`](../../services/homelab-services/src/dbs_api.rs) | The routes, the access rules (`find`, `viewer`), the write checks in `query`, and the change list. Tests are in `dbs_api_tests.rs`. |
| [`services/homelab-services/src/dbs_sql.rs`](../../services/homelab-services/src/dbs_sql.rs) | A small SQL reader. Splits statements and finds risky ones (`DROP`, `TRUNCATE`, `ALTER`, `GRANT`, `REVOKE`, and `DELETE` or `UPDATE` with no `WHERE`). Tests in the same file. |
| [`services/homelab-services/src/db.rs`](../../services/homelab-services/src/db.rs) | Not the databases feature. It is the metadata store for Files (`files.db`). Listed only so you do not confuse it with `dbs.rs`. |
| [`web/src/routes/databases/+page.svelte`](../../web/src/routes/databases/+page.svelte) | The Databases list. |
| [`web/src/routes/databases/[app]/[env]/+page.svelte`](../../web/src/routes/databases/[app]/[env]/+page.svelte) | One database: tables, rows, SQL, copies, import, and the "Run it?" prompt for prod. |
| [`web/src/lib/databases.ts`](../../web/src/lib/databases.ts), [`web/src/lib/databases.test.ts`](../../web/src/lib/databases.test.ts) | Helpers for the Databases screen. |
| [`plugins/typescript/src/db.ts`](../../plugins/typescript/src/db.ts), [`plugins/typescript/test/db.test.ts`](../../plugins/typescript/test/db.test.ts) | The TypeScript `homelab.db` (read only). |
| [`plugins/python/tests/test_db.py`](../../plugins/python/tests/test_db.py) | Tests for the Python `db` client. |
| [`plugins/rust/src/db.rs`](../../plugins/rust/src/db.rs), [`plugins/rust/tests/db.rs`](../../plugins/rust/tests/db.rs) | The Rust `db` client (read only). |
| [`docs/db.openapi.yaml`](../../docs/db.openapi.yaml) | The full API description. |

- **Routes**
  - homelab-services, under `/api/db`: `GET /api/db` (every database), `GET /api/db/{app}/{env}`, `GET /api/db/{app}/{env}/tables`, `GET /api/db/{app}/{env}/tables/{table}/rows` (`limit` 1 to 200, `offset`), `POST /api/db/{app}/{env}/query` with `{"sql", "write", "confirm"}`, and `GET /api/db/{app}/{env}/changes` (the change list).
  - homelab-services, internal only: `PUT /internal/db/{app}/{env}` (register), `DELETE /internal/db/{app}` (forget), and a snapshot route for copies.
  - homelabd, companion only: `GET /api/apps/{id}/db`, `POST /api/apps/{id}/envs/{env}/db/copies`, `POST .../db/copies/{name}/restore`, `POST /api/apps/{id}/db/copy-prod-to-dev`, `POST /api/apps/{id}/envs/{env}/db/import`. Routes are in [`internal/api/api.go`](../../internal/api/api.go).

- **Edge cases and rules**
  - **SQL box, reads:** Postgres reads run as the role `homelab_ro`, never as `app`. SQLite opens read only (`SQLITE_OPEN_READ_ONLY` in `dbs.rs`).
  - **SQL box, writes:** only a signed-in person (not a key or an app), only on Postgres, and `BEGIN` or `COMMIT` in the SQL is refused (`query` in `dbs_api.rs`). The SQL is at most 100 KB (`MAX_SQL`).
  - **Risky SQL:** a risky statement (see `risky` in `dbs_sql.rs`) needs the environment's name typed in the confirm box. Without it the answer is 409 with `needs_confirm`. The companion asks "This changes prod's data. Run it?" ([`web/src/routes/databases/[app]/[env]/+page.svelte`](../../web/src/routes/databases/[app]/[env]/+page.svelte)).
  - **Copy before a prod write:** `copyBeforeWrite` in [`internal/api/services.go`](../../internal/api/services.go) copies prod first (`before-change`). If the copy fails, nothing runs ("Couldn't make a copy first, so nothing was changed."). Dev writes get no copy.
  - **Limits:** a query runs for at most 10 seconds (`QUERY_LIMIT`), shows at most 500 rows (`MAX_ROWS`), and a connection waits at most 2 seconds (`CONNECT_LIMIT`).
  - **Copy prod to dev:** dev's own database is copied first (`before-copy-from-prod`). If the copy fails, dev is put back as it was (`copyProdToDev`).
  - **Restore:** a copy of the current data is made first (`before-restore`). If the load fails, the copy is put back.
  - **Import:** the file is at most 50 GB (`MaxImport`). The copy `before-import` is made first. A file that does not fit the engine is refused before the app stops.
  - **Copy names:** a copy is labelled `manual`, `promote`, `before-restore`, `before-copy-from-prod`, `before-import` or `before-change` ([`internal/hub/dbcopies.go`](../../internal/hub/dbcopies.go)).
  - **SQLite in the app's own folder:** a copy stores the file as `homelab-db/app.db`, whatever the file is called (`DatabaseFolder` and `DatabaseSQLiteFile` in [`internal/core/database.go`](../../internal/core/database.go)).
  - **Postgres password:** made once per environment, and never changed by Homelab.

- **To change it**
  - To change the Postgres image or memory, edit `DatabaseImage` and `DatabaseMemory` in [`internal/core/database.go`](../../internal/core/database.go), then run the Go tests below. This changes the containers of every Postgres app on the next deploy.
  - To change the query limits or the row cap, edit `QUERY_LIMIT`, `MAX_ROWS` or `MAX_SQL` in [`services/homelab-services/src/dbs.rs`](../../services/homelab-services/src/dbs.rs), then run `make check-services`.
  - To change which SQL needs the confirm, edit `risky` in [`services/homelab-services/src/dbs_sql.rs`](../../services/homelab-services/src/dbs_sql.rs). Add a test in the same file for each new case.
  - To change how many copies are kept, edit `KeepBackups` ([`internal/hub/hub.go`](../../internal/hub/hub.go)), then run the Go tests below.
  - To change the import limit, edit `MaxImport` in [`internal/hub/dbimport.go`](../../internal/hub/dbimport.go).

- **Test it**

```sh
make check-services
go test ./internal/hub/... ./internal/api/... ./internal/adapters/services/... ./internal/adapters/docker/...
cd web && npm ci && npm run check && npm test
pytest plugins/python
cd plugins/typescript && npm ci && npm run check && npm test
cd plugins/rust && cargo test --locked
```

The Docker adapter's end-to-end test needs Docker running: `make test-docker`.
