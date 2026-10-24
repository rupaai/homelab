# Platform services

Homelab gives apps, and servers outside Homelab, a small API: people (here), and later files, databases and data. Every call uses an **API key**.

## API keys

A key looks like `hlk_` plus 43 characters. Homelab keeps only a SHA-256 of it, so a lost key can't be shown again: make a new one.

There are two kinds.

**App keys.** Every app environment gets one automatically, in two settings:

- `HOMELAB_TOKEN`: the key.
- `HOMELAB_API_URL`: where to use it, `http://host.docker.internal:7085`.

An app key acts for its own app and environment only. It can read the people of its app and its own release versions. Later services decide what else it may touch from the app and environment it belongs to, so a dev key never gets prod's data. It only works at `HOMELAB_API_URL`, not on the companion's address.

An admin can revoke an app key under **Settings → API keys**, for example if it leaked. The app gets a new one the next time it's deployed.

**Admin keys.** For a server outside Homelab, such as a photo service on another computer. An admin makes one in the companion under **Settings → API keys**, names it, and copies it once. It works on the companion's own address. It stops working when it's revoked, when it expires, or when the person who made it is no longer an admin.

### Scopes

- `users:read`: read people.
- `files:read`: read every folder's files.
- `files:write`: also make folders, and save, change and delete files.
- `db:read`: look inside every database (read only).
- `releases:read`: list the app's own release versions. Only app keys have it (every app key does); admin keys can't.
- `logs:read`: search the logs. Every app key has it, and sees only its own app and environment. An admin key can have it too, to read every app's lines.

App keys need no files scope: an app always has its own folders, and only those. Databases and data add their own scopes when they arrive.

### The app door

Apps call Homelab on their own door, separate from the companion. The door takes app keys only: no sign-in, no cookies, no companion.

- **Linux:** the door listens on Docker's bridge address (usually `172.17.0.1`). Apps reach it as `host.docker.internal`. It isn't meant for the home network: another computer could only reach it by adding a route by hand, and even then it needs an app key.
- **Mac:** Docker runs in a small virtual machine that already forwards `host.docker.internal` to the Mac. The door listens on `127.0.0.1:7085`.

Change the port with `app_api_port: 7085` in `config.yaml`.

Apps still can't reach each other: each app environment keeps its own private network.

### Rules for every key call

- Send the key as `Authorization: Bearer <key>`.
- Cookies and `X-Homelab-*` headers on a key call are dropped.
- Every key call goes in the activity log. If a key makes the same read again and again, it's logged once every 10 minutes. Every change is always logged.

## Users API

| Call | Gives |
|---|---|
| `GET /api/users/me` | who is calling: an app (`{"kind":"app","app","env"}`), or an admin key and the person who made it |
| `GET /api/users?app=<id>` | the people with access to an app, with `access`: `admin`, `build` or `use`. Without `app`, everyone (admin keys only) |

An app key always gets its own app, and may leave `app` out.

```sh
# From inside an app container
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/users"
```

## homelab-services

Files, databases and data are served by a second program, `homelab-services`, written in Rust. Its code is in `services/`.

- homelabd starts it from next to itself (`bin/homelab-services`, or `homelab-services-<version>` on the device), restarts it if it stops, and stops it on shutdown. If it isn't running, the device card under Resources says so.
- It listens on `127.0.0.1:7086` only. Change the port with `services_port: 7086` in `config.yaml`.
- Nothing calls it directly. Apps, keys and the companion call Homelab at `/api/files`, `/api/db` and `/api/data`. Homelab checks who is calling and forwards the call.
- Every forwarded call carries who it is for (`X-Homelab-Caller`) and a signature (`X-Homelab-Signature`). homelabd makes a new signing key at every start and gives the program only the public half. The program refuses calls that are unsigned, more than 30 seconds old, or signed for another method or path. Homelab removes any `X-Homelab-*` headers, cookies and the key from the incoming call first.
- Releases ship it for Linux (amd64 and arm64). Updating Homelab updates it too. On the Mac, `make build` builds it.
- Data comes in the next change.

Checks: `make check-services` (format, lint, tests).

## Release versions

`homelab-services release` picks the next version of a repo, writes the release notes and makes the GitHub release. It does not need Homelab, so you can run it in any repo's CI.

```sh
homelab-services release <prod|dev|test> --repo owner/name --commit <sha> \
  [--pr N --title "..."] [--first vX.Y.Z] [--always] [--dry-run]
```

- It needs `GITHUB_TOKEN` (a token that can write releases). `GITHUB_API_URL` is optional (default `https://api.github.com`). The token is never printed.
- It prints the version on stdout (one line) and the notes on stderr. With `--dry-run` it creates nothing.
- If nothing needs a release, it prints nothing, says so on stderr and exits 0.
- `prod` makes `vX.Y.Z`. `dev` makes `vX.Y.Z-dev.N`. `test` makes `vX.Y.Z-pr.N` for pull request N (it needs `--pr` and `--title`). Dev and test are GitHub pre-releases.
- The version comes from the PR titles since the last `vX.Y.Z` release: `feat:` raises the middle number, `fix:` (or a title with no prefix) the last one. `feat!:`, any `type!:`, or `BREAKING CHANGE` in the body is breaking: it raises the first number, or the middle one while the version is below 1.0. `docs:`, `chore:`, `test:`, `ci:`, `style:`, `refactor:` and `build:` need no release, unless you pass `--always` (then the last number goes up).
- The first release is `--first`, else the version in `pyproject.toml`, `package.json` or `Cargo.toml` at that commit, else `v0.1.0`.
- Running it twice for the same commit gives the same release. If the tag is on another commit, it stops with an error. A test version is tied to its pull request number, so a second test release of the same pull request on a newer commit stops with that error.

### Versions for every app Homelab runs

You don't have to do anything in the app's repo. Homelab makes the versions itself, with the GitHub token it already has (`GITHUB_TOKEN`; for releases it needs **Contents: read and write**).

- **Dev:** before a new build goes on dev, Homelab makes a GitHub pre-release such as `v0.4.0-dev.1`. The next build is `v0.4.0-dev.2`, until prod catches up.
- **Prod:** a promote makes the release `v0.4.0` on the same commit that dev ran, and prod runs it. The promote sheet says "Promote v0.4.0-dev.2 to prod as v0.4.0", with a line like "2 new, 1 fix" and the notes.
- **Apps that already run:** if an app has no `vX.Y.Z` release yet and prod runs a commit, Homelab makes the first release for that commit once (from `pyproject.toml`, `package.json` or `Cargo.toml`, else `v0.1.0`), so the next dev build continues from it. The running prod container gets its `HOMELAB_VERSION` at its next deploy; nothing is restarted for a label.
- **Only the app's container** gets `HOMELAB_VERSION`, not its extra containers. An app with a single environment gets prod releases for its deploys.
- **Never in the way:** if GitHub says no (for example the token can't write releases), the app is deployed anyway, without a version. The app's page says "Can't make versions on GitHub: the token needs Contents: read and write." and the activity log has a line. Fix the token and the next build gets a version.
- A rollback brings back the old version with the old build. The version is kept on the deploy record and on the environment.

In the app, `HOMELAB_VERSION` holds it (unset when there is none). The plugins read it: `homelab.version()` (TypeScript), `homelab.version()` / `from homelab import version` (Python), `homelab::version()` (Rust).

An app can list its own versions with its own key (scope `releases:read`):

| Call | Gives |
|---|---|
| `GET /api/releases?limit=` | `{"releases": [{"tag","prerelease","commit","date","summary","notes"}]}`, newest first. Dev builds are pre-releases. `limit` is optional |

```sh
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/releases?limit=5"
```

```ts
// TypeScript
const homelab = new Homelab();
console.log(homelab.version());                  // "v0.4.0", or null
const releases = await homelab.releases.list({ limit: 5 });
```

```python
# Python
homelab = Homelab()
print(homelab.version())                         # "v0.4.0", or None
releases = homelab.releases.list(limit=5)
```

```rust
// Rust
let homelab = Homelab::from_env()?;
println!("{:?}", homelab.version());             // Some("v0.4.0"), or None
let releases = homelab.releases().list(Some(5)).await?;
```

Homelab keeps the GitHub token to itself: the app never sees it. Homelab adds the app's repo and the token when it forwards the call to homelab-services, which makes the GitHub calls and never logs or returns the token. Homelab itself uses two internal routes (`/internal/releases/publish` and `/internal/releases/list`) that only it can call.

Example GitHub Actions step (a job on `main` with `contents: write`):

```yaml
- uses: actions/checkout@v4
- name: Release
  env:
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  run: |
    VERSION=$(homelab-services release prod --repo "$GITHUB_REPOSITORY" --commit "$GITHUB_SHA")
    echo "version=$VERSION" >> "$GITHUB_OUTPUT"
```

## Files

Apps, keys and admins keep files on the device's disks. Each folder belongs to one app environment (like `movies` `prod`), or to the admins.

### Disks

List the disks in `config.yaml`. Without `storage:`, files go in `storage/` inside Homelab's data folder.

```yaml
storage:
  - name: main
    path: /mnt/storage/homelab   # a folder the homelab account can write to
max_upload_mb: 10240              # the biggest upload, in MB (default 10 GB)
```

The first disk is the default for new folders. Files sit at `<disk>/homelab-files/<folder>/<year>/<month>/<id>-<name>`. On mars without `storage:` that's `/var/lib/homelab/storage/homelab-files/`; with the 1 TB disk set up, `/mnt/storage/homelab/homelab-files/`.

An empty folder can be deleted. Files in the trash are removed for good when their folder is deleted. Folders can't be renamed yet. The list of files lives in `files.db` in homelab-services' own folder.

The **Files** screen in the companion shows how full each disk is, and the folders in `folders:` too. Homelab sends the "disk almost full" alert for each of them.

### Who can do what

| Caller | Can |
|---|---|
| An admin, in the companion | everything |
| An admin key | read with `files:read`; read and change with `files:write` |
| An app key | its own folders only. Other folders look like they don't exist |

### Calls

| Call | Does |
|---|---|
| `GET /api/files/folders` | the folders you can see |
| `POST /api/files/folders` `{"name","disk"?}` | make a folder. An app's folder is always its own |
| `DELETE /api/files/folders/<folder>` | delete an empty folder (409 if it still has files). Files in its trash go with it |
| `POST /api/files/<folder>?name=&tags=a,b&sha256=` | save a file. The body is the file itself, not JSON |
| `GET /api/files/<folder>?tag=&type=image/&taken_after=&taken_before=&camera=&after=&limit=` | list files, newest first, 50 at a time. Pass `next` as `after` for more |
| `GET /api/files/<folder>/<id>` | the file. Supports `Range` |
| `GET /api/files/<folder>/<id>/info` | the file's details |
| `PATCH /api/files/<folder>/<id>/info` `{"tags"?,"info"?}` | change tags (up to 20) or `info` (any JSON object up to 4 KB) |
| `DELETE /api/files/<folder>/<id>` | move it to the trash. The trash is emptied after 30 days |

- The type of a file is worked out from its bytes, not from what the caller says. Downloads are always sent as attachments.
- Homelab reads what it can from each file and returns it as `meta`. Photos: `width`, `height`, `taken_at`, `camera_make`, `camera_model`, `lens`, `orientation`, `gps`. Videos: `width`, `height`, `duration_s`, `taken_at`, `gps`. Audio: `duration_s`, `title`, `artist`, `album`, `year`. PDFs: `pages`, `title`, `author`. A key is there only when found. This is done in Rust with memory-safe parsers only, at most 10 seconds and two files at a time; a file it can't read just gets `{}`.
- Saving a file that's already in the folder (same bytes) stores nothing new: the answer is `200` with the file already there and `X-Homelab-Duplicate: true`.
- Every call is described in [files.openapi.yaml](files.openapi.yaml), so tools can make a client from it.
- The name is cleaned: only letters, digits, `.`, `_` and `-` are kept.
- An upload is written to a temp file, synced, then moved into place, so a crash never leaves half a file.

```sh
# From inside an app container
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"posters"}' "$HOMELAB_API_URL/api/files/folders"
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" --data-binary @poster.jpg \
  "$HOMELAB_API_URL/api/files/posters?name=poster.jpg&tags=dune"
```

### Use it from your code

The easy way is a plugin: [TypeScript, Python or Rust](../plugins/README.md), for example `await homelab.files.upload("posters", bytes, { name: "poster.jpg" })`. Without a plugin it's plain web calls, so any language works; the examples below show them. Inside a Homelab app, `HOMELAB_API_URL` and `HOMELAB_TOKEN` are already set. A server elsewhere uses the companion's address and a key from **Settings → API keys**.

TypeScript (Node 18+):

```ts
import fs from "node:fs";

const api = process.env.HOMELAB_API_URL!;
const auth = { Authorization: `Bearer ${process.env.HOMELAB_TOKEN}` };

// Once: make the folder (409 means it's already there).
await fetch(`${api}/api/files/folders`, {
  method: "POST",
  headers: { ...auth, "Content-Type": "application/json" },
  body: JSON.stringify({ name: "posters" }),
});

// Save a file. The answer has the id, size, type and meta (date taken, camera, ...).
const bytes = await fs.promises.readFile("poster.jpg");
const res = await fetch(`${api}/api/files/posters?name=poster.jpg&tags=dune`, { method: "POST", headers: auth, body: bytes });
const file = await res.json();

// List photos taken this year, and download one.
const list = await (await fetch(`${api}/api/files/posters?taken_after=2026-01-01`, { headers: auth })).json();
const data = await (await fetch(`${api}/api/files/posters/${file.id}`, { headers: auth })).arrayBuffer();
```

Python (`requests`):

```python
import os, requests
api, auth = os.environ["HOMELAB_API_URL"], {"Authorization": f"Bearer {os.environ['HOMELAB_TOKEN']}"}

requests.post(f"{api}/api/files/folders", json={"name": "posters"}, headers=auth)
with open("poster.jpg", "rb") as f:
    file = requests.post(f"{api}/api/files/posters", params={"name": "poster.jpg", "tags": "dune"}, data=f, headers=auth).json()
files = requests.get(f"{api}/api/files/posters", params={"camera": "iphone"}, headers=auth).json()["files"]
data = requests.get(f"{api}/api/files/posters/{file['id']}", headers=auth).content
```

Errors come back as `{"error": "..."}` with a plain message. A client can also be made from [files.openapi.yaml](files.openapi.yaml).

## Secrets

An app can keep secrets of its own while it runs, such as the sign-in token of one of its users. Homelab keeps them encrypted, separate for dev and prod.

There are two kinds:

- **Config secrets** are the ones the owner sets in the companion and the app lists in `homelab.yaml`. They are put in the app's environment at deploy. The app can read them here, but cannot change or delete them.
- **App secrets** are the ones the app saves itself. They are never put in the environment. The owner can see their names, and can show or delete them under **Secrets** in the companion ("Saved by the app").

An app sees only its own secrets for its own environment. Homelab's own credentials (like the GitHub token) are never visible to an app.

### Scopes

- `secrets:read`: list, read and see the versions.
- `secrets:write`: also save and delete app secrets.

Every app key has `secrets:read`. To let the app save secrets, add this to `homelab.yaml`:

```yaml
secrets_api: write   # read (the default), write or none
```

`none` gives the key no secrets scopes at all. A key made earlier gets its new scopes at the next deploy. Admin keys and signed-in people cannot use this API; they get 403.

### Use it

```sh
# Save a secret (needs secrets_api: write). Names use A-Z a-z 0-9 _ . : -
curl -X PUT "$HOMELAB_API_URL/api/secrets/google_token:user42" \
  -H "Authorization: Bearer $HOMELAB_TOKEN" -H "Content-Type: application/json" \
  -d '{"value":"ya29.example"}'

# Read it, list the names, delete it.
curl -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/secrets/google_token:user42"
curl -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/secrets"
curl -X DELETE -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/secrets/google_token:user42"
```

A value can be up to 64 KiB. Each secret keeps its last 5 versions (`GET /api/secrets/{name}?version=2`). If the secrets service is not running, calls answer 503 and nothing is saved. A client can be made from [secrets.openapi.yaml](secrets.openapi.yaml).

### Using it from code

The Homelab plugins wrap these calls. Inside an app, `HOMELAB_API_URL` and `HOMELAB_TOKEN` are already set.

```ts
// TypeScript
const homelab = new Homelab();
await homelab.secrets.set("google_token:user42", token);
const t = await homelab.secrets.getOrNull("google_token:user42"); // null if missing
const key = await homelab.secrets.get("STRIPE_KEY");              // config secret
```

```python
# Python
homelab = Homelab()
homelab.secrets.set("google_token:user42", token)
t = homelab.secrets.get_or_none("google_token:user42")   # None if missing
key = homelab.secrets.get("STRIPE_KEY")
```

```rust
// Rust
let homelab = Homelab::from_env()?;
homelab.secrets().set("google_token:user42", &token).await?;
let t = homelab.secrets().get_opt("google_token:user42", Default::default()).await?; // None if missing
let key = homelab.secrets().get("STRIPE_KEY", Default::default()).await?;
```

`get` reads the file `/run/secrets/<name>` first when there is one, so a config secret costs no web call. Pass `fresh` (`{ fresh: true }`, `fresh=True`, `.fresh()`) to always ask Homelab, for example to see a value that changed. Asking for a `version` always asks Homelab. Names are checked before anything is sent. A value never appears in an error or in debug output. See [plugins](../plugins/README.md).

## Databases

Add one line to `homelab.yaml` and every environment gets its own database:

```yaml
database: postgres   # or sqlite
```

Dev and prod never share one. The app reads `DATABASE_URL` with any normal driver.

| | `postgres` | `sqlite` |
|---|---|---|
| What runs | One more container, `db` (Postgres 17, 256 MB), next to the app | A file in the app's own folder, `/homelab/db/app.db` |
| `DATABASE_URL` | `postgres://app:<password>@db:5432/app` | `sqlite:////homelab/db/app.db` |
| Also set | The secret `DATABASE_PASSWORD` | `DATABASE_PATH=/homelab/db/app.db` |

- With `postgres`, the app starts after the database says it is ready. The password is made once for each environment and never changes. `DATABASE_URL` is a secret, so it follows `secrets_as`. `DATABASE_URL` and `DATABASE_PASSWORD` are Homelab's: the app can't list them in `secrets`, `generate_secrets` or `env`. A service called `db` is not allowed.
- With `sqlite`, an app data entry called `homelab-db` or with the path `/homelab/db` is not allowed.
- Postgres is only reachable from the device itself (`127.0.0.1`), never from the home network. To use it from another computer, see "Connect from your Mac".

### Use a SQLite file you already have

If the app already keeps a SQLite file in one of its own `data:` folders, point at it instead of getting `/homelab/db`:

```yaml
data: [{name: data, path: /data}]
database: {engine: sqlite, path: /data/catalogue_full.db}
```

- `path` is only for `sqlite`. It must be an absolute path inside one of the app's own `data:` mounts (not `folders:`, not a service's).
- The app gets `DATABASE_URL=sqlite:////data/catalogue_full.db` and `DATABASE_PATH=/data/catalogue_full.db`. Homelab adds no folder and no mount. The app may set `DATABASE_PATH` itself (older Homelab versions need it), but only to this same path.
- Copies, **Copy prod to dev**, **Restore** and import work on that one file (and its `-wal` and `-shm` files). The rest of the data folder is not touched. A copy is stored as `homelab-db/app.db` whatever the file is called. The data folder is copied before a promote as always.

### Copies

A copy is a snapshot of one environment's database. They are kept with the other copies of the app's data, and old ones are removed the same way.

- **Before every promote**, prod's database is copied with its data. If the new version fails and Homelab goes back, the database goes back too.
- **Copy now**: on the database screen, **Copy now** makes a copy of dev or prod. The app keeps running.
- **Copy prod to dev**: replaces dev's data with a copy of prod. Prod keeps running. Dev's app stops for a moment, and dev's own data is copied first, so nothing is lost.
- **Restore**: puts a copy back. The app stops for a moment. A copy of the current data is made first.

Copying and restoring are for admins, and every one goes in the activity log. They answer at once and finish in the background.

### Import a database

On the database screen, **Import** sends a file from your computer into dev or prod. It replaces what is there. Admins only, and the file goes straight to disk (up to 50 GB), never into memory.

| The file starts with | It is | For |
|---|---|---|
| `SQLite format 3` | a SQLite database file | `sqlite` |
| `PGDMP` | a `pg_dump -Fc` dump | `postgres` |
| anything else (text) | a plain `.sql` script | `postgres` |

A file that doesn't fit the app's database is refused with a plain message. Otherwise Homelab copies the current database (`before-import`), stops the app, loads the file, and starts the app. A plain `.sql` script runs in one transaction after the old tables are dropped: the first error stops it. If the load fails, the copy is put back and the activity log says why (`db.import_started`, `db.imported`, `db.failed`). Import is only in the companion; API keys can't do it.

### Safe changes to prod

When you run SQL that changes prod from the companion, Homelab first makes a copy of prod's database (`before-change`, the same as **Copy now**) and then runs the SQL. If the copy fails, nothing runs ("Couldn't make a copy first, so nothing was changed."). Changes to dev make no copy, and neither does reading. The copy's name is passed on to the change history, so you can see which copy to restore.

### Connect from your Mac

Postgres only. Open the database in the companion and look under **Connect from your Mac**.

1. Type your login on this device. The companion remembers it in your browser.
2. Copy the first line and run it in Terminal. Keep it open: `ssh -N -L 15432:127.0.0.1:<port> <login>@<this computer>`.
3. Tap **Show address**, copy it, and paste it into TablePlus or `psql`: `postgres://app:<password>@127.0.0.1:15432/app`.

The password is fetched only when you tap **Show address**, with the same rule as any secret: only people who can see secrets get it, and the view goes in the activity log.

### Look inside and run SQL

The **Databases** screen lists every database with its size and tables. Open one to see its tables and their rows (50 at a time), and to run SQL.

- SQL is **read only** by default. Postgres runs it as a read-only role; SQLite opens the file read only and takes one statement at a time.
- A statement can run for at most 10 seconds and shows at most 500 rows (the screen says when it cut the list). SQL can be up to 100 KB.
- Only Postgres can be changed, and only by a signed-in person: switch on **Allow changes**. On prod, Homelab asks first ("This changes prod's data. Run it?"). Apps and keys can't change data this way.
- Homelab never logs the SQL or the values. It logs who, which database, and how long it took.
- A database that is down shows "Not answering"; one that has not saved anything yet shows "Nothing saved yet".

### Databases API

A signed-in person can do everything. An admin key needs the `db:read` scope. An app key sees only its own app and environment; any other is 404. Full reference: [db.openapi.yaml](db.openapi.yaml).

| Call | Gives |
|---|---|
| `GET /api/db` | every database: app, env, engine, status, size, connections, tables |
| `GET /api/db/{app}/{env}` | one of them |
| `GET /api/db/{app}/{env}/tables` | its tables, with row counts |
| `GET /api/db/{app}/{env}/tables/{table}/rows` | rows (`limit` 1-200, `offset`) |
| `POST /api/db/{app}/{env}/query` | runs `{"sql": "...", "write": false}` |

The plugins (TypeScript, Python, Rust) wrap these calls as `db.list`, `db.info`, `db.tables`, `db.rows` and `db.query`; they never write. See [plugins](../plugins/README.md).

The companion's own calls (admins only): `GET /api/apps/{id}/db` (engine, port and copies), `POST /api/apps/{id}/envs/{env}/db/copies` (copy now), `POST /api/apps/{id}/envs/{env}/db/copies/{name}/restore`, `POST /api/apps/{id}/db/copy-prod-to-dev`, and `POST /api/apps/{id}/envs/{env}/db/import` (the raw file as the body, `Content-Type: application/octet-stream`, with the companion's `X-Homelab-Request: 1` header; answers 202 `{"ok":true,"kind":"sqlite|pg_dump|sql"}` and loads in the background).

## Logs

Homelab keeps the log lines of every app's containers, and a line for each thing Homelab does with files, databases, secrets and deploys. Search them on the companion's **Logs** screen, or with the API.

- An app's lines are what its containers print (stdout and stderr). When Homelab first sees a container, it reads back up to 14 days of its logs.
- Homelab's own lines have the app `homelab` and a service such as `files`. A line about an app (for example a file saved in that app's folder) also shows in that app's logs.
- Lines are kept 14 days. A size cap (`max_mb` in `config.yaml`, default 2048 MB) deletes the oldest lines first when the store is full.
- Secret values are hidden before a line is stored. A hidden value shows as `[secret NAME]`. Values shorter than 6 characters are not hidden. A secret saved a moment ago can show for up to a minute, because Homelab keeps its list of secrets for 60 seconds.
- Homelab never writes SQL, query values, file contents or keys into a line.

### Levels

Lines are ordered by number. `min_level` keeps that level and louder.

| Level | Number | Tag | Colour | Icon | Row on the screen |
|---|---|---|---|---|---|
| unknown | 0 | `---` | grey | question mark | normal |
| debug | 1 | `DBG` | blue | bug | dimmed |
| info | 2 | `INF` | green | circle with an i | normal |
| warn | 3 | `WRN` | yellow | triangle with a `!` | normal |
| error | 4 | `ERR` | red | circle with an X | light red |
| fatal | 5 | `FTL` | purple | octagon with a `!` | bold |

### How a line's level is read

- **A JSON line** (one that starts with `{`): the level comes from `level`, `lvl`, `severity` or `levelname`. It can be a word in any case, or a Python (pino) number: 10 or 20 is debug, 30 info, 40 warn, 50 error, 60 fatal. The message comes from `msg`, `message` or `event`. The time comes from `time`, `timestamp`, `ts` or `@timestamp` when it is RFC 3339, else from Docker. `app`, `env` and `service` are dropped, because Homelab sets them. The other fields go in `extra`. Up to 8 KB; a bigger set is dropped and `extra_truncated` is set. A JSON line with no level is `unknown`.
- **Any other line**: the first word is read. If the first word is a number (a time), the first three words are read. `TRACE`, `DEBUG` and `DBG` are debug. `INFO`, `INF` and `NOTICE` are info. `WARN`, `WARNING` and `WRN` are warn. `ERROR` and `ERR` are error. `FATAL`, `FTL`, `CRITICAL`, `CRIT` and `PANIC` are fatal. Case does not matter, and brackets, colons and `level=` are ignored. So `ERROR:root:msg` is an error.
- **No level found**: stderr is warn and stdout is info.
- A line is at most 16 KB. A longer one is cut, and `…` is added.

### Settings

An app sets its own minimum in `homelab.yaml`:

```yaml
logs:
  min_level: info    # debug, info, warn, error or fatal
```

The default is debug on dev and info on prod. Lines below the minimum are not stored. Lines of unknown level are always kept.

Homelab sets its own in `config.yaml`:

```yaml
logs:
  min_level: info        # Homelab's own events below this are not kept (default info)
  services:
    files: debug         # one service with its own level
  max_mb: 2048           # the most disk the logs may take, from 16 to 1048576 MB
```

A bad value stops Homelab from starting, and the message names the setting.

### What Homelab writes

| Service | Event | Level | Notes |
|---|---|---|---|
| `files` | folder made, folder deleted, file saved, file deleted | info | Names, ids, sizes and who did it. Never the file |
| `files` | file read | debug | Only kept with `services: {files: debug}` |
| `databases` | query run | info | Milliseconds, rows, and whether it was a write. Never the SQL |
| `databases` | table rows read | debug | Only kept with `services: {databases: debug}` |
| `secrets` | secret saved, secret deleted | info | Names only |
| `secrets` | secret read | info for an app's secret, debug for a config secret | Names and versions only. Config reads are only kept with `services: {secrets: debug}` |
| `deploys` | deploy, promote, rollback, build | info, or error when it failed | Every other Homelab activity line goes to a service named after its first word, such as `app`, `key` or `disk` |

### Who sees what

| Caller | Sees |
|---|---|
| An owner or admin, signed in to the companion | Everything |
| An admin key with `logs:read` | Everything |
| An app key (every app key has `logs:read`) | Its own app and environment, and Homelab's events about them |
| A key without `logs:read` | Nothing (403) |

### Calls

| Call | Gives |
|---|---|
| `GET /api/logs` | The lines that match, newest first, with the counts per level and the next page |

| Parameter | Meaning |
|---|---|
| `q` | Words to find, in the message and the extra fields. Only the first 200 characters and 10 words are used. Every word must match, as whole words: `err` does not find `error` |
| `app`, `env`, `service` | Exact match. `app=homelab` gives Homelab's own lines. An app key that asks for another app or environment gets 404 |
| `level` | Exact levels, as a comma list such as `error,fatal`. `unknown` is allowed |
| `min_level` | That level and louder. Lines of unknown level are kept too |
| `since`, `until` | RFC 3339 or unix milliseconds |
| `before` | A line id: only older lines. Use `next` from the last answer to page back |
| `after` | A line id: only newer lines, oldest first. Follows new lines (live tail). `next` is null. If you get `limit` lines, ask again, so a backlog is not skipped |
| `limit` | 1 to 500. Default 100. Another value gives 400 |

```json
{"lines":[{"id":42,"time":"2026-10-23T22:00:00.123Z","level":"error","msg":"...","app":"northern-lights",
  "env":"prod","service":"app","stream":"stderr","extra":{}}],
 "counts":{"unknown":0,"debug":0,"info":3,"warn":1,"error":2,"fatal":0},
 "next":"41"}
```

`counts` is for the same search without `level`, `min_level`, `before` and `after`. Every level is there, even at 0. So a count can be tapped to filter. `next` is the `before` value for the next page, or null.

A bad value gives 400, a key without `logs:read` gives 403, and an app key that asks for another app gives 404. Each answer is `{"error": "A sentence."}`. A complete description is in [logs.openapi.yaml](logs.openapi.yaml).

```sh
# From inside an app container: its warnings, errors and fatal lines, newest first
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/logs?min_level=warn&limit=20"

# The next older page: use the "next" value from the last answer (41 is an example)
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/logs?min_level=warn&limit=20&before=41"
```

### Use it from your code

The plugins wrap the search. See [plugins](../plugins/README.md).

```ts
// TypeScript
const homelab = new Homelab();
const found = await homelab.logs.search({ q: "timeout", minLevel: "warn", limit: 50 });
for (const line of found.lines) console.log(line.time, line.level, line.msg);
```

```python
# Python
homelab = Homelab()
found = homelab.logs.search(q="timeout", min_level="warn", limit=50)
for line in found.lines:
    print(line.time, line.level, line.msg)
```

```rust
// Rust
let homelab = Homelab::from_env()?;
let found = homelab.logs().search(LogQuery {
    q: Some("timeout".into()),
    min_level: Some("warn".into()),
    limit: Some(50),
    ..Default::default()
}).await?;
```

Python's `logging` can send its lines to Homelab in a form Homelab reads. Call it once, at start:

```python
import logging
import homelab

homelab.logs.setup()    # no key needed; safe to call twice
logging.getLogger("app").info("started", extra={"port": 8000})
# prints: {"time": "...", "level": "info", "msg": "started", "logger": "app", "port": 8000}
```

`setup()` prints one JSON line per record on stdout. DEBUG becomes debug, INFO info, WARNING warn, ERROR error and CRITICAL fatal. An `extra=` field named `time`, `level`, `msg`, `logger` or `exception` is saved as `extra_<name>`.
