# Files

This page covers the Files feature: where files are kept, who may use them, and where the code lives. See [the map](../map.md) for the other topics.

## In short

- Keeps files (photos, videos, audio, PDFs and any other file) in folders on the device's disks.
- Each folder belongs to one app environment (like `movies` on `prod`), or to the admins.
- Reads basic details from each file (size, date taken, camera, GPS, title) and returns them as `meta`.
- Admins see every folder, and how full each disk is, on the **Files** screen (`/files`).
- A deleted file goes to a trash for 30 days, then it is removed for good.

## How it works

1. The companion's Files screen ([`web/src/routes/files/`](../../web/src/routes/files/)) and apps both call `/api/files/...`. Apps use the app door (`HOMELAB_API_URL`) with their own key.
2. **homelabd** (Go) decides who is calling. It forwards `/api/files` to **homelab-services** (Rust) and signs the call with the caller's identity. See `servicePaths` in [`internal/api/services.go`](../../internal/api/services.go).
3. homelab-services checks access in `allowed` in [`services/homelab-services/src/files.rs`](../../services/homelab-services/src/files.rs). A person may do anything. An admin key needs `files:read` or `files:write`. An app's key may use only its own folders. Other folders look like they don't exist (404).
4. An upload streams into a temp file in `<disk>/homelab-files/.tmp`. The size is checked against `max_upload_mb` as it arrives, and the SHA-256 is worked out. If the `sha256` query value is given and does not match, the upload is refused.
5. The file is moved to `<disk>/homelab-files/<folder>/<year>/<month>/<id>-<name>`. The name keeps only letters, digits, `.`, `_` and `-`. If the folder already holds the same bytes, nothing new is stored, and the answer has `X-Homelab-Duplicate: true`.
6. The type of a file comes from its bytes, not from the name or the client's type.
7. `meta.rs` reads photo, video, audio and PDF details with memory-safe parsers only. It gives up after 10 seconds, and reads at most 64 MB of a file (100 MB for a PDF). A file it can't read gets `{}`.
8. Folder and file records are kept in `<home>/services/files.db` (SQLite, see `db.rs`).
9. Delete moves a file to the trash, so it is hidden at once. Once an hour, a purge job (`spawn_purge` in `files.rs`) removes trashed files older than 30 days, and clears old temp files.
10. `GET /api/storage` ([`internal/hub/disks.go`](../../internal/hub/disks.go)) lists the data disk, every `storage:` disk and every `folders:` entry, with used and total space. Each disk above 85% full raises a `disk:full` alert, checked every 5 minutes ([`internal/hub/alerts.go`](../../internal/hub/alerts.go)).

**Settings (`config.yaml`):**

- `storage:` is a list of disks, each with a `name` and an absolute `path`. Without it, there is one disk, `main`, at `<home>/storage`. The first disk is the default for new folders. Code: `Storage` in [`cmd/homelabd/config.go`](../../cmd/homelabd/config.go).
- `max_upload_mb:` is the largest upload, in MB. Default 10240 (10 GB). It must be between 1 and 1048576. Code: `MaxUploadMB` in [`cmd/homelabd/config.go`](../../cmd/homelabd/config.go).
- `folders:` lists shared folders. The Files screen shows them and the disk alert checks them. They are not app storage.
- homelabd passes the disks and the limit to homelab-services as `HOMELAB_SERVICES_STORAGE` and `HOMELAB_SERVICES_MAX_UPLOAD` ([`internal/adapters/services/services.go`](../../internal/adapters/services/services.go)).

**Who may do what:**

| Caller | Can |
|---|---|
| The owner or an admin, in the companion | Everything |
| An admin key with `files:read` | Read every folder's files |
| An admin key with `files:write` | Also make folders, save, change and delete files |
| An app's own key | Its own folders only. No scope is needed, because homelabd gives app keys no file scope |

Scope names are `ScopeFilesRead` and `ScopeFilesWrite` in [`internal/hub/keys.go`](../../internal/hub/keys.go). A folder can be deleted only when it is empty. Folders can't be renamed (there is no route for it).

Full list of calls: [files.openapi.yaml](../files.openapi.yaml). Full description, with examples: [platform-services.md, Files](../platform-services.md#files).

## Deep dive

- **Files**

| File | What it does |
|---|---|
| [`services/homelab-services/src/files.rs`](../../services/homelab-services/src/files.rs) | Folders, upload, download (with `Range`), list, info, tags, delete, trash, purge, and `allowed` (who may use what). |
| [`services/homelab-services/src/files_tests.rs`](../../services/homelab-services/src/files_tests.rs) | Tests for the routes, access rules, uploads, ranges, trash and folder delete. Loaded by `files.rs`. |
| [`services/homelab-services/src/meta.rs`](../../services/homelab-services/src/meta.rs) | Reads size, dates, camera, GPS, title and page count from each file. Tests are in the same file. |
| [`services/homelab-services/src/disk.rs`](../../services/homelab-services/src/disk.rs) | Name checks (`valid_name`, `safe_name`), folder creation, and the year and month of a file. Tests are in the same file. |
| [`services/homelab-services/src/db.rs`](../../services/homelab-services/src/db.rs) | The folder and file records in `files.db`. Tests are in the same file. |
| [`services/homelab-services/src/config.rs`](../../services/homelab-services/src/config.rs) | Reads `HOMELAB_SERVICES_STORAGE` and `HOMELAB_SERVICES_MAX_UPLOAD`. |
| [`services/homelab-services/src/main.rs`](../../services/homelab-services/src/main.rs) | Opens `files.db` and starts the purge job. |
| [`cmd/homelabd/config.go`](../../cmd/homelabd/config.go) | `storage:`, `folders:` and `max_upload_mb:` in `config.yaml`. |
| [`internal/adapters/services/services.go`](../../internal/adapters/services/services.go) | Starts homelab-services with the disks and the upload limit. |
| [`internal/api/services.go`](../../internal/api/services.go) | Forwards `/api/files` to homelab-services, and `storage` (the disk list). |
| [`internal/api/api.go`](../../internal/api/api.go) | The route `GET /api/storage`. |
| [`internal/hub/disks.go`](../../internal/hub/disks.go) | `Disks`: the disk list and how full each one is. |
| [`internal/hub/alerts.go`](../../internal/hub/alerts.go), [`internal/hub/alerts_test.go`](../../internal/hub/alerts_test.go) | The `disk:full` alert, at 85%. |
| [`internal/hub/keys.go`](../../internal/hub/keys.go) | The file scope names, and the scopes an admin key may have. |
| [`internal/api/services_test.go`](../../internal/api/services_test.go) | Tests for the forwarding of `/api/files`. |
| [`web/src/routes/files/+page.svelte`](../../web/src/routes/files/+page.svelte), [`web/src/routes/files/[folder]/+page.svelte`](../../web/src/routes/files/[folder]/+page.svelte) | The Files screen and one folder. |
| [`web/src/lib/files.ts`](../../web/src/lib/files.ts), [`web/src/lib/files.test.ts`](../../web/src/lib/files.test.ts) | Helpers for the Files screen. |
| [`web/src/lib/api.ts`](../../web/src/lib/api.ts) | The file calls (`fileFolders`, `files`, `uploadFile`, `fileBlob`, `deleteFile`). |
| [`web/src/lib/keys.ts`](../../web/src/lib/keys.ts) | The plain words for the file scopes. |
| [`plugins/typescript/src/files.ts`](../../plugins/typescript/src/files.ts), [`plugins/typescript/test/files.test.ts`](../../plugins/typescript/test/files.test.ts) | The TypeScript `homelab.files`. |
| [`plugins/python/src/homelab/files.py`](../../plugins/python/src/homelab/files.py), [`plugins/python/tests/test_files.py`](../../plugins/python/tests/test_files.py), [`plugins/python/tests/test_folders.py`](../../plugins/python/tests/test_folders.py) | The Python `Files` class. |
| [`plugins/rust/src/files.rs`](../../plugins/rust/src/files.rs), [`plugins/rust/tests/files.rs`](../../plugins/rust/tests/files.rs) | The Rust `Files` client. |
| [`docs/files.openapi.yaml`](../../docs/files.openapi.yaml) | The full API description. |

- **Routes** (all under `/api/files`, served by homelab-services)
  - `GET /api/files/folders`: the folders the caller may see.
  - `POST /api/files/folders` with `{"name", "disk"?}`: make a folder. Name must be lowercase letters, digits, `_` or `-`, up to 63 characters. A name already in use gives 409.
  - `DELETE /api/files/folders/{name}`: delete an empty folder. A folder with files gives 409.
  - `GET /api/files/{folder}`: list files, newest first, 50 at a time. Filters: `tag`, `type`, `taken_after`, `taken_before`, `camera`, `after`, `limit`.
  - `POST /api/files/{folder}?name=&tags=&sha256=`: save a file. The body is the file itself.
  - `GET /api/files/{folder}/{id}`: download. Supports `Range`. Always sent as an attachment.
  - `GET` and `PATCH /api/files/{folder}/{id}/info`: read, or change `tags` and `info`.
  - `DELETE /api/files/{folder}/{id}`: move to the trash.
  - `GET /api/storage` (homelabd): the disks, for the Files screen.

- **Edge cases and rules**
  - A folder name that is not valid, or that the caller may not use, gives 404 (`folder_for` in `files.rs`).
  - At most 20 tags per file, each cleaned (`clean_tags` in `files.rs`).
  - `info` must be a JSON object of at most 4096 bytes.
  - JSON request bodies are limited to 64 KB (`JSON_LIMIT` in `files.rs`). Uploads have no body limit of their own, only `max_upload_mb`.
  - A symlinked storage folder is refused, and a symlinked file is not served (tests in `files_tests.rs`).
  - Metadata problems never fail an upload. The file is kept with `meta: {}` (`meta.rs`).
  - The client cannot pick the type or the metadata (test `the_client_cannot_choose_the_type_or_meta`).
  - Trashed files are hidden from every list and download at once, and deleted for good after 30 days (`TRASH_KEEP` in `files.rs`).
  - Folders created by an app belong to that app and environment. Folders made by an admin have no owner (`owner_app` empty).

- **To change it**
  - To change the default upload limit, edit `defaultMaxUploadMB` in [`cmd/homelabd/config.go`](../../cmd/homelabd/config.go), then run the Go tests below.
  - To change how long trashed files are kept, edit `TRASH_KEEP` in [`services/homelab-services/src/files.rs`](../../services/homelab-services/src/files.rs), then run `make check-services`.
  - To change how metadata is read, or its time and size limits (`TIMEOUT`, `MAX_READ`, `MAX_PDF`), edit [`services/homelab-services/src/meta.rs`](../../services/homelab-services/src/meta.rs), then run `make check-services`.
  - To change who may do what, edit `allowed` in `files.rs` and the scope names in [`internal/hub/keys.go`](../../internal/hub/keys.go). Test both.
  - To change the disk-full level (85%), edit `diskFullAt` in [`internal/hub/alerts.go`](../../internal/hub/alerts.go), then run the Go tests below.
  - To change the Files screen, edit [`web/src/routes/files/`](../../web/src/routes/files/) and [`web/src/lib/files.ts`](../../web/src/lib/files.ts), then run the web tests below.

- **Test it**

```sh
make check-services
go test ./internal/adapters/services/... ./internal/hub/... ./internal/api/...
cd web && npm ci && npm run check && npm test
cd plugins/typescript && npm ci && npm run check && npm test
pytest plugins/python
cd plugins/rust && cargo test --locked
```
