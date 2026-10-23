# homelab-sdk (Rust)

A thin async client for [Homelab](../../README.md)'s Files, Secrets and Databases services. It builds the
request, reads the answer and turns errors into one `Error` type. Nothing else.

The crate is named `homelab-sdk`; you use it as `homelab`.

## Install

Not on crates.io yet. Use the git repository; cargo finds the package by name inside
the repo's `plugins/rust` folder:

```toml
[dependencies]
homelab-sdk = { git = "https://github.com/rupaai/homelab" }
tokio = { version = "1", features = ["rt-multi-thread", "macros"] }
```

TLS is `rustls` (no OpenSSL needed). Needs Rust 1.75 or newer.

## Quick start

Inside a Homelab app, `HOMELAB_API_URL` and `HOMELAB_TOKEN` are already set.

```rust
use homelab::{FolderOptions, Homelab, UploadOptions};

#[tokio::main]
async fn main() -> Result<(), homelab::Error> {
    let homelab = Homelab::from_env()?;          // or Homelab::new(url, token)?
    let files = homelab.files();

    files.ensure_folder("posters", FolderOptions::default()).await?;
    let up = files
        .upload("posters", std::fs::read("dune.jpg").unwrap(),
                UploadOptions::new("dune.jpg").tags(["dune", "poster"]))
        .await?;
    println!("{} ({} bytes), duplicate: {}", up.file.id, up.file.size, up.duplicate);

    let secrets = homelab.secrets();
    secrets.set("google_token:user42", "ya29.example").await?;   // needs secrets_api: write
    let saved = secrets.get_opt("google_token:user42", Default::default()).await?; // None if missing
    let api_key = secrets.get("STRIPE_KEY", Default::default()).await?; // reads /run/secrets/STRIPE_KEY first
    Ok(())
}
```

`Homelab` is cheap to clone. `Homelab::new(url, token)` accepts a trailing slash on the
url, and returns `Error::Config` for an empty url or token. The token is sent as
`Authorization: Bearer ...` and is never printed: not in errors, not in `{:?}`.
Normal calls time out after 60 s (`homelab.with_timeout(Duration)` changes it); uploads
and downloads have no timeout.

Folder names and ids are percent-encoded for you, and so are query values.
`homelab.with_secrets_dir(dir)` changes where `secrets().get` looks for files (default `/run/secrets`).

## Methods

All are on `homelab.files()`. Options are plain structs with `Default` and chainable
setters.

### folders

```rust
let folders = files.folders().await?;            // Vec<Folder>
for f in folders { println!("{} {} files {} bytes", f.name, f.files, f.bytes); }
```

### create_folder

```rust
let folder = files.create_folder("posters", FolderOptions::default().disk("main")).await?;
```

### ensure_folder

Creates the folder; on a 409 (already there) returns the existing one from `folders()`.

```rust
let folder = files.ensure_folder("posters", FolderOptions::default()).await?;
```

### delete_folder

The folder must be empty (409 otherwise).

```rust
files.delete_folder("posters").await?;
```

### upload

The body is the raw bytes. It takes anything `Into<reqwest::Body>`: `Vec<u8>`,
`Bytes`, `String`, `&'static [u8]`, `tokio::fs::File`, or a stream wrapped with
`reqwest::Body::wrap_stream`. `duplicate` is true when the folder already had these
bytes (status 200 and `X-Homelab-Duplicate: true`); nothing new is stored then.

```rust
let up = files
    .upload("posters", b"hello".as_slice(),
            UploadOptions::new("hello.txt").tags(["greeting"]).sha256(hex_digest))
    .await?;
if up.duplicate { println!("already stored as {}", up.file.id); }
```

### upload_file

Streams a file from disk; memory use stays flat for big files. The name defaults to the
file's own name.

```rust
let up = files.upload_file("movies", "/data/dune.mkv", UploadOptions::default()).await?;
```

### list

One page, newest first. Filters: `tag`, `content_type` (sent as `type`, a prefix such as
`image/`), `taken_after`, `taken_before` (RFC 3339), `camera`, `after`, `limit`.

```rust
use homelab::ListOptions;
let page = files
    .list("photos", ListOptions::default().content_type("image/").camera("canon").limit(100))
    .await?;
println!("{} files, next page after {:?}", page.files.len(), page.next);
```

### all

A `Stream` of every matching file; it follows `next` as `after`, fetching a page at a
time as you read.

```rust
use homelab::TryStreamExt;
let mut stream = Box::pin(files.all("photos", ListOptions::default().tag("holiday")));
while let Some(file) = stream.try_next().await? {
    println!("{} {}", file.id, file.name);
}
```

### info

```rust
let file = files.info("photos", &id).await?;
println!("{:?} {:?}", file.meta.taken_at, file.meta.gps);
```

### update

Replaces `tags` and/or `info`; a field you leave out stays as it is.

```rust
use homelab::UpdateOptions;
let mut info = serde_json::Map::new();
info.insert("rating".into(), 5.into());
let file = files.update("photos", &id, UpdateOptions::default().tags(["best"]).info(info)).await?;
```

### download

The whole file in memory as `bytes::Bytes`. `range` is a `Range` header value.

```rust
use homelab::DownloadOptions;
let bytes = files.download("photos", &id, DownloadOptions::default()).await?;
let head = files.download("photos", &id, DownloadOptions::default().range("bytes=0-99")).await?;
```

### download_stream

The same, as a stream of chunks.

```rust
use homelab::StreamExt;
let mut s = Box::pin(files.download_stream("movies", &id, DownloadOptions::default()).await?);
while let Some(chunk) = s.next().await {
    out.write_all(&chunk?)?;
}
```

### delete

Moves the file to the trash (emptied after 30 days).

```rust
files.delete("photos", &id).await?;
```

## Secrets

All are on `homelab.secrets()`. Names are checked before anything is sent or read: 1 to 200 of
`A-Z a-z 0-9 _ . : -`, never `.` or `..`. A bad name is `Error::Config`. `:` and the like are
percent-encoded for you. A value never appears in an error or in `{:?}` output.

### get

Without `version`, it reads the file `<secrets dir>/<name>` first when there is one (only for
names like `STRIPE_KEY`: letters, digits and `_`, not starting with a digit). The contents are
returned exactly as written. With no file, or with `.fresh()`, it asks Homelab. A `.version(n)`
(1 or more) always asks Homelab. A missing secret is `Error::Api { status: 404, .. }`.

```rust
use homelab::GetSecretOptions;
let key = secrets.get("STRIPE_KEY", GetSecretOptions::default()).await?;
let live = secrets.get("STRIPE_KEY", GetSecretOptions::default().fresh()).await?;
let old = secrets.get("google_token:user42", GetSecretOptions::default().version(2)).await?;
```

### get_opt

The same, but `Ok(None)` when there is no such secret (404). Other errors are still errors.

```rust
let token = secrets.get_opt("google_token:user42", GetSecretOptions::default()).await?;
```

### set

Needs `secrets_api: write` in `homelab.yaml`. A secret the owner set (`config`) cannot be changed (403).

```rust
let saved = secrets.set("google_token:user42", &token).await?;   // saved.name, saved.version
```

### delete

Deleting a secret that is not there is fine.

```rust
secrets.delete("google_token:user42").await?;
```

### list

Names, kinds and versions. Never values.

```rust
for s in secrets.list().await? { println!("{} {} v{} {}", s.name, s.kind, s.version, s.updated_at); }
```

### versions

The last 5 versions, newest first (`version`, `set_at`). 404 if the secret does not exist.

```rust
for v in secrets.versions("google_token:user42").await? { println!("{} {}", v.version, v.set_at); }
```

## Databases

Apps connect with `DATABASE_URL` and any normal driver. `homelab.db()` is for looking inside:
list databases, tables, rows and read-only SQL. It never writes (writing is for people in the
companion). Wherever a call takes `app` and `env`, `None` means `HOMELAB_APP` and `HOMELAB_ENV`,
which Homelab sets in every app. An app key sees only its own database.

```rust
let db = homelab.db();
```

### list

`app, env, engine, status, size_bytes, connections, tables` for each database.

```rust
for d in db.list().await? { println!("{} {} {} {}", d.app, d.env, d.engine, d.status); }
```

### info

```rust
let me = db.info(None, None).await?; // this app, this env
```

### tables

```rust
for t in db.tables(None, None).await? { println!("{}.{} ~{} rows", t.schema, t.name, t.rows); }
```

### rows

One page of a table: `columns`, `rows` (each a list in column order) and `truncated`.
`limit` is 1-200 (default 50), `offset` from 0, `schema` defaults to `public`.

```rust
let page = db.rows(None, None, "orders", RowsOptions::default().limit(20).offset(40)).await?;
```

### query

Read-only SQL: `columns, rows, row_count, truncated, command, ms`.

```rust
let r = db.query(None, None, "select count(*) from orders").await?;
```

## Types

`Folder`, `File` and `Meta` (with `Gps`), `SecretInfo`, `SecretVersion`, `SecretSaved`, `DbInfo`, `DbTable`, `DbRows` and `DbQueryResult` mirror the API. Every field has a default, and
anything the server adds later lands in the `extra` map of each type, so decoding never
breaks when the server grows. `File.info` is a `serde_json::Map`; `Meta` fields are
`Option`s, `None` when the server did not find them.

## Errors

Every call returns `homelab::Result<T>`, with one error enum:

| Variant | When |
|---|---|
| `Error::Api { status, message }` | the server answered with an error. `message` is its `{"error": "..."}` text, or `HTTP 502 Bad Gateway` when the body is not JSON |
| `Error::Network(reqwest::Error)` | no connection, a timeout, or an unreadable answer |
| `Error::Config(String)` | missing or bad url/token (`Set HOMELAB_API_URL or pass url`), a file path that cannot be opened, a folder/id of `.` or `..`, a bad secret name, or an unreadable secret file |

`err.status()` gives the HTTP status, or 0 for network and config errors.

```rust
match files.info("photos", &id).await {
    Err(e) if e.status() == 404 => println!("gone"),
    Err(e) => return Err(e),
    Ok(file) => println!("{}", file.name),
}
```

## Development

```sh
cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test
```

Tests run against a small in-process fake server on `127.0.0.1`. No network needed.
