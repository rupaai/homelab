# homelab-sdk (Python)

A small Python client for Homelab's **Files** and **Secrets** services. Standard library only (no runtime
dependencies), Python 3.9+, fully typed.

## Install

```sh
pip install "git+https://github.com/rupaai/homelab#subdirectory=plugins/python"
```

or from a checkout:

```sh
pip install ./plugins/python          # or: pip install -e "plugins/python[dev]"
```

(PyPI later.) The distribution is `homelab-sdk`; you import `homelab`.

## Quick start

```python
from homelab import Homelab

homelab = Homelab()   # reads HOMELAB_API_URL and HOMELAB_TOKEN

homelab.files.ensure_folder("posters")
file, duplicate = homelab.files.upload("posters", "poster.jpg", tags=["dune"])
data = homelab.files.download("posters", file.id)

homelab.secrets.set("google_token:user42", token)            # needs secrets_api: write
saved = homelab.secrets.get_or_none("google_token:user42")   # None if missing
api_key = homelab.secrets.get("STRIPE_KEY")                  # reads /run/secrets/STRIPE_KEY first
```

Inside a Homelab app both variables are already set. To pass them yourself:
`Homelab(url="http://host:7085", token="hlk_...", timeout=60, secrets_dir="/run/secrets")`. A trailing slash on the
url is fine. A missing url or token raises `HomelabError` right away. `timeout` (default
60 seconds) covers normal calls; uploads and downloads have no timeout. The token is never
shown in `repr()`, `str()` or errors.

## Methods

All methods are on `homelab.files`. Names are snake_case. Anything unknown the server
sends is kept in `.raw` on each result (never an error).

### `folders() -> list[Folder]`
```python
for f in homelab.files.folders():
    print(f.name, f.files, f.bytes)
```

### `create_folder(name, *, disk=None) -> Folder`
```python
folder = homelab.files.create_folder("posters", disk="main")   # 409 if it exists
```

### `ensure_folder(name, *, disk=None) -> Folder`
Creates the folder, or returns the existing one when the server answers 409.
```python
folder = homelab.files.ensure_folder("posters")
```

### `delete_folder(name) -> None`
Only empty folders (409 otherwise).
```python
homelab.files.delete_folder("posters")
```

### `upload(folder, data, *, name=None, tags=None, sha256=None) -> UploadResult`
`data` is `bytes`, a file path (`str` or `Path`, streamed from disk with a
`Content-Length`; `name` defaults to the file name), or a binary file object. Returns
`(file, duplicate)`; `duplicate` is `True` when the folder already held the same bytes
(the existing file comes back).
```python
file, duplicate = homelab.files.upload("posters", b"...", name="poster.jpg", tags=["dune", "poster"])
with open("big.mov", "rb") as fh:
    homelab.files.upload("videos", fh, name="big.mov", sha256=expected_hex)
```

### `list(folder, *, tag, type, taken_after, taken_before, camera, after, limit) -> FileList`
One page, newest first: `files, next = ...`. `taken_after`/`taken_before` take an RFC 3339
string, a `date` or a `datetime`. `type` is a content-type prefix such as `"image/"`.
```python
page = homelab.files.list("posters", type="image/", taken_after="2024-05-01", limit=50)
more = homelab.files.list("posters", after=page.next) if page.next else None
```

### `all(folder, **same_filters) -> Iterator[File]`
A generator that follows `next` for you.
```python
for f in homelab.files.all("posters", camera="canon eos"):
    print(f.id, f.meta.taken_at)
```

### `info(folder, id) -> File`
```python
f = homelab.files.info("posters", file_id)
print(f.content_type, f.size, f.meta.width, f.meta.gps)
```

### `update(folder, id, *, tags=None, info=None) -> File`
Replaces `tags` and/or `info`; what you leave out stays.
```python
homelab.files.update("posters", file_id, tags=["dune"], info={"rating": 5})
```

### `download(folder, id, *, range=None) -> bytes`
```python
head = homelab.files.download("posters", file_id, range="bytes=0-99")
```

### `download_to(folder, id, path, *, range=None) -> Path`
Streams to disk without holding the file in memory.
```python
homelab.files.download_to("videos", file_id, "/tmp/big.mov")
```

### `delete(folder, id) -> None`
Moves the file to the trash.
```python
homelab.files.delete("posters", file_id)
```

## Secrets

All on `homelab.secrets`. Names are checked before anything is sent or read: 1 to 200 of
`A-Z a-z 0-9 _ . : -`, never `.` or `..`. A bad name raises `HomelabError` (status 0).
`:` and the like are URL-encoded for you. A value never appears in an error or in `repr()`.

### `get(name, *, version=None, fresh=False) -> str`
Without `version`, it reads the file `<secrets_dir>/<name>` first when there is one (only for
names like `STRIPE_KEY`: letters, digits and `_`, not starting with a digit). The contents are
returned exactly as written. With no file, or with `fresh=True`, it asks Homelab. A `version`
(1 or more) always asks Homelab. A missing secret raises `HomelabError` with status 404.
```python
key = homelab.secrets.get("STRIPE_KEY")
live = homelab.secrets.get("STRIPE_KEY", fresh=True)
old = homelab.secrets.get("google_token:user42", version=2)
```

### `get_or_none(name, *, version=None, fresh=False) -> str | None`
The same, but `None` when there is no such secret (404). Other errors still raise.
```python
token = homelab.secrets.get_or_none("google_token:user42")
```

### `set(name, value) -> SecretSaved`
Needs `secrets_api: write` in `homelab.yaml`. A secret the owner set (`config`) cannot be changed (403).
```python
saved = homelab.secrets.set("google_token:user42", token)   # saved.name, saved.version
```

### `delete(name) -> None`
Deleting a secret that is not there is fine.
```python
homelab.secrets.delete("google_token:user42")
```

### `list() -> list[SecretInfo]`
Names, kinds and versions. Never values.
```python
for s in homelab.secrets.list():
    print(s.name, s.kind, s.version, s.updated_at)
```

### `versions(name) -> list[SecretVersion]`
The last 5 versions, newest first (`version`, `set_at`). 404 if the secret does not exist.
```python
for v in homelab.secrets.versions("google_token:user42"):
    print(v.version, v.set_at)
```

## Types

`Folder`, `File`, `Meta` (with `Gps`), `SecretInfo`, `SecretVersion` and `SecretSaved` are frozen dataclasses. `File` has `id, folder, name,
content_type, size, sha256, created_at, created_by, tags, info, meta`; `Meta` has
`width, height, taken_at, camera_make, camera_model, lens, orientation, gps, duration_s,
title, artist, album, year, pages, author` (each `None` when the server did not find it).
Every object has `.raw`, the server's dict, including fields this version does not know.
Path parts (`folder`, `id`) and query values are URL-encoded for you.

## Errors

Every failure raises `HomelabError` with `status` (the HTTP status, or `0` for network
errors, timeouts and missing configuration) and `message` (the server's `{"error": ...}`
text, or `HTTP <status> <reason>` when there is none).

```python
from homelab import HomelabError

try:
    homelab.files.info("posters", file_id)
except HomelabError as e:
    if e.status == 404:
        ...
    else:
        raise
```

Redirects are never followed (the token must not leave the url you gave).

## Development

```sh
pip install -e "plugins/python[dev]"
pytest plugins/python
mypy --strict plugins/python/src
```
