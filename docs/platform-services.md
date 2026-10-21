# Platform services

Homelab gives apps, and servers outside Homelab, a small API: people (here), and later files, databases and data. Every call uses an **API key**.

## API keys

A key looks like `hlk_` plus 43 characters. Homelab keeps only a SHA-256 of it, so a lost key can't be shown again: make a new one.

There are two kinds.

**App keys.** Every app environment gets one automatically, in two settings:

- `HOMELAB_TOKEN`: the key.
- `HOMELAB_API_URL`: where to use it, `http://host.docker.internal:7085`.

An app key acts for its own app and environment only. It can read the people of its app. Later services decide what else it may touch from the app and environment it belongs to, so a dev key never gets prod's data. It only works at `HOMELAB_API_URL`, not on the companion's address.

An admin can revoke an app key under **Settings → API keys**, for example if it leaked. The app gets a new one the next time it's deployed.

**Admin keys.** For a server outside Homelab, such as a photo service on another computer. An admin makes one in the companion under **Settings → API keys**, names it, and copies it once. It works on the companion's own address. It stops working when it's revoked, when it expires, or when the person who made it is no longer an admin.

### Scopes

- `users:read`: read people.
- `files:read`: read every folder's files.
- `files:write`: also make folders, and save, change and delete files.

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
- Databases and data come in the next changes.

Checks: `make check-services` (format, lint, tests).

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
