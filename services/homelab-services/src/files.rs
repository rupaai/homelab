//! The files service: folders, uploads, downloads, tags and trash.

use std::collections::HashMap;
use std::io::SeekFrom;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::extract::{DefaultBodyLimit, Path, Query, State};
use axum::http::header::{
    ACCEPT_RANGES, CACHE_CONTROL, CONTENT_DISPOSITION, CONTENT_LENGTH, CONTENT_RANGE, CONTENT_TYPE,
    ETAG, RANGE,
};
use axum::http::{HeaderMap, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get};
use axum::{Json, Router};
use futures_util::StreamExt;
use serde::Deserialize;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use tokio::io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt};
use ulid::Ulid;

use crate::app::{error_response, not_found, unix_now, AppState};
use crate::caller::{Caller, Kind};
use crate::config::Disk;
use crate::db::{self, Db, FileRow, Folder};
use crate::disk::{make_dirs, open_file, safe_name, valid_name, year_month};
use crate::meta;

/// Trashed files are deleted for good after this many seconds (30 days).
const TRASH_KEEP: i64 = 30 * 24 * 60 * 60;
/// Size limit for JSON request bodies.
const JSON_LIMIT: usize = 64 * 1024;

/// What the file handlers share.
pub struct Files {
    pub db: Db,
    pub disks: Vec<Disk>,
    pub max_upload: u64,
}

impl Files {
    /// Opens the database in `data_dir`. The disks must already be prepared.
    pub fn open(
        data_dir: &std::path::Path,
        disks: Vec<Disk>,
        max_upload: u64,
    ) -> Result<Files, String> {
        Ok(Files {
            db: Db::open(&data_dir.join("files.db"))?,
            disks,
            max_upload,
        })
    }

    /// Finds the folder, if the caller may use it. A folder the caller cannot
    /// see is reported as missing. `write` is for changes.
    async fn folder_for(
        &self,
        caller: &Caller,
        name: &str,
        write: bool,
    ) -> Result<Folder, ApiError> {
        let missing = || err(StatusCode::NOT_FOUND, format!("There's no folder {name}."));
        if !valid_name(name, 63) {
            return Err(missing());
        }
        let key = name.to_string();
        let folder = self
            .db
            .run(move |c| db::folder_get(c, &key))
            .await?
            .ok_or_else(missing)?;
        if !allowed(caller, false, &folder.owner_app, &folder.owner_env) {
            return Err(missing());
        }
        if write && !allowed(caller, true, &folder.owner_app, &folder.owner_env) {
            return Err(err(
                StatusCode::FORBIDDEN,
                "You don't have permission to change files.",
            ));
        }
        Ok(folder)
    }

    /// Deletes files that were trashed more than 30 days before `now`.
    /// Returns how many were removed.
    pub async fn purge(&self, now: i64) -> rusqlite::Result<usize> {
        let disks = self.disks.clone();
        self.db
            .run(move |conn| {
                let mut removed = 0;
                for (id, disk, path) in db::trashed_before(conn, now - TRASH_KEEP)? {
                    let Some(disk) = disks.iter().find(|d| d.name == disk) else {
                        tracing::warn!(%id, %disk, "cannot purge: that disk is not configured");
                        continue;
                    };
                    // Remove the file first, then the row.
                    match std::fs::remove_file(disk.path.join(&path)) {
                        Ok(()) => {}
                        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                        Err(e) => {
                            tracing::warn!(%id, %path, "cannot purge: {e}");
                            continue;
                        }
                    }
                    db::file_delete(conn, &id)?;
                    removed += 1;
                }
                for disk in &disks {
                    clean_tmp(&disk.path.join("homelab-files").join(".tmp"));
                }
                Ok(removed)
            })
            .await
    }
}

/// Removes uploads left half done (by a crash or a stop) more than a day ago.
fn clean_tmp(dir: &std::path::Path) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    let day_ago = std::time::SystemTime::now() - Duration::from_secs(24 * 60 * 60);
    for e in entries.flatten() {
        let old = e
            .metadata()
            .and_then(|m| m.modified())
            .is_ok_and(|t| t < day_ago);
        if old {
            if let Err(err) = std::fs::remove_file(e.path()) {
                tracing::warn!(path = %e.path().display(), "cannot remove a half-done upload: {err}");
            }
        }
    }
}

/// Runs `purge` once now and then every hour.
pub fn spawn_purge(files: Arc<Files>) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(3600));
        loop {
            tick.tick().await;
            match files.purge(unix_now()).await {
                Ok(0) => {}
                Ok(n) => tracing::info!("removed {n} trashed files"),
                Err(e) => tracing::warn!("trash clean-up failed: {e}"),
            }
        }
    });
}

/// The routes under /api/files.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/files/folders",
            get(list_folders)
                .post(create_folder)
                .fallback(not_found)
                .layer(DefaultBodyLimit::max(JSON_LIMIT)),
        )
        .route(
            "/api/files/folders/{name}",
            delete(delete_folder).fallback(not_found),
        )
        .route(
            "/api/files/{folder}",
            get(list_files)
                .post(upload)
                .fallback(not_found)
                .layer(DefaultBodyLimit::disable()),
        )
        .route(
            "/api/files/{folder}/{id}",
            get(download).delete(delete_file).fallback(not_found),
        )
        .route(
            "/api/files/{folder}/{id}/info",
            get(info)
                .patch(patch_info)
                .fallback(not_found)
                .layer(DefaultBodyLimit::max(JSON_LIMIT)),
        )
}

// ---- access ----

/// Whether the caller may read (or, with `write`, change) a folder owned by
/// the given app and env ("" means admin-owned).
pub fn allowed(caller: &Caller, write: bool, owner_app: &str, owner_env: &str) -> bool {
    let has = |scope: &str| caller.scopes.iter().any(|s| s == scope);
    match caller.kind {
        Kind::Person => true,
        Kind::Admin => has("files:write") || (!write && has("files:read")),
        // Apps get their own folders only. homelabd gives them no scopes.
        Kind::App => !caller.app.is_empty() && caller.app == owner_app && caller.env == owner_env,
        // homelabd uses the system caller for secrets only.
        Kind::System => false,
    }
}

/// Who to record as the creator.
fn created_by(c: &Caller) -> String {
    match c.kind {
        Kind::Person => format!("person:{}", c.person),
        Kind::Admin => format!("key:{}", c.key),
        Kind::App => format!("app:{}/{}", c.app, c.env),
        Kind::System => "system".to_string(),
    }
}

// ---- errors ----

/// An error answer: a status and a plain message.
pub struct ApiError(StatusCode, String);

fn err(status: StatusCode, message: impl Into<String>) -> ApiError {
    ApiError(status, message.into())
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        error_response(self.0, &self.1)
    }
}

impl From<rusqlite::Error> for ApiError {
    fn from(e: rusqlite::Error) -> ApiError {
        tracing::error!("database error: {e}");
        err(StatusCode::INTERNAL_SERVER_ERROR, "Something went wrong.")
    }
}

/// A file or folder problem on the server's disk.
fn disk_error(what: &str, e: impl std::fmt::Display) -> ApiError {
    tracing::error!("{what}: {e}");
    err(
        StatusCode::INTERNAL_SERVER_ERROR,
        "The server could not use its storage.",
    )
}

type Reply = Result<Response, ApiError>;

// ---- folders ----

fn folder_json(f: &Folder, files: i64, bytes: i64) -> Value {
    json!({
        "name": f.name,
        "disk": f.disk,
        "app": f.owner_app,
        "env": f.owner_env,
        "files": files,
        "bytes": bytes,
        "created_at": f.created_at,
    })
}

/// GET /api/files/folders
async fn list_folders(State(s): State<AppState>, caller: Caller) -> Reply {
    let all = s.files.db.run(db::folder_list).await?;
    let folders: Vec<Value> = all
        .iter()
        .filter(|(f, _, _)| allowed(&caller, false, &f.owner_app, &f.owner_env))
        .map(|(f, n, b)| folder_json(f, *n, *b))
        .collect();
    Ok(Json(json!({ "folders": folders })).into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct NewFolder {
    name: String,
    #[serde(default)]
    disk: Option<String>,
    #[serde(default)]
    app: String,
    #[serde(default)]
    env: String,
}

/// Reads a JSON body.
fn parse_body<T: serde::de::DeserializeOwned>(body: &Bytes) -> Result<T, ApiError> {
    serde_json::from_slice(body).map_err(|e| {
        err(
            StatusCode::BAD_REQUEST,
            format!("That isn't a valid request body: {e}"),
        )
    })
}

/// POST /api/files/folders
async fn create_folder(State(s): State<AppState>, caller: Caller, body: Bytes) -> Reply {
    let mut new: NewFolder = parse_body(&body)?;
    if !valid_name(&new.name, 63) || new.name == "folders" {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "A folder name needs 1-63 lowercase letters, digits, _ or -, and can't be \"folders\".",
        ));
    }
    let disk = match &new.disk {
        None => s.files.disks[0].name.clone(),
        Some(d) if s.files.disks.iter().any(|x| &x.name == d) => d.clone(),
        Some(d) => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                format!("There's no disk called {d}."),
            ))
        }
    };

    if caller.kind == Kind::App {
        // Apps only make folders for themselves.
        let other = (!new.app.is_empty() || !new.env.is_empty())
            && (new.app != caller.app || new.env != caller.env);
        if other {
            return Err(err(
                StatusCode::FORBIDDEN,
                "An app can only make folders for itself.",
            ));
        }
        new.app = caller.app.clone();
        new.env = caller.env.clone();
    } else if new.app.is_empty() != new.env.is_empty() {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "Give both app and env, or neither.",
        ));
    } else if !new.app.is_empty()
        && (!valid_name(&new.app, 63) || !matches!(new.env.as_str(), "dev" | "prod"))
    {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "The app must be a valid app name and the env must be dev or prod.",
        ));
    }
    if !allowed(&caller, true, &new.app, &new.env) {
        return Err(err(
            StatusCode::FORBIDDEN,
            "You don't have permission to make folders.",
        ));
    }

    let folder = Folder {
        name: new.name,
        disk,
        owner_app: new.app,
        owner_env: new.env,
        created_at: unix_now(),
        created_by: created_by(&caller),
    };
    let json = folder_json(&folder, 0, 0);
    let name = folder.name.clone();
    match s.files.db.run(move |c| db::folder_insert(c, &folder)).await {
        Ok(()) => Ok((StatusCode::CREATED, Json(json)).into_response()),
        Err(rusqlite::Error::SqliteFailure(e, _))
            if e.code == rusqlite::ErrorCode::ConstraintViolation =>
        {
            Err(err(
                StatusCode::CONFLICT,
                format!("There's already a folder {name}."),
            ))
        }
        Err(e) => Err(e.into()),
    }
}

/// DELETE /api/files/folders/<name>: deletes an empty folder. Files in its
/// trash go with it.
async fn delete_folder(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
) -> Reply {
    let folder = s.files.folder_for(&caller, &name, true).await?;
    let Some(disk) = s.files.disks.iter().find(|d| d.name == folder.disk) else {
        return Err(disk_error(
            "cannot delete the folder",
            format!("the disk {} is not configured", folder.disk),
        ));
    };
    let root = disk.path.clone();
    let key = name.clone();
    let outcome = s
        .files
        .db
        .run(move |c| {
            Ok(db::folder_delete(
                c,
                &key,
                |path| match std::fs::remove_file(root.join(path)) {
                    Ok(()) => Ok(()),
                    Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
                    Err(e) => Err(disk_error("cannot remove a trashed file", e)),
                },
            ))
        })
        .await??;
    match outcome {
        db::FolderDelete::Deleted => {}
        db::FolderDelete::NotEmpty => {
            return Err(err(
                StatusCode::CONFLICT,
                "The folder still has files. Delete them first.",
            ))
        }
        db::FolderDelete::Missing => {
            return Err(err(
                StatusCode::NOT_FOUND,
                format!("There's no folder {name}."),
            ))
        }
    }
    let base = disk.path.join("homelab-files");
    let _ = tokio::task::spawn_blocking(move || remove_empty_dirs(&base, &name)).await;
    Ok(StatusCode::NO_CONTENT.into_response())
}

/// Whether `path` is a real directory (not a link to one).
fn is_real_dir(path: &std::path::Path) -> bool {
    std::fs::symlink_metadata(path).is_ok_and(|m| m.is_dir())
}

/// Best effort: removes the empty year/month folders and the folder itself
/// under `<base>/<folder>`. Only `remove_dir`, so anything left in them stays.
fn remove_empty_dirs(base: &std::path::Path, folder: &str) {
    let top = base.join(folder);
    if !is_real_dir(base) || !is_real_dir(&top) {
        return;
    }
    let subdirs = |dir: &std::path::Path| -> Vec<PathBuf> {
        match std::fs::read_dir(dir) {
            Ok(entries) => entries
                .flatten()
                .map(|e| e.path())
                .filter(|p| is_real_dir(p))
                .collect(),
            Err(e) => {
                tracing::warn!(path = %dir.display(), "cannot list a storage folder: {e}");
                Vec::new()
            }
        }
    };
    let remove = |dir: &std::path::Path| {
        if let Err(e) = std::fs::remove_dir(dir) {
            tracing::warn!(path = %dir.display(), "cannot remove a storage folder: {e}");
        }
    };
    for year in subdirs(&top) {
        for month in subdirs(&year) {
            remove(&month);
        }
        remove(&year);
    }
    remove(&top);
}

// ---- tags and info ----

/// Checks one tag: 1-40 characters of `a-z 0-9 space _ -` (after lowercasing).
pub fn clean_tag(tag: &str) -> Option<String> {
    let tag = tag.to_lowercase();
    let ok = !tag.is_empty()
        && tag.chars().count() <= 40
        && tag
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, ' ' | '_' | '-'));
    ok.then_some(tag)
}

/// Checks a list of tags: at most 20, each valid.
pub fn clean_tags(tags: &[String]) -> Result<Vec<String>, ApiError> {
    if tags.len() > 20 {
        return Err(err(StatusCode::BAD_REQUEST, "Use at most 20 tags."));
    }
    tags.iter()
        .map(|t| {
            clean_tag(t).ok_or_else(|| {
                err(
                    StatusCode::BAD_REQUEST,
                    "A tag needs 1-40 letters, digits, spaces, _ or -.",
                )
            })
        })
        .collect()
}

fn file_json(f: &FileRow) -> Value {
    json!({
        "id": f.id,
        "folder": f.folder,
        "name": f.name,
        "content_type": f.content_type,
        "size": f.size,
        "sha256": f.sha256,
        "created_at": f.created_at,
        "created_by": f.created_by,
        "tags": f.tags,
        "info": f.info,
        "meta": f.meta,
    })
}

/// A file id from the address. Anything that is not a ULID is "not found".
fn parse_id(id: &str) -> Result<String, ApiError> {
    Ulid::from_string(id)
        .map(|u| u.to_string())
        .map_err(|_| err(StatusCode::NOT_FOUND, "There's no file with that id."))
}

/// Finds a file or answers 404.
async fn find_file(s: &AppState, folder: &str, id: &str) -> Result<FileRow, ApiError> {
    let (folder, id) = (folder.to_string(), parse_id(id)?);
    s.files
        .db
        .run(move |c| db::file_get(c, &folder, &id))
        .await?
        .ok_or_else(|| err(StatusCode::NOT_FOUND, "There's no file with that id."))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Patch {
    tags: Option<Vec<String>>,
    info: Option<Value>,
}

/// GET /api/files/<folder>/<id>/info
async fn info(
    State(s): State<AppState>,
    caller: Caller,
    Path((folder, id)): Path<(String, String)>,
) -> Reply {
    s.files.folder_for(&caller, &folder, false).await?;
    let file = find_file(&s, &folder, &id).await?;
    Ok(Json(file_json(&file)).into_response())
}

/// PATCH /api/files/<folder>/<id>/info
async fn patch_info(
    State(s): State<AppState>,
    caller: Caller,
    Path((folder, id)): Path<(String, String)>,
    body: Bytes,
) -> Reply {
    s.files.folder_for(&caller, &folder, true).await?;
    let patch: Patch = parse_body(&body)?;
    let tags = match &patch.tags {
        Some(t) => Some(serde_json::to_string(&clean_tags(t)?).unwrap_or_default()),
        None => None,
    };
    let info = match &patch.info {
        Some(v) if v.is_object() && v.to_string().len() <= 4096 => Some(v.to_string()),
        Some(_) => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "The info must be a JSON object of at most 4096 bytes.",
            ))
        }
        None => None,
    };
    // 404 if it is missing, before changing anything.
    find_file(&s, &folder, &id).await?;
    let (f, i) = (folder.clone(), parse_id(&id)?);
    s.files
        .db
        .run(move |c| db::file_update(c, &f, &i, tags.as_deref(), info.as_deref()))
        .await?;
    let file = find_file(&s, &folder, &id).await?;
    Ok(Json(file_json(&file)).into_response())
}

/// DELETE /api/files/<folder>/<id>: moves it to the trash.
async fn delete_file(
    State(s): State<AppState>,
    caller: Caller,
    Path((folder, id)): Path<(String, String)>,
) -> Reply {
    s.files.folder_for(&caller, &folder, true).await?;
    let id = parse_id(&id)?;
    let now = unix_now();
    let changed = s
        .files
        .db
        .run(move |c| db::file_trash(c, &folder, &id, now))
        .await?;
    if changed == 0 {
        return Err(err(StatusCode::NOT_FOUND, "There's no file with that id."));
    }
    Ok(StatusCode::NO_CONTENT.into_response())
}

// ---- listing ----

/// GET /api/files/<folder>?tag=&type=&after=&limit=&taken_after=&taken_before=&camera=
async fn list_files(
    State(s): State<AppState>,
    caller: Caller,
    Path(folder): Path<String>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    s.files.folder_for(&caller, &folder, false).await?;
    let limit = match q.get("limit") {
        None => 50,
        Some(l) => l
            .parse::<i64>()
            .map(|n| n.clamp(1, 200))
            .map_err(|_| err(StatusCode::BAD_REQUEST, "The limit must be a number."))?,
    };
    let after = match q.get("after") {
        Some(a) => Some(
            Ulid::from_string(a)
                .map_err(|_| {
                    err(
                        StatusCode::BAD_REQUEST,
                        "The after value must be a file id.",
                    )
                })?
                .to_string(),
        ),
        None => None,
    };
    let bound = |name: &str| -> Result<Option<String>, ApiError> {
        q.get(name)
            .map(|v| {
                meta::parse_taken_bound(v).ok_or_else(|| {
                    err(
                        StatusCode::BAD_REQUEST,
                        format!("The {name} value must be an RFC 3339 date or date-time."),
                    )
                })
            })
            .transpose()
    };
    let camera = match q.get("camera") {
        Some(c) if (1..=100).contains(&c.chars().count()) => Some(c.clone()),
        Some(_) => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "The camera value must be 1-100 characters.",
            ))
        }
        None => None,
    };
    let filter = db::ListFilter {
        tag: q.get("tag").cloned(),
        type_prefix: q.get("type").cloned(),
        after,
        taken_after: bound("taken_after")?,
        taken_before: bound("taken_before")?,
        camera,
    };
    // Ask for one extra to know whether there is a next page.
    let mut rows = s
        .files
        .db
        .run(move |c| db::file_list(c, &folder, &filter, limit + 1))
        .await?;
    let next = if rows.len() as i64 > limit {
        rows.truncate(limit as usize);
        rows.last().map(|f| f.id.clone())
    } else {
        None
    };
    let files: Vec<Value> = rows.iter().map(file_json).collect();
    Ok(Json(json!({ "files": files, "next": next })).into_response())
}

// ---- upload ----

/// What came in over the wire.
struct Received {
    size: u64,
    sha256: String,
    /// The first bytes, for sniffing the type.
    head: Vec<u8>,
}

/// Streams the body into the file at `path`, counting and hashing as it goes.
async fn receive(body: Body, path: &std::path::Path, max: u64) -> Result<Received, ApiError> {
    let mut out = tokio::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .await
        .map_err(|e| disk_error("cannot create the temp file", e))?;
    let mut hash = Sha256::new();
    let mut size = 0u64;
    let mut head = Vec::new();
    let mut stream = body.into_data_stream();
    while let Some(chunk) = stream.next().await {
        let chunk =
            chunk.map_err(|_| err(StatusCode::BAD_REQUEST, "The upload was interrupted."))?;
        size += chunk.len() as u64;
        if size > max {
            return Err(err(
                StatusCode::PAYLOAD_TOO_LARGE,
                format!(
                    "The file is bigger than the {} MB limit.",
                    max.div_ceil(1024 * 1024)
                ),
            ));
        }
        if head.len() < 8192 {
            let n = (8192 - head.len()).min(chunk.len());
            head.extend_from_slice(&chunk[..n]);
        }
        hash.update(&chunk);
        out.write_all(&chunk)
            .await
            .map_err(|e| disk_error("cannot write the temp file", e))?;
    }
    out.sync_all()
        .await
        .map_err(|e| disk_error("cannot sync the temp file", e))?;
    Ok(Received {
        size,
        sha256: hex::encode(hash.finalize()),
        head,
    })
}

/// POST /api/files/<folder>?name=&tags=&sha256=
///
/// 201 with the new file, or 200 with the existing file and
/// `X-Homelab-Duplicate: true` when the folder already holds the same content.
async fn upload(
    State(s): State<AppState>,
    caller: Caller,
    Path(folder): Path<String>,
    Query(q): Query<HashMap<String, String>>,
    body: Body,
) -> Reply {
    let folder = s.files.folder_for(&caller, &folder, true).await?;
    let name = safe_name(q.get("name").map(String::as_str).unwrap_or(""));
    let tags: Vec<String> = q
        .get("tags")
        .map(|t| {
            t.split(',')
                .filter(|t| !t.is_empty())
                .map(String::from)
                .collect()
        })
        .unwrap_or_default();
    let tags = clean_tags(&tags)?;
    let want_sha = match q.get("sha256") {
        None => None,
        Some(h) if h.len() == 64 && h.bytes().all(|b| b.is_ascii_hexdigit()) => {
            Some(h.to_lowercase())
        }
        Some(_) => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "The sha256 must be 64 hex digits.",
            ))
        }
    };
    let disk = s
        .files
        .disks
        .iter()
        .find(|d| d.name == folder.disk)
        .ok_or_else(|| disk_error("folder uses an unknown disk", &folder.disk))?
        .path
        .clone();

    let id = Ulid::generate();
    let (year, month) = year_month(id.timestamp_ms() / 1000);
    let (tmp_dir, dir) = {
        let (disk, name) = (disk.clone(), folder.name.clone());
        tokio::task::spawn_blocking(move || make_dirs(&disk, &name, year, month))
            .await
            .expect("blocking task panicked")
            .map_err(|e| disk_error("cannot prepare the storage folders", e))?
    };
    let tmp = tmp_dir.join(id.to_string());
    let file_name = format!("{id}-{name}");
    let target = dir.join(&file_name);

    let result = store(
        &s,
        body,
        Stored {
            tmp: &tmp,
            target: &target,
            dir: &dir,
            row: FileRow {
                id: id.to_string(),
                folder: folder.name.clone(),
                name,
                content_type: String::new(),
                size: 0,
                sha256: String::new(),
                created_at: unix_now(),
                created_by: created_by(&caller),
                app: caller.app.clone(),
                env: caller.env.clone(),
                tags,
                info: json!({}),
                meta: json!({}),
                taken_at: None,
                path: format!(
                    "homelab-files/{}/{year:04}/{month:02}/{file_name}",
                    folder.name
                ),
            },
            want_sha,
        },
    )
    .await;
    match result {
        Ok((row, false)) => Ok((StatusCode::CREATED, Json(file_json(&row))).into_response()),
        Ok((row, true)) => Ok((
            StatusCode::OK,
            [("x-homelab-duplicate", "true")],
            Json(file_json(&row)),
        )
            .into_response()),
        Err(e) => {
            // Leave nothing behind.
            let _ = tokio::fs::remove_file(&tmp).await;
            let _ = tokio::fs::remove_file(&target).await;
            Err(e)
        }
    }
}

/// Where an upload goes and what to record about it.
struct Stored<'a> {
    tmp: &'a std::path::Path,
    target: &'a std::path::Path,
    dir: &'a std::path::Path,
    row: FileRow,
    want_sha: Option<String>,
}

/// Receives the body, moves it into place and records it. The flag is true
/// when the folder already had the same content: nothing new is stored then.
async fn store(s: &AppState, body: Body, mut st: Stored<'_>) -> Result<(FileRow, bool), ApiError> {
    let got = receive(body, st.tmp, s.files.max_upload).await?;
    if st.want_sha.is_some_and(|w| w != got.sha256) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "The file doesn't match the sha256 you gave.",
        ));
    }
    // The same content in the same folder is kept only once. Other folders may
    // belong to other apps, so they keep their own copy.
    let (folder, sha) = (st.row.folder.clone(), got.sha256.clone());
    if let Some(existing) = s
        .files
        .db
        .run(move |c| db::file_by_sha(c, &folder, &sha))
        .await?
    {
        tokio::fs::remove_file(st.tmp)
            .await
            .map_err(|e| disk_error("cannot remove the duplicate upload", e))?;
        return Ok((existing, true));
    }
    st.row.size = got.size as i64;
    st.row.sha256 = got.sha256;
    // Never trust the client's Content-Type.
    st.row.content_type = infer::get(&got.head)
        .map(|t| t.mime_type())
        .unwrap_or("application/octet-stream")
        .to_string();

    // The body is complete and synced: read what the file says about itself.
    let found = meta::extract(
        st.tmp.to_path_buf(),
        st.row.content_type.clone(),
        &st.row.id,
    )
    .await;
    st.row.meta = found.meta;
    st.row.taken_at = found.taken_at;

    tokio::fs::rename(st.tmp, st.target)
        .await
        .map_err(|e| disk_error("cannot move the upload into place", e))?;
    // Make the rename survive a crash.
    let dir = tokio::fs::File::open(st.dir)
        .await
        .map_err(|e| disk_error("cannot open the storage folder", e))?;
    dir.sync_all()
        .await
        .map_err(|e| disk_error("cannot sync the storage folder", e))?;

    let row = st.row.clone();
    s.files.db.run(move |c| db::file_insert(c, &row)).await?;
    Ok((st.row, false))
}

// ---- download ----

/// What a Range header asks for.
#[derive(Debug, PartialEq, Eq)]
pub enum Wanted {
    /// No usable single range: send everything.
    Whole,
    /// Bytes first..=last.
    Part(u64, u64),
    /// Nothing in the file matches.
    Unsatisfiable,
}

/// Reads a `Range` header for a file of `size` bytes.
/// Only a single range is served; anything odd means the whole file.
pub fn parse_range(header: &str, size: u64) -> Wanted {
    let Some(spec) = header.trim().strip_prefix("bytes=") else {
        return Wanted::Whole;
    };
    if spec.contains(',') {
        return Wanted::Whole;
    }
    let Some((a, b)) = spec.trim().split_once('-') else {
        return Wanted::Whole;
    };
    let (a, b) = (a.trim(), b.trim());
    let num = |t: &str| t.parse::<u64>().ok();
    match (a.is_empty(), b.is_empty()) {
        // bytes=-n: the last n bytes.
        (true, false) => match num(b) {
            Some(0) => Wanted::Unsatisfiable,
            Some(_) if size == 0 => Wanted::Unsatisfiable,
            Some(n) => Wanted::Part(size.saturating_sub(n), size - 1),
            None => Wanted::Whole,
        },
        // bytes=a-: from a to the end.
        (false, true) => match num(a) {
            Some(first) if first < size => Wanted::Part(first, size - 1),
            Some(_) => Wanted::Unsatisfiable,
            None => Wanted::Whole,
        },
        // bytes=a-b
        (false, false) => match (num(a), num(b)) {
            (Some(first), Some(last)) if first <= last => {
                if first < size {
                    Wanted::Part(first, last.min(size - 1))
                } else {
                    Wanted::Unsatisfiable
                }
            }
            _ => Wanted::Whole,
        },
        (true, true) => Wanted::Whole,
    }
}

/// GET /api/files/<folder>/<id>: the bytes.
async fn download(
    State(s): State<AppState>,
    caller: Caller,
    Path((folder_name, id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Reply {
    let folder = s.files.folder_for(&caller, &folder_name, false).await?;
    let row = find_file(&s, &folder_name, &id).await?;
    let disk = s
        .files
        .disks
        .iter()
        .find(|d| d.name == folder.disk)
        .ok_or_else(|| disk_error("folder uses an unknown disk", &folder.disk))?;
    let path: PathBuf = disk.path.join(&row.path);

    let file = tokio::task::spawn_blocking(move || open_file(&path))
        .await
        .expect("blocking task panicked")
        .map_err(|e| disk_error("cannot open the stored file", e))?;
    let size = file
        .metadata()
        .map_err(|e| disk_error("cannot read the stored file", e))?
        .len();
    let mut file = tokio::fs::File::from_std(file);

    let wanted = match headers.get(RANGE).and_then(|v| v.to_str().ok()) {
        Some(r) => parse_range(r, size),
        None => Wanted::Whole,
    };
    let mut res = Response::builder()
        .header(
            CONTENT_TYPE,
            HeaderValue::from_str(&row.content_type)
                .unwrap_or(HeaderValue::from_static("application/octet-stream")),
        )
        .header(ACCEPT_RANGES, "bytes")
        .header(
            CONTENT_DISPOSITION,
            // The name was made safe when it was stored.
            format!("attachment; filename=\"{}\"", safe_name(&row.name)),
        )
        .header("x-content-type-options", "nosniff")
        .header(CACHE_CONTROL, "no-store")
        .header(ETAG, format!("\"{}\"", row.sha256));
    let (status, start, len) = match wanted {
        Wanted::Whole => (StatusCode::OK, 0, size),
        Wanted::Part(a, b) => {
            res = res.header(CONTENT_RANGE, format!("bytes {a}-{b}/{size}"));
            (StatusCode::PARTIAL_CONTENT, a, b - a + 1)
        }
        Wanted::Unsatisfiable => {
            return Ok(Response::builder()
                .status(StatusCode::RANGE_NOT_SATISFIABLE)
                .header(CONTENT_RANGE, format!("bytes */{size}"))
                .header(CONTENT_TYPE, "application/json")
                .body(Body::from(
                    json!({ "error": "That part of the file doesn't exist." }).to_string(),
                ))
                .expect("valid response"));
        }
    };
    if start > 0 {
        file.seek(SeekFrom::Start(start))
            .await
            .map_err(|e| disk_error("cannot read the stored file", e))?;
    }
    let stream = tokio_util::io::ReaderStream::new(file.take(len));
    res.status(status)
        .header(CONTENT_LENGTH, len)
        .body(Body::from_stream(stream))
        .map_err(|e| disk_error("cannot build the response", e))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::*;
    use http_body_util::BodyExt;
    use sha2::{Digest, Sha256};
    use tower::ServiceExt;

    const PNG: &[u8] = b"\x89PNG\r\n\x1a\n0123456789";

    async fn call(
        env: &TestEnv,
        who: &Value,
        method: &str,
        target: &str,
        body: &[u8],
        extra: &[(&str, &str)],
    ) -> (StatusCode, HeaderMap, Vec<u8>) {
        let req = request(who, method, target, body.to_vec(), extra);
        let res = env.router.clone().oneshot(req).await.unwrap();
        let (parts, body) = res.into_parts();
        (
            parts.status,
            parts.headers,
            body.collect().await.unwrap().to_bytes().to_vec(),
        )
    }

    /// Like `call`, with the body parsed as JSON.
    async fn json_call(
        env: &TestEnv,
        who: &Value,
        method: &str,
        target: &str,
        body: Value,
    ) -> (StatusCode, Value) {
        let bytes = if body.is_null() {
            Vec::new()
        } else {
            body.to_string().into_bytes()
        };
        let (status, _, out) = call(env, who, method, target, &bytes, &[]).await;
        (status, serde_json::from_slice(&out).unwrap_or(Value::Null))
    }

    async fn make_folder(env: &TestEnv, who: &Value, name: &str, app: &str, env_name: &str) {
        let body = json!({ "name": name, "app": app, "env": env_name });
        let (status, _) = json_call(env, who, "POST", "/api/files/folders", body).await;
        assert_eq!(status, StatusCode::CREATED);
    }

    async fn put(env: &TestEnv, who: &Value, query: &str, body: &[u8]) -> (StatusCode, Value) {
        let target = format!("/api/files/photos{query}");
        let (status, _, out) = call(
            env,
            who,
            "POST",
            &target,
            body,
            &[("content-type", "text/plain")],
        )
        .await;
        (status, serde_json::from_slice(&out).unwrap_or(Value::Null))
    }

    /// Everything inside `dir`, as paths.
    fn tree(dir: &std::path::Path) -> Vec<PathBuf> {
        let mut out = Vec::new();
        for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            out.push(entry.path());
            if entry.path().is_dir() {
                out.extend(tree(&entry.path()));
            }
        }
        out
    }

    fn files_on_disk(env: &TestEnv) -> usize {
        tree(&env.disk).iter().filter(|p| p.is_file()).count()
    }

    // ---- pure functions ----

    fn caller(kind: Kind, scopes: &[&str], app: &str, env: &str) -> Caller {
        Caller {
            v: 1,
            kind,
            person: "p".into(),
            key: "k".into(),
            app: app.into(),
            env: env.into(),
            scopes: scopes.iter().map(|s| s.to_string()).collect(),
            method: "GET".into(),
            path: "/".into(),
            ts: 0,
        }
    }

    #[test]
    fn access_rules() {
        let person = caller(Kind::Person, &[], "", "");
        assert!(allowed(&person, true, "", ""));
        assert!(allowed(&person, true, "notes", "dev"));

        let none = caller(Kind::Admin, &[], "", "");
        let read = caller(Kind::Admin, &["files:read"], "", "");
        let write = caller(Kind::Admin, &["files:write"], "", "");
        assert!(!allowed(&none, false, "", ""));
        assert!(!allowed(&none, true, "", ""));
        assert!(allowed(&read, false, "notes", "dev"));
        assert!(!allowed(&read, true, "notes", "dev"));
        assert!(allowed(&write, false, "", ""));
        assert!(allowed(&write, true, "notes", "prod"));

        let app = caller(Kind::App, &[], "notes", "dev");
        for write in [false, true] {
            assert!(allowed(&app, write, "notes", "dev"));
            assert!(!allowed(&app, write, "notes", "prod"));
            assert!(!allowed(&app, write, "other", "dev"));
            assert!(!allowed(&app, write, "", ""));
        }
        // An app caller without an app never matches admin-owned folders.
        let odd = caller(Kind::App, &[], "", "");
        assert!(!allowed(&odd, false, "", ""));
    }

    #[test]
    fn ranges() {
        use Wanted::*;
        assert_eq!(parse_range("bytes=0-4", 10), Part(0, 4));
        assert_eq!(parse_range("bytes=5-", 10), Part(5, 9));
        assert_eq!(parse_range("bytes=-3", 10), Part(7, 9));
        assert_eq!(parse_range("bytes=-30", 10), Part(0, 9));
        assert_eq!(parse_range("bytes=8-100", 10), Part(8, 9));
        assert_eq!(parse_range("bytes=10-", 10), Unsatisfiable);
        assert_eq!(parse_range("bytes=10-12", 10), Unsatisfiable);
        assert_eq!(parse_range("bytes=-0", 10), Unsatisfiable);
        assert_eq!(parse_range("bytes=0-0", 0), Unsatisfiable);
        assert_eq!(parse_range("bytes=0-1,4-5", 10), Whole);
        assert_eq!(parse_range("bytes=5-2", 10), Whole);
        assert_eq!(parse_range("bytes=x-2", 10), Whole);
        assert_eq!(parse_range("bytes=-", 10), Whole);
        assert_eq!(parse_range("lines=1-2", 10), Whole);
    }

    #[test]
    fn tags() {
        assert_eq!(
            clean_tag("Holiday 2024_a-b").as_deref(),
            Some("holiday 2024_a-b")
        );
        assert_eq!(clean_tag(""), None);
        assert_eq!(clean_tag("a/b"), None);
        assert_eq!(clean_tag("é"), None);
        assert!(clean_tag(&"a".repeat(40)).is_some());
        assert!(clean_tag(&"a".repeat(41)).is_none());
        let twenty: Vec<String> = (0..20).map(|i| format!("t{i}")).collect();
        assert!(clean_tags(&twenty).is_ok());
        let mut twenty_one = twenty;
        twenty_one.push("x".into());
        assert!(clean_tags(&twenty_one).is_err());
    }

    // ---- folders ----

    #[tokio::test]
    async fn creating_folders() {
        let env = test_env(1 << 20);
        let url = "/api/files/folders";

        // A person makes an admin folder.
        let (status, body) =
            json_call(&env, &person(), "POST", url, json!({"name": "photos"})).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(body["name"], "photos");
        assert_eq!(body["disk"], "main");
        assert_eq!(body["app"], "");
        assert_eq!(body["files"], 0);

        // Again: 409.
        let (status, _) = json_call(&env, &person(), "POST", url, json!({"name": "photos"})).await;
        assert_eq!(status, StatusCode::CONFLICT);

        // Admin keys need files:write.
        for scopes in [&[][..], &["files:read"][..]] {
            let (status, body) =
                json_call(&env, &admin(scopes), "POST", url, json!({"name": "a1"})).await;
            assert_eq!(status, StatusCode::FORBIDDEN);
            assert!(body["error"].is_string());
        }
        let writer = admin(&["files:write"]);
        let body = json!({"name": "a1", "app": "notes", "env": "prod"});
        let (status, body) = json_call(&env, &writer, "POST", url, body).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(
            (body["app"].as_str(), body["env"].as_str()),
            (Some("notes"), Some("prod"))
        );

        // An app owns what it makes, even if it names no app.
        let notes = app_key("notes", "dev");
        let (status, body) = json_call(&env, &notes, "POST", url, json!({"name": "n1"})).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(
            (body["app"].as_str(), body["env"].as_str()),
            (Some("notes"), Some("dev"))
        );
        // It may name itself.
        let body = json!({"name": "n2", "app": "notes", "env": "dev"});
        assert_eq!(
            json_call(&env, &notes, "POST", url, body).await.0,
            StatusCode::CREATED
        );
        // It may not name another app or env.
        for (a, e) in [("other", "dev"), ("notes", "prod")] {
            let body = json!({"name": "n3", "app": a, "env": e});
            assert_eq!(
                json_call(&env, &notes, "POST", url, body).await.0,
                StatusCode::FORBIDDEN
            );
        }

        // Bad requests.
        for body in [
            json!({"name": "Bad"}),
            json!({"name": ".."}),
            json!({"name": "folders"}),
            json!({"name": "x", "disk": "nope"}),
            json!({"name": "x", "app": "notes"}),
            json!({"name": "x", "app": "notes", "env": "staging"}),
            json!({"name": "x", "app": "../x", "env": "dev"}),
            json!({"nope": 1}),
        ] {
            let (status, body) = json_call(&env, &person(), "POST", url, body.clone()).await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
        }
    }

    #[tokio::test]
    async fn folder_list_depends_on_the_caller() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "pics", "", "").await;
        make_folder(&env, &person(), "notes-dev", "notes", "dev").await;
        make_folder(&env, &person(), "other-prod", "other", "prod").await;

        let names = |who: Value| {
            let env = &env;
            async move {
                let (status, body) =
                    json_call(env, &who, "GET", "/api/files/folders", Value::Null).await;
                assert_eq!(status, StatusCode::OK);
                body["folders"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|f| f["name"].as_str().unwrap().to_string())
                    .collect::<Vec<_>>()
            }
        };
        assert_eq!(names(person()).await, ["notes-dev", "other-prod", "pics"]);
        assert_eq!(names(admin(&["files:read"])).await.len(), 3);
        assert_eq!(names(admin(&[])).await.len(), 0);
        assert_eq!(names(app_key("notes", "dev")).await, ["notes-dev"]);
        assert_eq!(names(app_key("notes", "prod")).await.len(), 0);
    }

    // ---- files ----

    #[tokio::test]
    async fn upload_list_info_download() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;

        // The client says text/plain; the bytes say PNG.
        let (status, info) = put(&env, &person(), "?name=My%20Cat.png&tags=Pets,cute", PNG).await;
        assert_eq!(status, StatusCode::CREATED, "{info}");
        assert_eq!(info["name"], "My_Cat.png");
        assert_eq!(info["content_type"], "image/png");
        assert_eq!(info["size"], PNG.len());
        assert_eq!(info["sha256"], hex::encode(Sha256::digest(PNG)));
        assert_eq!(info["tags"], json!(["pets", "cute"]));
        assert_eq!(info["created_by"], "person:p1");
        let id = info["id"].as_str().unwrap().to_string();

        // It is on disk where the layout says, under the disk.
        let stored: Vec<_> = tree(&env.disk)
            .into_iter()
            .filter(|p| p.is_file())
            .collect();
        assert_eq!(stored.len(), 1);
        let rel = stored[0]
            .strip_prefix(&env.disk)
            .unwrap()
            .to_string_lossy()
            .to_string();
        assert!(rel.starts_with("homelab-files/photos/"), "{rel}");
        assert!(rel.ends_with(&format!("{id}-My_Cat.png")), "{rel}");
        // The temp folder is empty.
        assert_eq!(tree(&env.disk.join("homelab-files/.tmp")).len(), 0);

        // A second file, text, to test the filters.
        let (status, second) = put(&env, &person(), "?name=a.txt&tags=docs", b"hello").await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(second["content_type"], "application/octet-stream");

        let list = |query: &str| {
            let target = format!("/api/files/photos{query}");
            let env = &env;
            async move {
                json_call(env, &person(), "GET", &target, Value::Null)
                    .await
                    .1
            }
        };
        let all = list("").await;
        let ids: Vec<_> = all["files"]
            .as_array()
            .unwrap()
            .iter()
            .map(|f| f["id"].clone())
            .collect();
        assert_eq!(ids, [second["id"].clone(), json!(id)]); // newest first
        assert_eq!(all["next"], Value::Null);
        assert_eq!(
            list("?type=image/").await["files"]
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(list("?tag=docs").await["files"][0]["name"], "a.txt");
        assert_eq!(list("?tag=doc").await["files"].as_array().unwrap().len(), 0);
        let page = list("?limit=1").await;
        assert_eq!(page["files"].as_array().unwrap().len(), 1);
        assert_eq!(page["next"], second["id"]);
        let rest = list(&format!(
            "?limit=1&after={}",
            page["next"].as_str().unwrap()
        ))
        .await;
        assert_eq!(rest["files"][0]["id"], json!(id));
        assert_eq!(rest["next"], Value::Null);

        // Info.
        let (status, got) = json_call(
            &env,
            &person(),
            "GET",
            &format!("/api/files/photos/{id}/info"),
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(got, info);

        // Download.
        let (status, headers, bytes) = call(
            &env,
            &person(),
            "GET",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(bytes, PNG);
        assert_eq!(headers["content-type"], "image/png");
        assert_eq!(headers["content-length"], PNG.len().to_string().as_str());
        assert_eq!(headers["accept-ranges"], "bytes");
        assert_eq!(
            headers["content-disposition"],
            "attachment; filename=\"My_Cat.png\""
        );
        assert_eq!(headers["x-content-type-options"], "nosniff");
        assert_eq!(headers["cache-control"], "no-store");
        assert_eq!(
            headers["etag"],
            format!("\"{}\"", hex::encode(Sha256::digest(PNG))).as_str()
        );

        // Folder totals.
        let (_, body) = json_call(&env, &person(), "GET", "/api/files/folders", Value::Null).await;
        assert_eq!(body["folders"][0]["files"], 2);
        assert_eq!(body["folders"][0]["bytes"], PNG.len() + 5);
    }

    #[tokio::test]
    async fn ranges_on_download() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "?name=n.bin", b"0123456789").await;
        let target = format!("/api/files/photos/{}", info["id"].as_str().unwrap());
        let get = |range: &'static str| {
            let (env, target) = (&env, &target);
            async move { call(env, &person(), "GET", target, &[], &[("range", range)]).await }
        };

        let (status, headers, bytes) = get("bytes=2-4").await;
        assert_eq!(status, StatusCode::PARTIAL_CONTENT);
        assert_eq!(bytes, b"234");
        assert_eq!(headers["content-range"], "bytes 2-4/10");
        assert_eq!(headers["content-length"], "3");
        assert_eq!(get("bytes=-3").await.2, b"789");
        assert_eq!(get("bytes=7-").await.2, b"789");

        let (status, headers, _) = get("bytes=10-").await;
        assert_eq!(status, StatusCode::RANGE_NOT_SATISFIABLE);
        assert_eq!(headers["content-range"], "bytes */10");

        // Several ranges: the whole file.
        let (status, _, bytes) = get("bytes=0-1,3-4").await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(bytes, b"0123456789");
    }

    #[tokio::test]
    async fn too_big_uploads_are_refused() {
        let env = test_env(100);
        make_folder(&env, &person(), "photos", "", "").await;
        let (status, body) = put(&env, &person(), "", &[7u8; 101]).await;
        assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
        assert_eq!(body["error"], "The file is bigger than the 1 MB limit.");
        assert_eq!(files_on_disk(&env), 0);
        // Exactly the limit is fine.
        assert_eq!(
            put(&env, &person(), "", &[7u8; 100]).await.0,
            StatusCode::CREATED
        );
    }

    #[tokio::test]
    async fn wrong_sha256_is_refused() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let wrong = "0".repeat(64);
        let (status, _) = put(&env, &person(), &format!("?sha256={wrong}"), b"abc").await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(files_on_disk(&env), 0);
        let (status, _) = put(&env, &person(), "?sha256=xyz", b"abc").await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        // The right one (in capitals) works.
        let right = hex::encode(Sha256::digest(b"abc")).to_uppercase();
        let (status, _) = put(&env, &person(), &format!("?sha256={right}"), b"abc").await;
        assert_eq!(status, StatusCode::CREATED);
        // Nothing was recorded for the refused ones.
        let (_, body) = json_call(&env, &person(), "GET", "/api/files/photos", Value::Null).await;
        assert_eq!(body["files"].as_array().unwrap().len(), 1);
    }

    #[tokio::test]
    async fn bad_tags_are_refused() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (status, _) = put(&env, &person(), "?tags=ok,bad/tag", b"x").await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(files_on_disk(&env), 0);
    }

    #[tokio::test]
    async fn patching_tags_and_info() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "?tags=a", b"x").await;
        let target = format!("/api/files/photos/{}/info", info["id"].as_str().unwrap());

        let (status, body) = json_call(
            &env,
            &person(),
            "PATCH",
            &target,
            json!({"tags": ["B", "c d"]}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["tags"], json!(["b", "c d"]));
        assert_eq!(body["info"], json!({}));

        // Only info: tags stay.
        let (_, body) =
            json_call(&env, &person(), "PATCH", &target, json!({"info": {"w": 1}})).await;
        assert_eq!(body["tags"], json!(["b", "c d"]));
        assert_eq!(body["info"], json!({"w": 1}));

        for bad in [
            json!({"tags": ["no/slash"]}),
            json!({"tags": (0..21).map(|i| format!("t{i}")).collect::<Vec<_>>()}),
            json!({"info": [1]}),
            json!({"info": {"big": "x".repeat(5000)}}),
            json!({"name": "new"}),
        ] {
            let (status, _) = json_call(&env, &person(), "PATCH", &target, bad).await;
            assert_eq!(status, StatusCode::BAD_REQUEST);
        }
        // A body over 64 KiB is refused.
        let huge = vec![b' '; 70 * 1024];
        let (status, _, _) = call(&env, &person(), "PATCH", &target, &huge, &[]).await;
        assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
        // Unchanged by the bad ones.
        let (_, body) = json_call(&env, &person(), "GET", &target, Value::Null).await;
        assert_eq!(body["info"], json!({"w": 1}));
    }

    #[tokio::test]
    async fn deleted_files_are_hidden_then_purged() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "", b"bye").await;
        let id = info["id"].as_str().unwrap();
        let file = format!("/api/files/photos/{id}");

        let (status, _, _) = call(&env, &person(), "DELETE", &file, &[], &[]).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        for (method, target) in [
            ("GET", file.clone()),
            ("GET", format!("{file}/info")),
            ("DELETE", file.clone()),
        ] {
            let (status, _, _) = call(&env, &person(), method, &target, &[], &[]).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{method} {target}");
        }
        let (_, list) = json_call(&env, &person(), "GET", "/api/files/photos", Value::Null).await;
        assert_eq!(list["files"].as_array().unwrap().len(), 0);
        let (_, folders) =
            json_call(&env, &person(), "GET", "/api/files/folders", Value::Null).await;
        assert_eq!(folders["folders"][0]["files"], 0);
        assert_eq!(folders["folders"][0]["bytes"], 0);

        // Still on disk for now; a purge 29 days later keeps it, 31 days later removes it.
        let day = 24 * 60 * 60;
        assert_eq!(files_on_disk(&env), 1);
        assert_eq!(env.files.purge(unix_now() + 29 * day).await.unwrap(), 0);
        assert_eq!(files_on_disk(&env), 1);
        assert_eq!(env.files.purge(unix_now() + 31 * day).await.unwrap(), 1);
        assert_eq!(files_on_disk(&env), 0);
        assert_eq!(env.files.purge(unix_now() + 31 * day).await.unwrap(), 0);
    }

    // ---- access and safety ----

    #[tokio::test]
    async fn other_callers_see_no_folder() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "notes", "dev").await;
        let owner = app_key("notes", "dev");
        let (_, info) = put(&env, &owner, "?name=a.txt", b"mine").await;
        assert_eq!(info["created_by"], "app:notes/dev");
        let id = info["id"].as_str().unwrap();

        let others = [
            app_key("other", "dev"),
            app_key("notes", "prod"),
            admin(&[]),
        ];
        for who in &others {
            for (method, target) in [
                ("GET", "/api/files/photos".to_string()),
                ("POST", "/api/files/photos".to_string()),
                ("GET", format!("/api/files/photos/{id}")),
                ("GET", format!("/api/files/photos/{id}/info")),
                ("PATCH", format!("/api/files/photos/{id}/info")),
                ("DELETE", format!("/api/files/photos/{id}")),
                ("GET", "/api/files/missing".to_string()),
            ] {
                let (status, _, body) = call(&env, who, method, &target, b"{}", &[]).await;
                assert_eq!(status, StatusCode::NOT_FOUND, "{method} {target}");
                let body: Value = serde_json::from_slice(&body).unwrap();
                assert!(body["error"]
                    .as_str()
                    .unwrap()
                    .starts_with("There's no folder"));
            }
        }
        // Nothing was uploaded or deleted by them.
        assert_eq!(files_on_disk(&env), 1);
        let (status, _, bytes) = call(
            &env,
            &owner,
            "GET",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        assert_eq!((status, bytes.as_slice()), (StatusCode::OK, &b"mine"[..]));
    }

    #[tokio::test]
    async fn read_only_admin_can_read_not_write() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "", b"x").await;
        let id = info["id"].as_str().unwrap();
        let reader = admin(&["files:read"]);
        let (status, _, _) = call(
            &env,
            &reader,
            "GET",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(put(&env, &reader, "", b"y").await.0, StatusCode::FORBIDDEN);
        let (status, _, _) = call(
            &env,
            &reader,
            "DELETE",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        let writer = admin(&["files:write"]);
        let (status, info) = put(&env, &writer, "", b"z").await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(info["created_by"], "key:k1");
    }

    #[tokio::test]
    async fn path_tricks_find_nothing() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "", b"x").await;
        let id = info["id"].as_str().unwrap();
        let before = tree(env.dir.path()).len();

        for target in [
            "/api/files/..",
            "/api/files/%2e%2e",
            "/api/files/%2e%2e/%2e%2e",
            "/api/files/..%2Fdisk",
            "/api/files/photos%2F..%2F..",
            "/api/files/Photos",
            "/api/files/photos/..",
            "/api/files/photos/%2e%2e",
            "/api/files/photos/..%2F..%2Fetc%2Fpasswd",
            "/api/files/photos/not-an-id",
            "/api/files/photos/not-an-id/info",
            &format!("/api/files/photos/{id}/extra/more"),
            &format!("/api/files/photos/{id}%2F.."),
        ] {
            for method in ["GET", "POST", "DELETE"] {
                let (status, _, _) = call(&env, &person(), method, target, b"x", &[]).await;
                assert!(
                    status == StatusCode::NOT_FOUND || status == StatusCode::BAD_REQUEST,
                    "{method} {target}: {status}"
                );
            }
        }
        // Bad folder names when creating.
        for name in ["..", "../x", "a/b", "%2e%2e"] {
            let (status, _) = json_call(
                &env,
                &person(),
                "POST",
                "/api/files/folders",
                json!({"name": name}),
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{name}");
        }
        // A hostile file name stays a plain name inside the folder.
        let (status, info) = put(&env, &person(), "?name=..%2F..%2F..%2Fevil.txt", b"y").await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(info["name"], "_.._.._evil.txt");

        // Nothing new outside the disk's own folders.
        let outside: Vec<_> = tree(env.dir.path())
            .into_iter()
            .filter(|p| !p.starts_with(&env.disk) && !p.starts_with(env.dir.path().join("data")))
            .collect();
        assert!(outside.is_empty(), "{outside:?}");
        assert!(tree(env.dir.path()).len() > before); // only the second upload
    }

    #[tokio::test]
    async fn symlinked_storage_folders_are_refused() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let elsewhere = tempfile::tempdir().unwrap();
        std::os::unix::fs::symlink(elsewhere.path(), env.disk.join("homelab-files")).unwrap();

        let (status, body) = put(&env, &person(), "", b"x").await;
        assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
        assert!(body["error"].is_string());
        assert_eq!(tree(elsewhere.path()).len(), 0);
    }

    #[tokio::test]
    async fn symlinked_files_are_not_served() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "", b"x").await;
        let id = info["id"].as_str().unwrap();
        let stored = tree(&env.disk).into_iter().find(|p| p.is_file()).unwrap();
        let secret = env.dir.path().join("secret");
        std::fs::write(&secret, "secret").unwrap();
        std::fs::remove_file(&stored).unwrap();
        std::os::unix::fs::symlink(&secret, &stored).unwrap();

        let (status, _, bytes) = call(
            &env,
            &person(),
            "GET",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
        assert!(!String::from_utf8_lossy(&bytes).contains("secret"));
    }

    #[tokio::test]
    async fn wrong_methods_are_404() {
        let env = test_env(1 << 20);
        for (method, target) in [
            ("PUT", "/api/files/folders"),
            ("DELETE", "/api/files/photos"),
            ("POST", "/api/files/photos/01ARZ3NDEKTSV4RRFFQ69G5FAV"),
            ("POST", "/api/files/photos/01ARZ3NDEKTSV4RRFFQ69G5FAV/info"),
        ] {
            let (status, _, _) = call(&env, &person(), method, target, &[], &[]).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{method} {target}");
        }
    }

    // ---- deleting folders ----

    async fn folder_names(env: &TestEnv) -> Vec<String> {
        let (_, list) = json_call(env, &person(), "GET", "/api/files/folders", Value::Null).await;
        list["folders"]
            .as_array()
            .unwrap()
            .iter()
            .map(|f| f["name"].as_str().unwrap().to_string())
            .collect()
    }

    async fn delete_folder_call(env: &TestEnv, who: &Value, name: &str) -> (StatusCode, Value) {
        json_call(
            env,
            who,
            "DELETE",
            &format!("/api/files/folders/{name}"),
            Value::Null,
        )
        .await
    }

    #[tokio::test]
    async fn an_empty_folder_is_deleted() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (status, _) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert!(folder_names(&env).await.is_empty());
        let (status, body) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        assert_eq!(body["error"], "There's no folder photos.");
    }

    #[tokio::test]
    async fn a_folder_with_files_is_not_deleted() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        put(&env, &person(), "", b"keep me").await;
        let (status, body) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::CONFLICT);
        assert_eq!(
            body["error"],
            "The folder still has files. Delete them first."
        );
        assert_eq!(folder_names(&env).await, ["photos"]);
        assert_eq!(files_on_disk(&env), 1);
    }

    #[tokio::test]
    async fn trashed_files_go_with_the_folder() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        make_folder(&env, &person(), "other", "", "").await;
        let (_, info) = put(&env, &person(), "", b"bye").await;
        let id = info["id"].as_str().unwrap();
        let (status, _, _) = call(
            &env,
            &person(),
            "DELETE",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert_eq!(files_on_disk(&env), 1);

        let (status, _) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert_eq!(files_on_disk(&env), 0);
        assert!(!env.disk.join("homelab-files/photos").exists());
        assert!(env.disk.join("homelab-files").is_dir());
        assert_eq!(folder_names(&env).await, ["other"]);
        let rows = env
            .files
            .db
            .run(|c| c.query_row("SELECT COUNT(*) FROM files", [], |r| r.get::<_, i64>(0)))
            .await
            .unwrap();
        assert_eq!(rows, 0);
        // The purge has nothing left to do.
        let later = unix_now() + 31 * 24 * 60 * 60;
        assert_eq!(env.files.purge(later).await.unwrap(), 0);
    }

    #[tokio::test]
    async fn a_missing_disk_file_does_not_block_deleting() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "", b"gone").await;
        let id = info["id"].as_str().unwrap();
        call(
            &env,
            &person(),
            "DELETE",
            &format!("/api/files/photos/{id}"),
            &[],
            &[],
        )
        .await;
        for p in tree(&env.disk) {
            if p.is_file() {
                std::fs::remove_file(p).unwrap();
            }
        }
        let (status, _) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert!(folder_names(&env).await.is_empty());
    }

    #[tokio::test]
    async fn leftover_things_in_a_deleted_folder_stay() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let stray = env.disk.join("homelab-files/photos/2020/01");
        std::fs::create_dir_all(&stray).unwrap();
        std::fs::write(stray.join("stray.txt"), b"not ours").unwrap();
        let (status, _) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert!(stray.join("stray.txt").is_file());
    }

    #[tokio::test]
    async fn folder_delete_access() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "mine", "blog", "prod").await;
        make_folder(&env, &person(), "theirs", "shop", "prod").await;
        make_folder(&env, &person(), "adm", "", "").await;
        let blog = app_key("blog", "prod");
        let (status, _) = delete_folder_call(&env, &blog, "theirs").await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        let (status, _) = delete_folder_call(&env, &blog, "adm").await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        let (status, _) = delete_folder_call(&env, &blog, "mine").await;
        assert_eq!(status, StatusCode::NO_CONTENT);

        let reader = admin(&["files:read"]);
        let (status, _) = delete_folder_call(&env, &reader, "adm").await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        assert_eq!(folder_names(&env).await, ["adm", "theirs"]);
        let writer = admin(&["files:write"]);
        let (status, _) = delete_folder_call(&env, &writer, "adm").await;
        assert_eq!(status, StatusCode::NO_CONTENT);
    }

    #[tokio::test]
    async fn a_deleted_folder_name_can_be_used_again() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (status, _) = delete_folder_call(&env, &person(), "photos").await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        make_folder(&env, &person(), "photos", "", "").await;
        assert_eq!(
            put(&env, &person(), "", b"again").await.0,
            StatusCode::CREATED
        );
    }

    #[tokio::test]
    async fn folders_route_beats_the_file_routes() {
        let env = test_env(1 << 20);
        // DELETE goes to the folder delete (its message names a folder, the
        // file delete would say "no file").
        let (status, body) = delete_folder_call(&env, &person(), "x").await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        assert_eq!(body["error"], "There's no folder x.");
        for method in ["GET", "POST", "PUT", "PATCH"] {
            let (status, _, _) =
                call(&env, &person(), method, "/api/files/folders/x", &[], &[]).await;
            assert!(
                status == StatusCode::NOT_FOUND || status == StatusCode::METHOD_NOT_ALLOWED,
                "{method}: {status}"
            );
        }
    }

    // ---- metadata ----

    use crate::meta::fixtures;

    async fn list(env: &TestEnv, query: &str) -> (StatusCode, Value) {
        let target = format!("/api/files/photos{query}");
        json_call(env, &person(), "GET", &target, Value::Null).await
    }

    fn names(listing: &Value) -> Vec<String> {
        let mut v: Vec<String> = listing["files"]
            .as_array()
            .unwrap()
            .iter()
            .map(|f| f["name"].as_str().unwrap().to_string())
            .collect();
        v.sort();
        v
    }

    #[tokio::test]
    async fn upload_returns_meta() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;

        let (status, info) = put(&env, &person(), "?name=a.jpg", &fixtures::jpeg_with_exif()).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(info["content_type"], "image/jpeg");
        assert_eq!(info["meta"]["width"], 640);
        assert_eq!(info["meta"]["camera_make"], "ACME");
        assert_eq!(info["meta"]["taken_at"], "2023-07-09T20:36:33+08:00");
        assert!(info["meta"]["gps"]["lat"].is_f64());

        // The same object comes back from the info and the list.
        let target = format!("/api/files/photos/{}/info", info["id"].as_str().unwrap());
        let (_, again) = json_call(&env, &person(), "GET", &target, Value::Null).await;
        assert_eq!(again["meta"], info["meta"]);
        let (_, listing) = list(&env, "").await;
        assert_eq!(listing["files"][0]["meta"], info["meta"]);

        let (status, png) = put(&env, &person(), "?name=b.png", &fixtures::png()).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(png["meta"], json!({ "width": 3, "height": 2 }));

        // Nothing found: an empty object, not null or missing.
        let (status, text) = put(&env, &person(), "?name=c.txt", b"just words").await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(text["meta"], json!({}));
    }

    #[tokio::test]
    async fn the_client_cannot_choose_the_type_or_meta() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        // A PNG that calls itself a JPEG (by name and Content-Type), and the
        // other way round.
        let (_, info) = put(&env, &person(), "?name=x.jpg", &fixtures::png()).await;
        assert_eq!(info["content_type"], "image/png");
        assert_eq!(info["meta"]["width"], 3);
        let target = "/api/files/photos?name=y.png";
        let (status, _, out) = call(
            &env,
            &person(),
            "POST",
            target,
            &fixtures::jpeg_with_exif(),
            &[("content-type", "image/png")],
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        let out: Value = serde_json::from_slice(&out).unwrap();
        assert_eq!(out["content_type"], "image/jpeg");
        assert_eq!(out["meta"]["camera_make"], "ACME");
    }

    #[tokio::test]
    async fn garbage_claiming_to_be_a_jpeg_still_uploads() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (status, info) = put(&env, &person(), "?name=bad.jpg", &fixtures::fake_jpeg()).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(info["content_type"], "image/jpeg");
        assert_eq!(info["meta"], json!({}));
        // Truncated real files too.
        let whole = fixtures::jpeg_with_exif();
        for (i, len) in [10, 40, 100, whole.len() - 3].into_iter().enumerate() {
            let (status, _) = put(&env, &person(), &format!("?name=t{i}.jpg"), &whole[..len]).await;
            assert_eq!(status, StatusCode::CREATED, "{len}");
        }
    }

    #[tokio::test]
    async fn listing_by_date_and_camera() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let old = fixtures::jpeg_with_exif();
        let mut other = old.clone();
        for (from, to) in [
            (&b"2023:07:09"[..], &b"2021:01:02"[..]),
            (b"ACME\0", b"Zeta\0"),
            (b"Pocket Cam 3", b"Big Camera 9"),
            (b"+08:00", b"\0\0\0\0\0\0"),
        ] {
            let at = other.windows(from.len()).position(|w| w == from).unwrap();
            other[at..at + from.len()].copy_from_slice(to);
        }
        put(&env, &person(), "?name=acme.jpg", &old).await;
        put(&env, &person(), "?name=zeta.jpg", &other).await;
        put(&env, &person(), "?name=plain.png", &fixtures::png()).await;
        put(&env, &person(), "?name=note.txt", b"hi").await;

        let q = |s: &str| {
            let (env, s) = (&env, s.to_string());
            async move { list(env, &s).await }
        };
        // 2023-07-09 20:36 at +08:00 is 12:36 UTC.
        assert_eq!(names(&q("").await.1).len(), 4);
        assert_eq!(names(&q("?taken_after=2022-01-01").await.1), ["acme.jpg"]);
        assert_eq!(names(&q("?taken_before=2022-01-01").await.1), ["zeta.jpg"]);
        assert_eq!(
            names(&q("?taken_after=2021-01-02&taken_before=2024-01-01").await.1),
            ["acme.jpg", "zeta.jpg"]
        );
        // The first is inclusive, the second is not.
        assert_eq!(
            names(&q("?taken_after=2021-01-02T20:36:33Z").await.1),
            ["acme.jpg", "zeta.jpg"]
        );
        assert_eq!(
            names(&q("?taken_before=2021-01-02T20:36:33Z").await.1),
            Vec::<String>::new()
        );
        assert_eq!(
            names(
                &q("?taken_before=2023-07-09T12:36:34Z&taken_after=2023-07-09T12:36:33Z")
                    .await
                    .1
            ),
            ["acme.jpg"]
        );
        assert_eq!(
            names(
                &q("?taken_before=2023-07-09T12:36:33Z&taken_after=2023-01-01")
                    .await
                    .1
            ),
            Vec::<String>::new()
        );
        // A time zone offset is taken into account.
        assert_eq!(
            names(&q("?taken_after=2023-07-09T21:00:00%2B08:00").await.1),
            Vec::<String>::new()
        );
        assert_eq!(
            names(&q("?taken_after=2023-07-09T20:36:33%2B08:00").await.1),
            ["acme.jpg"]
        );

        assert_eq!(names(&q("?camera=acme").await.1), ["acme.jpg"]);
        assert_eq!(names(&q("?camera=POCKET").await.1), ["acme.jpg"]);
        assert_eq!(names(&q("?camera=acme%20pocket").await.1), ["acme.jpg"]);
        assert_eq!(names(&q("?camera=zet").await.1), ["zeta.jpg"]);
        assert_eq!(names(&q("?camera=camera").await.1), ["zeta.jpg"]);
        assert_eq!(names(&q("?camera=nikon").await.1), Vec::<String>::new());
        // % and _ are plain characters, and quotes do no harm.
        for odd in ["%25", "_", "'%20OR%201%3D1%20--", "%22"] {
            assert_eq!(
                names(&q(&format!("?camera={odd}")).await.1),
                Vec::<String>::new(),
                "{odd}"
            );
        }
        assert_eq!(
            names(&q("?camera=acme&taken_after=2022-01-01").await.1),
            ["acme.jpg"]
        );
        assert_eq!(
            names(&q("?camera=acme&taken_before=2022-01-01").await.1),
            Vec::<String>::new()
        );

        // Paging still works with the filters.
        let (_, page) = q("?taken_after=2000-01-01&limit=1").await;
        assert_eq!(page["files"].as_array().unwrap().len(), 1);
        assert!(page["next"].is_string());
        let after = page["next"].as_str().unwrap();
        let (_, page2) = q(&format!("?taken_after=2000-01-01&limit=1&after={after}")).await;
        assert_eq!(page2["files"].as_array().unwrap().len(), 1);
        assert!(page2["next"].is_null());
        assert_ne!(page["files"][0]["id"], page2["files"][0]["id"]);

        for bad in [
            "?taken_after=yesterday",
            "?taken_before=2023-13-01",
            "?taken_after=",
            "?taken_after=2023-07-09T10:00:00",
            "?camera=",
            &format!("?camera={}", "a".repeat(101)),
        ] {
            let (status, body) = q(bad).await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{bad}");
            assert!(body["error"].is_string());
        }
        let (status, _) = q(&format!("?camera={}", "a".repeat(100))).await;
        assert_eq!(status, StatusCode::OK);
    }

    #[tokio::test]
    async fn duplicates_are_stored_once_per_folder() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        make_folder(&env, &person(), "other", "", "").await;
        let jpeg = fixtures::jpeg_with_exif();

        let (status, first) = put(&env, &person(), "?name=a.jpg&tags=one", &jpeg).await;
        assert_eq!(status, StatusCode::CREATED);
        let on_disk = files_on_disk(&env);

        let (status, headers, out) = call(
            &env,
            &person(),
            "POST",
            "/api/files/photos?name=copy.jpg&tags=two",
            &jpeg,
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(headers["x-homelab-duplicate"], "true");
        let second: Value = serde_json::from_slice(&out).unwrap();
        // The existing file, untouched.
        assert_eq!(second, first);
        assert_eq!(second["name"], "a.jpg");
        assert_eq!(second["tags"], json!(["one"]));
        assert_eq!(files_on_disk(&env), on_disk);
        assert!(tree(&env.disk.join("homelab-files").join(".tmp")).is_empty());
        assert_eq!(list(&env, "").await.1["files"].as_array().unwrap().len(), 1);

        // A new upload has no such header.
        let (status, headers, _) =
            call(&env, &person(), "POST", "/api/files/photos", b"new", &[]).await;
        assert_eq!(status, StatusCode::CREATED);
        assert!(headers.get("x-homelab-duplicate").is_none());

        // Another folder keeps its own copy.
        let (status, headers, _) =
            call(&env, &person(), "POST", "/api/files/other", &jpeg, &[]).await;
        assert_eq!(status, StatusCode::CREATED);
        assert!(headers.get("x-homelab-duplicate").is_none());

        // A trashed file does not count.
        let target = format!("/api/files/photos/{}", first["id"].as_str().unwrap());
        let (status, _, _) = call(&env, &person(), "DELETE", &target, b"", &[]).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let (status, again) = put(&env, &person(), "?name=back.jpg", &jpeg).await;
        assert_eq!(status, StatusCode::CREATED);
        assert_ne!(again["id"], first["id"]);

        // A wrong sha256 is still refused, even for a duplicate.
        let (status, _) = put(
            &env,
            &person(),
            &format!("?sha256={}", "0".repeat(64)),
            &jpeg,
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn patch_cannot_change_meta() {
        let env = test_env(1 << 20);
        make_folder(&env, &person(), "photos", "", "").await;
        let (_, info) = put(&env, &person(), "?name=a.jpg", &fixtures::jpeg_with_exif()).await;
        let target = format!("/api/files/photos/{}/info", info["id"].as_str().unwrap());

        for body in [
            json!({ "meta": { "width": 1 } }),
            json!({ "meta": {}, "tags": ["x"] }),
            json!({ "taken_at": "2000-01-01T00:00:00" }),
            json!({ "info": { "meta": 1 }, "meta": null }),
        ] {
            let (status, out) = json_call(&env, &person(), "PATCH", &target, body).await;
            assert_eq!(status, StatusCode::BAD_REQUEST);
            assert!(out["error"].is_string());
        }
        let (_, now) = json_call(&env, &person(), "GET", &target, Value::Null).await;
        assert_eq!(now["meta"], info["meta"]);
        assert_eq!(now["tags"], json!([]));

        // Tags and info still work, and meta stays as it was.
        let body = json!({ "tags": ["x"], "info": { "meta": "mine" } });
        let (status, out) = json_call(&env, &person(), "PATCH", &target, body).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(out["meta"], info["meta"]);
        assert_eq!(out["info"], json!({ "meta": "mine" }));
        let (_, listing) = list(&env, "?taken_after=2023-01-01").await;
        assert_eq!(listing["files"].as_array().unwrap().len(), 1);
    }
}
