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

use crate::api::{err, ApiError, Reply};
use crate::app::{not_found, unix_now, AppState};
use crate::caller::{Caller, Kind};
use crate::config::Disk;
use crate::db::{self, Db, FileRow, Folder};
use crate::disk::{make_dirs, open_file, safe_name, valid_name, year_month};
use crate::logs::{about, who, Level};
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
    let (about_app, about_env) = about(&caller, &folder.owner_app, &folder.owner_env);
    let disk = folder.disk.clone();
    match s.files.db.run(move |c| db::folder_insert(c, &folder)).await {
        Ok(()) => {
            s.logs.event(
                "files",
                Level::Info,
                &format!("folder made: {name}"),
                json!({ "folder": name, "disk": disk, "by": who(&caller) }),
                &about_app,
                &about_env,
            );
            Ok((StatusCode::CREATED, Json(json)).into_response())
        }
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
    let event_name = name.clone();
    let _ = tokio::task::spawn_blocking(move || remove_empty_dirs(&base, &name)).await;
    let (about_app, about_env) = about(&caller, &folder.owner_app, &folder.owner_env);
    s.logs.event(
        "files",
        Level::Info,
        &format!("folder deleted: {event_name}"),
        json!({ "folder": event_name, "by": who(&caller) }),
        &about_app,
        &about_env,
    );
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
    let owner = s.files.folder_for(&caller, &folder, true).await?;
    let id = parse_id(&id)?;
    let now = unix_now();
    let (folder_name, file_id) = (folder.clone(), id.clone());
    let changed = s
        .files
        .db
        .run(move |c| db::file_trash(c, &folder, &id, now))
        .await?;
    if changed == 0 {
        return Err(err(StatusCode::NOT_FOUND, "There's no file with that id."));
    }
    let (about_app, about_env) = about(&caller, &owner.owner_app, &owner.owner_env);
    s.logs.event(
        "files",
        Level::Info,
        &format!("file deleted in {folder_name}"),
        json!({ "folder": folder_name, "id": file_id, "by": who(&caller) }),
        &about_app,
        &about_env,
    );
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
        Ok((row, false)) => {
            let (about_app, about_env) = about(&caller, &folder.owner_app, &folder.owner_env);
            s.logs.event(
                "files",
                Level::Info,
                &format!("file saved in {}: {}", row.folder, row.name),
                json!({
                    "folder": row.folder,
                    "name": row.name,
                    "id": row.id,
                    "size": row.size,
                    "by": who(&caller),
                }),
                &about_app,
                &about_env,
            );
            Ok((StatusCode::CREATED, Json(file_json(&row))).into_response())
        }
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
    let (about_app, about_env) = about(&caller, &folder.owner_app, &folder.owner_env);
    s.logs.event(
        "files",
        Level::Debug,
        &format!("file read in {folder_name}"),
        json!({ "folder": folder_name, "id": row.id, "size": len, "by": who(&caller) }),
        &about_app,
        &about_env,
    );
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
#[path = "files_tests.rs"]
mod tests;
