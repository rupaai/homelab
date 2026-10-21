//! The metadata database (SQLite).

use std::path::Path;
use std::sync::{Arc, Mutex};

use rusqlite::{params, Connection, OptionalExtension};

/// One shared connection. Calls run on a blocking thread.
#[derive(Clone)]
pub struct Db(Arc<Mutex<Connection>>);

pub struct Folder {
    pub name: String,
    pub disk: String,
    /// Empty means admin-owned.
    pub owner_app: String,
    pub owner_env: String,
    pub created_at: i64,
    pub created_by: String,
}

#[derive(Clone)]
pub struct FileRow {
    pub id: String,
    pub folder: String,
    pub name: String,
    pub content_type: String,
    pub size: i64,
    pub sha256: String,
    pub created_at: i64,
    pub created_by: String,
    pub app: String,
    pub env: String,
    pub tags: Vec<String>,
    pub info: serde_json::Value,
    /// Relative to the disk's path.
    pub path: String,
}

impl Db {
    /// Opens (and if needed creates or upgrades) the database file.
    pub fn open(path: &Path) -> Result<Db, String> {
        let conn = Connection::open(path)
            .map_err(|e| format!("cannot open the files database {}: {e}", path.display()))?;
        conn.execute_batch(
            "PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;",
        )
        .and_then(|_| migrate(&conn))
        .map_err(|e| format!("cannot set up the files database: {e}"))?;
        Ok(Db(Arc::new(Mutex::new(conn))))
    }

    /// Runs `f` with the connection on a blocking thread.
    pub async fn run<T: Send + 'static>(
        &self,
        f: impl FnOnce(&Connection) -> rusqlite::Result<T> + Send + 'static,
    ) -> rusqlite::Result<T> {
        let db = self.0.clone();
        tokio::task::spawn_blocking(move || {
            let conn = db.lock().unwrap_or_else(|e| e.into_inner());
            f(&conn)
        })
        .await
        .expect("database task panicked")
    }
}

/// Brings the schema up to date, using `user_version`.
/// Add a new `if version < N` step here for later changes.
fn migrate(conn: &Connection) -> rusqlite::Result<()> {
    let version: i64 = conn.query_row("PRAGMA user_version", [], |r| r.get(0))?;
    if version < 1 {
        conn.execute_batch(
            "BEGIN;
             CREATE TABLE folders(
               name TEXT PRIMARY KEY,
               disk TEXT NOT NULL,
               owner_app TEXT NOT NULL DEFAULT '',
               owner_env TEXT NOT NULL DEFAULT '',
               created_at INTEGER NOT NULL,
               created_by TEXT NOT NULL);
             CREATE TABLE files(
               id TEXT PRIMARY KEY,
               folder TEXT NOT NULL REFERENCES folders(name),
               name TEXT NOT NULL,
               content_type TEXT NOT NULL,
               size INTEGER NOT NULL,
               sha256 TEXT NOT NULL,
               created_at INTEGER NOT NULL,
               created_by TEXT NOT NULL,
               app TEXT NOT NULL,
               env TEXT NOT NULL,
               tags TEXT NOT NULL DEFAULT '[]',
               info TEXT NOT NULL DEFAULT '{}',
               path TEXT NOT NULL,
               trashed_at INTEGER);
             CREATE INDEX files_by_folder ON files(folder, trashed_at, id);
             PRAGMA user_version = 1;
             COMMIT;",
        )?;
    }
    Ok(())
}

fn folder_from(r: &rusqlite::Row) -> rusqlite::Result<Folder> {
    Ok(Folder {
        name: r.get(0)?,
        disk: r.get(1)?,
        owner_app: r.get(2)?,
        owner_env: r.get(3)?,
        created_at: r.get(4)?,
        created_by: r.get(5)?,
    })
}

pub fn folder_get(conn: &Connection, name: &str) -> rusqlite::Result<Option<Folder>> {
    conn.query_row(
        "SELECT name, disk, owner_app, owner_env, created_at, created_by FROM folders WHERE name = ?",
        [name],
        folder_from,
    )
    .optional()
}

/// Every folder with its count and total size of files that are not trashed.
pub fn folder_list(conn: &Connection) -> rusqlite::Result<Vec<(Folder, i64, i64)>> {
    let mut stmt = conn.prepare(
        "SELECT f.name, f.disk, f.owner_app, f.owner_env, f.created_at, f.created_by,
                COUNT(x.id), COALESCE(SUM(x.size), 0)
         FROM folders f LEFT JOIN files x ON x.folder = f.name AND x.trashed_at IS NULL
         GROUP BY f.name ORDER BY f.name",
    )?;
    let rows = stmt.query_map([], |r| Ok((folder_from(r)?, r.get(6)?, r.get(7)?)))?;
    rows.collect()
}

pub fn folder_insert(conn: &Connection, f: &Folder) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO folders(name, disk, owner_app, owner_env, created_at, created_by)
         VALUES (?, ?, ?, ?, ?, ?)",
        params![
            f.name,
            f.disk,
            f.owner_app,
            f.owner_env,
            f.created_at,
            f.created_by
        ],
    )
    .map(|_| ())
}

const FILE_COLS: &str = "id, folder, name, content_type, size, sha256, created_at, created_by, \
                         app, env, tags, info, path";

fn file_from(r: &rusqlite::Row) -> rusqlite::Result<FileRow> {
    let tags: String = r.get(10)?;
    let info: String = r.get(11)?;
    Ok(FileRow {
        id: r.get(0)?,
        folder: r.get(1)?,
        name: r.get(2)?,
        content_type: r.get(3)?,
        size: r.get(4)?,
        sha256: r.get(5)?,
        created_at: r.get(6)?,
        created_by: r.get(7)?,
        app: r.get(8)?,
        env: r.get(9)?,
        tags: serde_json::from_str(&tags).unwrap_or_default(),
        info: serde_json::from_str(&info).unwrap_or_else(|_| serde_json::json!({})),
        path: r.get(12)?,
    })
}

pub fn file_insert(conn: &Connection, f: &FileRow) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO files(id, folder, name, content_type, size, sha256, created_at, created_by,
                           app, env, tags, info, path)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        params![
            f.id,
            f.folder,
            f.name,
            f.content_type,
            f.size,
            f.sha256,
            f.created_at,
            f.created_by,
            f.app,
            f.env,
            serde_json::to_string(&f.tags).unwrap_or_default(),
            f.info.to_string(),
            f.path
        ],
    )
    .map(|_| ())
}

/// One file that is not trashed.
pub fn file_get(conn: &Connection, folder: &str, id: &str) -> rusqlite::Result<Option<FileRow>> {
    conn.query_row(
        &format!(
            "SELECT {FILE_COLS} FROM files WHERE folder = ? AND id = ? AND trashed_at IS NULL"
        ),
        [folder, id],
        file_from,
    )
    .optional()
}

/// Files that are not trashed, newest first. The filters are all optional.
pub fn file_list(
    conn: &Connection,
    folder: &str,
    tag: Option<&str>,
    type_prefix: Option<&str>,
    after: Option<&str>,
    limit: i64,
) -> rusqlite::Result<Vec<FileRow>> {
    let mut stmt = conn.prepare(&format!(
        "SELECT {FILE_COLS} FROM files
         WHERE folder = ?1 AND trashed_at IS NULL
           AND (?2 IS NULL OR EXISTS (SELECT 1 FROM json_each(files.tags) WHERE value = ?2))
           AND (?3 IS NULL OR substr(content_type, 1, length(?3)) = ?3)
           AND (?4 IS NULL OR id < ?4)
         ORDER BY id DESC LIMIT ?5"
    ))?;
    let rows = stmt.query_map(params![folder, tag, type_prefix, after, limit], file_from)?;
    rows.collect()
}

/// Sets new tags and/or info (already serialized). Returns how many rows changed.
pub fn file_update(
    conn: &Connection,
    folder: &str,
    id: &str,
    tags: Option<&str>,
    info: Option<&str>,
) -> rusqlite::Result<usize> {
    conn.execute(
        "UPDATE files SET tags = COALESCE(?1, tags), info = COALESCE(?2, info)
         WHERE folder = ?3 AND id = ?4 AND trashed_at IS NULL",
        params![tags, info, folder, id],
    )
}

pub fn file_trash(conn: &Connection, folder: &str, id: &str, now: i64) -> rusqlite::Result<usize> {
    conn.execute(
        "UPDATE files SET trashed_at = ? WHERE folder = ? AND id = ? AND trashed_at IS NULL",
        params![now, folder, id],
    )
}

/// Files trashed before `cutoff`: (id, disk, path).
pub fn trashed_before(
    conn: &Connection,
    cutoff: i64,
) -> rusqlite::Result<Vec<(String, String, String)>> {
    let mut stmt = conn.prepare(
        "SELECT x.id, f.disk, x.path FROM files x JOIN folders f ON f.name = x.folder
         WHERE x.trashed_at < ?",
    )?;
    let rows = stmt.query_map([cutoff], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?;
    rows.collect()
}

pub fn file_delete(conn: &Connection, id: &str) -> rusqlite::Result<()> {
    conn.execute("DELETE FROM files WHERE id = ?", [id])
        .map(|_| ())
}
