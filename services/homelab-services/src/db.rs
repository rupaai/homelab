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
    /// Found by the server on upload. Clients cannot change it.
    pub meta: serde_json::Value,
    /// `meta.taken_at` for searching: UTC if it had an offset, else as given.
    pub taken_at: Option<String>,
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

/// Never change this: databases in the field have already run it.
const MIGRATION_1: &str = "BEGIN;
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
             COMMIT;";

/// Brings the schema up to date, using `user_version`.
/// Add a new `if version < N` step here for later changes.
fn migrate(conn: &Connection) -> rusqlite::Result<()> {
    let version: i64 = conn.query_row("PRAGMA user_version", [], |r| r.get(0))?;
    if version < 1 {
        conn.execute_batch(MIGRATION_1)?;
    }
    if version < 2 {
        conn.execute_batch(
            "BEGIN;
             ALTER TABLE files ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';
             ALTER TABLE files ADD COLUMN taken_at TEXT;
             CREATE INDEX files_by_taken ON files(folder, taken_at);
             CREATE INDEX files_by_sha ON files(folder, sha256);
             PRAGMA user_version = 2;
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
                         app, env, tags, info, path, meta, taken_at";

fn file_from(r: &rusqlite::Row) -> rusqlite::Result<FileRow> {
    let tags: String = r.get(10)?;
    let info: String = r.get(11)?;
    let meta: String = r.get(13)?;
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
        meta: serde_json::from_str(&meta)
            .ok()
            .filter(serde_json::Value::is_object)
            .unwrap_or_else(|| serde_json::json!({})),
        taken_at: r.get(14)?,
    })
}

pub fn file_insert(conn: &Connection, f: &FileRow) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO files(id, folder, name, content_type, size, sha256, created_at, created_by,
                           app, env, tags, info, path, meta, taken_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
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
            f.path,
            f.meta.to_string(),
            f.taken_at
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

/// A non-trashed file of the folder with this content, if there is one.
pub fn file_by_sha(
    conn: &Connection,
    folder: &str,
    sha256: &str,
) -> rusqlite::Result<Option<FileRow>> {
    conn.query_row(
        &format!(
            "SELECT {FILE_COLS} FROM files
             WHERE folder = ? AND sha256 = ? AND trashed_at IS NULL
             ORDER BY id LIMIT 1"
        ),
        [folder, sha256],
        file_from,
    )
    .optional()
}

/// The optional filters of a file listing. All values are bound as parameters.
#[derive(Default)]
pub struct ListFilter {
    pub tag: Option<String>,
    pub type_prefix: Option<String>,
    pub after: Option<String>,
    /// `taken_at` at or after this (already normalised).
    pub taken_after: Option<String>,
    /// `taken_at` before this (already normalised).
    pub taken_before: Option<String>,
    /// Case-insensitive part of the camera make or model.
    pub camera: Option<String>,
}

/// Files that are not trashed, newest first. The filters are all optional.
pub fn file_list(
    conn: &Connection,
    folder: &str,
    f: &ListFilter,
    limit: i64,
) -> rusqlite::Result<Vec<FileRow>> {
    let mut stmt = conn.prepare(&format!(
        "SELECT {FILE_COLS} FROM files
         WHERE folder = ?1 AND trashed_at IS NULL
           AND (?2 IS NULL OR EXISTS (SELECT 1 FROM json_each(files.tags) WHERE value = ?2))
           AND (?3 IS NULL OR substr(content_type, 1, length(?3)) = ?3)
           AND (?4 IS NULL OR id < ?4)
           AND (?5 IS NULL OR taken_at >= ?5)
           AND (?6 IS NULL OR taken_at < ?6)
           AND (?7 IS NULL
                OR instr(lower(json_extract(meta, '$.camera_make')), lower(?7)) > 0
                OR instr(lower(json_extract(meta, '$.camera_model')), lower(?7)) > 0
                OR instr(lower(json_extract(meta, '$.camera_make') || ' '
                               || json_extract(meta, '$.camera_model')), lower(?7)) > 0)
         ORDER BY id DESC LIMIT ?8"
    ))?;
    let rows = stmt.query_map(
        params![
            folder,
            f.tag,
            f.type_prefix,
            f.after,
            f.taken_after,
            f.taken_before,
            f.camera,
            limit
        ],
        file_from,
    )?;
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

/// How `folder_delete` ended.
#[derive(Debug, PartialEq, Eq)]
pub enum FolderDelete {
    Deleted,
    /// The folder still has files that are not trashed.
    NotEmpty,
    Missing,
}

/// Deletes a folder that has no live files, with its trashed files, in one
/// transaction. `remove` is called with the relative path of each trashed file
/// (to remove it from disk) before anything is committed; if it fails, nothing
/// is changed.
pub fn folder_delete<E: From<rusqlite::Error>>(
    conn: &Connection,
    name: &str,
    mut remove: impl FnMut(&str) -> Result<(), E>,
) -> Result<FolderDelete, E> {
    let tx = conn.unchecked_transaction()?;
    let live: i64 = tx.query_row(
        "SELECT COUNT(*) FROM files WHERE folder = ? AND trashed_at IS NULL",
        [name],
        |r| r.get(0),
    )?;
    if live > 0 {
        return Ok(FolderDelete::NotEmpty);
    }
    let paths: Vec<String> = {
        let mut stmt = tx.prepare("SELECT path FROM files WHERE folder = ?")?;
        let rows = stmt.query_map([name], |r| r.get(0))?;
        rows.collect::<rusqlite::Result<_>>()?
    };
    for path in &paths {
        remove(path)?;
    }
    tx.execute("DELETE FROM files WHERE folder = ?", [name])?;
    if tx.execute("DELETE FROM folders WHERE name = ?", [name])? == 0 {
        return Ok(FolderDelete::Missing);
    }
    tx.commit()?;
    Ok(FolderDelete::Deleted)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn user_version(conn: &Connection) -> i64 {
        conn.query_row("PRAGMA user_version", [], |r| r.get(0))
            .unwrap()
    }

    #[tokio::test]
    async fn a_version_1_database_is_upgraded() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("files.db");
        {
            let conn = Connection::open(&path).unwrap();
            conn.execute_batch(MIGRATION_1).unwrap();
            assert_eq!(user_version(&conn), 1);
            conn.execute_batch(
                "INSERT INTO folders VALUES ('photos', 'main', '', '', 1, 'person:p1');
                 INSERT INTO files(id, folder, name, content_type, size, sha256, created_at,
                                   created_by, app, env, tags, info, path)
                 VALUES ('01AAAAAAAAAAAAAAAAAAAAAAAA', 'photos', 'a.jpg', 'image/jpeg', 3, 'ab',
                         1, 'person:p1', '', '', '[\"x\"]', '{\"k\":1}', 'homelab-files/a');",
            )
            .unwrap();
        }
        let db = Db::open(&path).unwrap();
        // Opening it again changes nothing.
        Db::open(&path).unwrap();
        let (version, row) = db
            .run(|c| {
                Ok((
                    user_version(c),
                    file_get(c, "photos", "01AAAAAAAAAAAAAAAAAAAAAAAA")?,
                ))
            })
            .await
            .unwrap();
        assert_eq!(version, 2);
        let row = row.unwrap();
        assert_eq!(row.meta, serde_json::json!({}));
        assert_eq!(row.taken_at, None);
        assert_eq!(row.tags, ["x"]);
        assert_eq!(row.info, serde_json::json!({"k": 1}));
    }
}
