# Homelab plugins

Small libraries that let your code use Homelab's services in one line, for example
`await homelab.files.upload("photos", bytes, { name: "cat.jpg" })`.

- **Thin.** All the real work (storing files, reading their info, checking access) is done by
  Homelab's Rust service. A plugin only makes the web calls, so every language behaves the same.
- **No setup inside Homelab.** In a Homelab app, `HOMELAB_API_URL` and `HOMELAB_TOKEN` are already
  set and the plugins read them. A server elsewhere passes the companion's address and a key from
  **Settings → API keys**.
- **Safe by default.** The key is never printed (not in errors, not in debug output), redirects are
  never followed (the key can't travel to another place), and `.` / `..` names are refused before
  anything is sent.

| Language | Folder | Install (until they're published) |
|---|---|---|
| TypeScript / JavaScript | [typescript](typescript) | `npm install /path/to/homelab/plugins/typescript` |
| Python 3.9+ | [python](python) | `pip install "git+https://github.com/rupaai/homelab#subdirectory=plugins/python"` |
| Rust | [rust](rust) | `homelab-sdk = { git = "https://github.com/rupaai/homelab" }` |

Each folder's README has a quick start and every call. Services so far: **files** and
**secrets**. Databases and data come next, as `homelab.db` and `homelab.data`.

## The contract every plugin follows

requests, reading answers, and turning errors into one error type. The source of truth is
docs/files.openapi.yaml, docs/secrets.openapi.yaml and docs/platform-services.md.

## Config
- `url` and `token`. Default to env `HOMELAB_API_URL` and `HOMELAB_TOKEN`. Missing either
  -> a clear error at construction ("Set HOMELAB_API_URL or pass url").
- Trailing slash on url is fine. Every request sends `Authorization: Bearer <token>`.
- The token is never printed: not in errors, not in debug/repr output.
- Optional timeout (default 60 s for normal calls; none for upload/download bodies).

## Errors
One error type `HomelabError` with `status` (HTTP status, 0 for network errors) and
`message` (the server's `{"error": "..."}` text, or a plain fallback).

## Files API surface (names per language style)
client.files.folders() -> Folder[]                          GET    /api/files/folders
client.files.createFolder(name, {disk?}) -> Folder          POST   /api/files/folders  (JSON)
client.files.ensureFolder(name, {disk?}) -> Folder          create; on 409 return the existing one from folders()
client.files.deleteFolder(name) -> void                     DELETE /api/files/folders/{name}
client.files.upload(folder, data, {name, tags?, sha256?}) -> {file, duplicate}
                                                             POST   /api/files/{folder}?name=&tags=a,b&sha256=
    data: bytes / file path / stream (per language); body is the raw bytes, never JSON or multipart.
    duplicate = response header X-Homelab-Duplicate == "true" (status 200 instead of 201).
client.files.list(folder, {tag?, type?, takenAfter?, takenBefore?, camera?, after?, limit?})
    -> {files, next}                                         GET    /api/files/{folder}
client.files.all(folder, filters) -> iterator over every file, following `next` as `after`
client.files.info(folder, id) -> File                        GET    /api/files/{folder}/{id}/info
client.files.update(folder, id, {tags?, info?}) -> File      PATCH  /api/files/{folder}/{id}/info (JSON)
client.files.download(folder, id, {range?}) -> bytes / stream   GET /api/files/{folder}/{id}
client.files.delete(folder, id) -> void                      DELETE /api/files/{folder}/{id}

Path parts (folder, id) are URL-encoded. Query values are URL-encoded.

## Secrets API surface (names per language style)
client.secrets.get(name, {version?, fresh?}) -> string      GET    /api/secrets/{name}[?version=n]
    Without `version`, and unless `fresh`, a file `<dir>/<name>` is read first (dir defaults to
    /run/secrets, configurable on the client). Only names matching ^[A-Za-z_][A-Za-z0-9_]*$
    are looked up on disk. No file -> ask Homelab. `version` or `fresh` always ask Homelab.
    The file is returned exactly as written. Any other file error -> HomelabError (status 0).
client.secrets.getOrNull(name, {version?, fresh?}) -> string | null
    Same as get, but a 404 gives null (None / Option::None). Other errors still raise.
    Names: getOrNull (TS), get_or_none (Python), get_opt (Rust).
client.secrets.set(name, value) -> {name, version}          PUT    /api/secrets/{name} {"value": "..."}
client.secrets.delete(name) -> void                         DELETE /api/secrets/{name}  (204; absent is fine)
client.secrets.list() -> SecretInfo[]                       GET    /api/secrets  -> {secrets: [...]}
client.secrets.versions(name) -> SecretVersion[]            GET    /api/secrets/{name}/versions  -> {versions: [...]}

Names are checked before anything is sent or read: 1 to 200 of A-Z a-z 0-9 _ . : - (the
server's rule), and "." / ".." are refused. A bad name is a HomelabError with status 0.
A version must be a whole number from 1. Path parts are URL-encoded (":" becomes %3A).
A secret value is never put in an error, a log line or repr/debug output. The plugins
also blank the value in an error message if a server ever echoed it back.
`set` and `delete` need the `secrets:write` scope (`secrets_api: write` in homelab.yaml); a
missing scope is a 403.

## Types
Folder { name, disk, app, env, files, bytes, created_at }
File   { id, folder, name, content_type, size, sha256, created_at, created_by, tags[], info{}, meta{} }
Meta   { width?, height?, taken_at?, camera_make?, camera_model?, lens?, orientation?,
         gps?{lat, lon, alt?}, duration_s?, title?, artist?, album?, year?, pages?, author? }
SecretInfo    { name, kind ("config" | "app"), version, updated_at }   (never a value)
SecretVersion { version, set_at }                                      (never a value)
Saved         { name, version }
Unknown fields from the server are kept/ignored, never an error (forward compatible).
