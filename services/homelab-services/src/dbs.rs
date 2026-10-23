//! The databases service: which databases exist, and looking inside them.
//!
//! homelabd tells us about each app database (`register`). We keep that list in our own
//! SQLite file so it survives a restart. Postgres is reached over loopback with
//! tokio-postgres; SQLite files are opened read only with rusqlite.
//!
//! Read queries on Postgres run as the role `homelab_ro`, never as `app`. Its password is
//! random and kept only in the registry file (mode 0600). SQL text and values are never
//! logged and never put in an error message we make up (the database's own message is
//! returned to the caller as it is).

use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use futures_util::TryStreamExt;
use rusqlite::limits::Limit;
use rusqlite::types::ValueRef;
use rusqlite::{params, Connection, OpenFlags, OptionalExtension};
use tokio_postgres::{Client, Config, NoTls, SimpleQueryMessage};

use crate::secrets::Secrets;

/// The most rows a query returns.
pub const MAX_ROWS: usize = 500;
/// The most SQL a query may have, in bytes.
pub const MAX_SQL: usize = 100 * 1024;
/// How long a query may run.
const QUERY_LIMIT: Duration = Duration::from_secs(10);
/// How long to wait for a connection.
const CONNECT_LIMIT: Duration = Duration::from_secs(2);
/// The read-only Postgres role.
const RO_ROLE: &str = "homelab_ro";

/// Which database engine an app uses.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Engine {
    /// On 127.0.0.1 at this port.
    Postgres(u16),
    /// The file on this machine.
    Sqlite(PathBuf),
}

impl Engine {
    pub fn name(&self) -> &'static str {
        match self {
            Engine::Postgres(_) => "postgres",
            Engine::Sqlite(_) => "sqlite",
        }
    }
}

/// One registered database.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Reg {
    pub app: String,
    pub env: String,
    pub engine: Engine,
}

/// Why something failed. The text is safe to show.
#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    /// 400: the request or the SQL is wrong.
    Invalid(String),
    /// 404.
    Missing(String),
    /// 409.
    Conflict(String),
    /// 503: the database (or what we need to reach it) isn't available.
    Down(String),
    /// 500: our own state failed. Already logged.
    Internal,
}

fn invalid(m: impl Into<String>) -> Error {
    Error::Invalid(m.into())
}

/// What a query gave back. Cells are text or null.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Output {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<Option<String>>>,
    pub truncated: bool,
    pub command: String,
    /// Rows changed, for statements that return no rows.
    pub affected: u64,
}

/// One table, as listed by /tables.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Table {
    pub schema: String,
    pub name: String,
    pub rows: i64,
}

/// The facts shown in the list.
#[derive(Debug, PartialEq, Eq)]
pub struct Info {
    /// "ok", "down" or "empty".
    pub status: &'static str,
    pub size_bytes: i64,
    pub connections: Option<i64>,
    pub tables: i64,
}

/// The registry, plus the little state the Postgres part needs.
pub struct Dbs {
    conn: Arc<Mutex<Connection>>,
    /// Only one role repair at a time.
    repair: tokio::sync::Mutex<()>,
    /// How long a query may run. Tests make it short.
    limit: Duration,
}

// ---- the registry ----

impl Dbs {
    /// Opens (and if needed creates) the registry file.
    pub fn open(path: &Path) -> Result<Dbs, String> {
        Dbs::open_with_limit(path, QUERY_LIMIT)
    }

    pub fn open_with_limit(path: &Path, limit: Duration) -> Result<Dbs, String> {
        let why = |e: &dyn std::fmt::Display| {
            format!("cannot open the databases registry {}: {e}", path.display())
        };
        // The file holds the read-only roles' passwords: only us.
        {
            use std::os::unix::fs::OpenOptionsExt;
            std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .mode(0o600)
                .open(path)
                .map_err(|e| why(&e))?;
        }
        let conn = Connection::open(path).map_err(|e| why(&e))?;
        conn.execute_batch(
            "PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;
             CREATE TABLE IF NOT EXISTS dbs(
               app TEXT NOT NULL,
               env TEXT NOT NULL,
               engine TEXT NOT NULL,
               port INTEGER NOT NULL DEFAULT 0,
               path TEXT NOT NULL DEFAULT '',
               ro_password TEXT NOT NULL DEFAULT '',
               PRIMARY KEY(app, env));
             CREATE TABLE IF NOT EXISTS changes(
               id INTEGER PRIMARY KEY AUTOINCREMENT,
               app TEXT NOT NULL,
               env TEXT NOT NULL,
               person TEXT NOT NULL,
               at INTEGER NOT NULL,
               sql TEXT NOT NULL,
               ok INTEGER NOT NULL,
               rows INTEGER NOT NULL,
               ms INTEGER NOT NULL,
               error TEXT NOT NULL DEFAULT '',
               copy TEXT NOT NULL DEFAULT '');
             CREATE INDEX IF NOT EXISTS changes_by_db ON changes(app, env, id);",
        )
        .map_err(|e| why(&e))?;
        Ok(Dbs {
            conn: Arc::new(Mutex::new(conn)),
            repair: tokio::sync::Mutex::new(()),
            limit,
        })
    }

    async fn run<T: Send + 'static>(
        &self,
        f: impl FnOnce(&Connection) -> rusqlite::Result<T> + Send + 'static,
    ) -> Result<T, Error> {
        let db = self.conn.clone();
        tokio::task::spawn_blocking(move || {
            let conn = db.lock().unwrap_or_else(|e| e.into_inner());
            f(&conn)
        })
        .await
        .map_err(|_| Error::Internal)?
        .map_err(|e| {
            tracing::error!("databases registry error: {e}");
            Error::Internal
        })
    }

    /// Remembers a database. Calling it again replaces the entry; the read-only password
    /// is kept unless the engine or port changed.
    pub async fn register(&self, reg: Reg) -> Result<(), Error> {
        self.run(move |c| {
            let (engine, port, path) = match &reg.engine {
                Engine::Postgres(p) => ("postgres", *p as i64, String::new()),
                Engine::Sqlite(p) => ("sqlite", 0, p.to_string_lossy().into_owned()),
            };
            c.execute(
                "INSERT INTO dbs(app, env, engine, port, path) VALUES(?, ?, ?, ?, ?)
                 ON CONFLICT(app, env) DO UPDATE SET
                   ro_password = CASE WHEN engine = excluded.engine AND port = excluded.port
                                      THEN ro_password ELSE '' END,
                   engine = excluded.engine, port = excluded.port, path = excluded.path",
                params![reg.app, reg.env, engine, port, path],
            )?;
            Ok(())
        })
        .await
    }

    /// Forgets every environment of an app. Returns how many.
    pub async fn forget_app(&self, app: &str) -> Result<usize, Error> {
        let app = app.to_string();
        self.run(move |c| {
            c.execute("DELETE FROM changes WHERE app = ?", [&app])?;
            c.execute("DELETE FROM dbs WHERE app = ?", [&app])
        })
        .await
    }

    pub async fn get(&self, app: &str, env: &str) -> Result<Option<Reg>, Error> {
        let (app, env) = (app.to_string(), env.to_string());
        self.run(move |c| {
            c.query_row(
                "SELECT app, env, engine, port, path FROM dbs WHERE app = ? AND env = ?",
                [app, env],
                reg_from,
            )
            .optional()
        })
        .await
    }

    /// Every database, by app then env.
    pub async fn list(&self) -> Result<Vec<Reg>, Error> {
        self.run(|c| {
            let mut stmt =
                c.prepare("SELECT app, env, engine, port, path FROM dbs ORDER BY app, env")?;
            let rows = stmt.query_map([], reg_from)?;
            rows.collect()
        })
        .await
    }

    async fn ro_password(&self, app: &str, env: &str) -> Result<String, Error> {
        let (app, env) = (app.to_string(), env.to_string());
        self.run(move |c| {
            Ok(c.query_row(
                "SELECT ro_password FROM dbs WHERE app = ? AND env = ?",
                [app, env],
                |r| r.get(0),
            )
            .optional()?
            .unwrap_or_default())
        })
        .await
    }

    async fn set_ro_password(&self, app: &str, env: &str, password: &str) -> Result<(), Error> {
        let (app, env, password) = (app.to_string(), env.to_string(), password.to_string());
        self.run(move |c| {
            c.execute(
                "UPDATE dbs SET ro_password = ? WHERE app = ? AND env = ?",
                [password, app, env],
            )?;
            Ok(())
        })
        .await
    }
}

/// The most changes kept for one database.
pub const KEEP_CHANGES: i64 = 1000;

/// One write attempt, as recorded.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Change {
    /// Chosen when recorded; ignored on the way in.
    pub id: i64,
    pub app: String,
    pub env: String,
    pub person: String,
    /// Unix seconds.
    pub at: i64,
    pub sql: String,
    pub ok: bool,
    pub rows: i64,
    pub ms: i64,
    /// A short reason when it failed, else empty.
    pub error: String,
    /// The copy made just before, or empty.
    pub copy: String,
}

impl Dbs {
    /// Records a write attempt and drops the oldest ones beyond the newest
    /// `KEEP_CHANGES` of that database.
    pub async fn record_change(&self, change: Change) -> Result<(), Error> {
        self.run(move |c| {
            c.execute(
                "INSERT INTO changes(app, env, person, at, sql, ok, rows, ms, error, copy)
                 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                params![
                    change.app,
                    change.env,
                    change.person,
                    change.at,
                    change.sql,
                    change.ok,
                    change.rows,
                    change.ms,
                    change.error,
                    change.copy
                ],
            )?;
            c.execute(
                "DELETE FROM changes WHERE app = ?1 AND env = ?2 AND id <= (
                   SELECT id FROM changes WHERE app = ?1 AND env = ?2
                   ORDER BY id DESC LIMIT 1 OFFSET ?3)",
                params![change.app, change.env, KEEP_CHANGES],
            )?;
            Ok(())
        })
        .await
    }

    /// The newest changes of a database first, those with an id below `before` if given.
    pub async fn changes(
        &self,
        app: &str,
        env: &str,
        limit: usize,
        before: Option<i64>,
    ) -> Result<Vec<Change>, Error> {
        let (app, env) = (app.to_string(), env.to_string());
        self.run(move |c| {
            let mut stmt = c.prepare(
                "SELECT id, app, env, person, at, sql, ok, rows, ms, error, copy FROM changes
                 WHERE app = ?1 AND env = ?2 AND id < ?3 ORDER BY id DESC LIMIT ?4",
            )?;
            let rows = stmt.query_map(
                params![app, env, before.unwrap_or(i64::MAX), limit as i64],
                |r| {
                    Ok(Change {
                        id: r.get(0)?,
                        app: r.get(1)?,
                        env: r.get(2)?,
                        person: r.get(3)?,
                        at: r.get(4)?,
                        sql: r.get(5)?,
                        ok: r.get(6)?,
                        rows: r.get(7)?,
                        ms: r.get(8)?,
                        error: r.get(9)?,
                        copy: r.get(10)?,
                    })
                },
            )?;
            rows.collect()
        })
        .await
    }
}

fn reg_from(r: &rusqlite::Row) -> rusqlite::Result<Reg> {
    let engine: String = r.get(2)?;
    let port: i64 = r.get(3)?;
    let path: String = r.get(4)?;
    Ok(Reg {
        app: r.get(0)?,
        env: r.get(1)?,
        engine: if engine == "postgres" {
            Engine::Postgres(port as u16)
        } else {
            Engine::Sqlite(PathBuf::from(path))
        },
    })
}

// ---- Postgres ----

/// A connection. The background task is stopped when this is dropped.
struct Pg {
    client: Client,
    task: tokio::task::JoinHandle<()>,
}

impl Drop for Pg {
    fn drop(&mut self) {
        self.task.abort();
    }
}

/// Why connecting failed.
enum Connect {
    /// The server said no to this user or password.
    Auth,
    /// Anything else: not running, not ready, too slow.
    Down,
}

fn quote_ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

/// A random password: 32 lowercase letters and digits.
fn random_password() -> String {
    let mut bytes = [0u8; 20];
    let ok = std::fs::File::open("/dev/urandom")
        .and_then(|mut f| std::io::Read::read_exact(&mut f, &mut bytes))
        .is_ok();
    if !ok {
        // No randomness is a broken machine; the ulid still differs per call.
        bytes[..16].copy_from_slice(&ulid::Ulid::generate().to_bytes());
    }
    hex::encode(bytes)
}

impl Dbs {
    async fn pg_connect(&self, port: u16, user: &str, password: &str) -> Result<Pg, Connect> {
        let mut config = Config::new();
        config
            .host("127.0.0.1")
            .port(port)
            .user(user)
            .password(password)
            .dbname("app")
            .application_name("homelab")
            .connect_timeout(CONNECT_LIMIT)
            .options(format!("-c statement_timeout={}", self.limit.as_millis()));
        let connecting = tokio::time::timeout(CONNECT_LIMIT * 2, config.connect(NoTls));
        match connecting.await {
            Ok(Ok((client, connection))) => {
                let task = tokio::spawn(async move {
                    let _ = connection.await;
                });
                Ok(Pg { client, task })
            }
            Ok(Err(e)) => {
                let auth = e.code().is_some_and(|c| c.code().starts_with("28"));
                Err(if auth { Connect::Auth } else { Connect::Down })
            }
            Err(_) => Err(Connect::Down),
        }
    }

    /// A connection as `app`, with the password from the secrets store.
    async fn pg_app(&self, secrets: &Option<Arc<Secrets>>, reg: &Reg) -> Result<Pg, Error> {
        let Engine::Postgres(port) = reg.engine else {
            return Err(Error::Internal);
        };
        let secrets = secrets.clone().ok_or_else(|| {
            Error::Down(
                "The secrets service isn't running, so the database password can't be read.".into(),
            )
        })?;
        let (app, env) = (reg.app.clone(), reg.env.clone());
        let found =
            tokio::task::spawn_blocking(move || secrets.get(&app, &env, "DATABASE_PASSWORD", None))
                .await
                .map_err(|_| Error::Internal)?;
        let password = match found {
            Ok(Some(p)) => p.value,
            Ok(None) => {
                return Err(Error::Down(
                    "The database password isn't stored yet.".into(),
                ))
            }
            Err(_) => return Err(Error::Down("The database password can't be read.".into())),
        };
        self.pg_connect(port, "app", &password)
            .await
            .map_err(|c| match c {
                Connect::Auth => Error::Down("The database refused the app's password.".into()),
                Connect::Down => down(),
            })
    }

    /// A connection as the read-only role. Creates or repairs the role when the stored
    /// password doesn't work.
    async fn pg_ro(&self, secrets: &Option<Arc<Secrets>>, reg: &Reg) -> Result<Pg, Error> {
        let Engine::Postgres(port) = reg.engine else {
            return Err(Error::Internal);
        };
        let stored = self.ro_password(&reg.app, &reg.env).await?;
        if !stored.is_empty() {
            match self.pg_connect(port, RO_ROLE, &stored).await {
                Ok(pg) => return Ok(pg),
                Err(Connect::Down) => return Err(down()),
                Err(Connect::Auth) => {}
            }
        }
        let _guard = self.repair.lock().await;
        let admin = self.pg_app(secrets, reg).await?;
        let password = random_password();
        // The password is only letters and digits, so quoting it is simple.
        let sql = format!(
            "DO $$ BEGIN
               IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '{RO_ROLE}') THEN
                 CREATE ROLE {RO_ROLE} LOGIN;
               END IF;
             END $$;
             ALTER ROLE {RO_ROLE} WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
               NOREPLICATION PASSWORD '{password}';
             GRANT pg_read_all_data TO {RO_ROLE};
             ALTER ROLE {RO_ROLE} SET statement_timeout = '{}ms';",
            self.limit.as_millis()
        );
        admin.client.batch_execute(&sql).await.map_err(|e| {
            tracing::error!(app = %reg.app, env = %reg.env, "cannot set up the read-only role: {}", pg_message(&e));
            Error::Down("The read-only role could not be set up.".into())
        })?;
        drop(admin);
        self.set_ro_password(&reg.app, &reg.env, &password).await?;
        self.pg_connect(port, RO_ROLE, &password)
            .await
            .map_err(|_| down())
    }

    /// Runs SQL on Postgres: read only, or as `app` when `write`.
    async fn pg_query(
        &self,
        secrets: &Option<Arc<Secrets>>,
        reg: &Reg,
        sql: &str,
        write: bool,
        cap: usize,
    ) -> Result<Output, Error> {
        let pg = if write {
            self.pg_app(secrets, reg).await?
        } else {
            self.pg_ro(secrets, reg).await?
        };
        pg_run(&pg.client, sql, cap, self.limit).await
    }
}

fn down() -> Error {
    Error::Down("The database isn't reachable right now.".into())
}

/// The database's own message for an error, or a short plain one.
fn pg_message(e: &tokio_postgres::Error) -> String {
    match e.as_db_error() {
        Some(db) => db.message().to_string(),
        None => "The database connection failed.".to_string(),
    }
}

/// Runs SQL with the simple protocol, so every cell comes back as text. Stops reading
/// at `cap` rows.
async fn pg_run(client: &Client, sql: &str, cap: usize, limit: Duration) -> Result<Output, Error> {
    let work = async {
        let mut stream = std::pin::pin!(client.simple_query_raw(sql).await?);
        let mut out = Output::default();
        let mut has_columns = false;
        while let Some(message) = stream.try_next().await? {
            match message {
                SimpleQueryMessage::RowDescription(columns) => {
                    // With several statements the last result is the one we return.
                    out = Output::default();
                    out.columns = columns.iter().map(|c| c.name().to_string()).collect();
                    has_columns = true;
                }
                SimpleQueryMessage::Row(row) => {
                    if out.rows.len() >= cap {
                        out.truncated = true;
                        break;
                    }
                    out.rows.push(
                        (0..row.len())
                            .map(|i| row.get(i).map(str::to_string))
                            .collect(),
                    );
                }
                SimpleQueryMessage::CommandComplete(n) if !has_columns => out.affected = n,
                _ => {}
            }
        }
        out.command = if has_columns {
            format!("SELECT {}", out.rows.len())
        } else {
            format!("OK {}", out.affected)
        };
        Ok::<_, tokio_postgres::Error>(out)
    };
    match tokio::time::timeout(limit + Duration::from_secs(2), work).await {
        Ok(Ok(out)) => Ok(out),
        Ok(Err(e)) => match e.as_db_error() {
            Some(db) => Err(invalid(db.message())),
            None => Err(down()),
        },
        Err(_) => Err(invalid("The query took too long.")),
    }
}

// ---- SQLite ----

/// Opens a SQLite file for reading only: read-only open, `query_only`, and no ATTACH.
fn sqlite_open(path: &Path) -> Result<Connection, Error> {
    if !path.exists() {
        return Err(Error::Conflict(
            "The database file doesn't exist yet.".into(),
        ));
    }
    let conn = Connection::open_with_flags(
        path,
        OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )
    .map_err(|e| sqlite_error(&e))?;
    conn.busy_timeout(Duration::from_secs(2))
        .and_then(|_| conn.pragma_update(None, "query_only", true))
        .and_then(|_| conn.set_limit(Limit::SQLITE_LIMIT_ATTACHED, 0).map(|_| ()))
        .map_err(|e| sqlite_error(&e))?;
    Ok(conn)
}

fn sqlite_error(e: &rusqlite::Error) -> Error {
    match e {
        rusqlite::Error::MultipleStatement => invalid("Run one statement at a time."),
        rusqlite::Error::SqliteFailure(f, m) => {
            if f.code == rusqlite::ErrorCode::OperationInterrupted {
                invalid("The query took too long.")
            } else {
                invalid(m.clone().unwrap_or_else(|| e.to_string()))
            }
        }
        _ => invalid(e.to_string()),
    }
}

fn cell(v: ValueRef) -> Option<String> {
    match v {
        ValueRef::Null => None,
        ValueRef::Integer(i) => Some(i.to_string()),
        ValueRef::Real(f) => Some(f.to_string()),
        ValueRef::Text(t) => Some(String::from_utf8_lossy(t).into_owned()),
        ValueRef::Blob(b) => Some(format!("<blob {} bytes>", b.len())),
    }
}

/// Runs one statement on an open connection. Stops the query after `limit`.
fn sqlite_run(conn: &Connection, sql: &str, cap: usize, limit: Duration) -> Result<Output, Error> {
    // A timer thread interrupts the query; dropping `done` stops it.
    let handle = conn.get_interrupt_handle();
    let (done, wait) = std::sync::mpsc::channel::<()>();
    let timer = std::thread::spawn(move || {
        if wait.recv_timeout(limit) == Err(std::sync::mpsc::RecvTimeoutError::Timeout) {
            handle.interrupt();
        }
    });
    let result = (|| {
        let mut stmt = conn.prepare(sql).map_err(|e| sqlite_error(&e))?;
        let columns: Vec<String> = stmt.column_names().iter().map(|s| s.to_string()).collect();
        let width = columns.len();
        let mut out = Output {
            columns,
            ..Output::default()
        };
        if width == 0 {
            // A statement with no result (a write on a read-only file fails here).
            stmt.execute([]).map_err(|e| sqlite_error(&e))?;
            out.command = "OK".to_string();
            return Ok(out);
        }
        let mut rows = stmt.query([]).map_err(|e| sqlite_error(&e))?;
        while let Some(row) = rows.next().map_err(|e| sqlite_error(&e))? {
            if out.rows.len() >= cap {
                out.truncated = true;
                break;
            }
            let mut cells = Vec::with_capacity(width);
            for i in 0..width {
                cells.push(cell(row.get_ref(i).map_err(|e| sqlite_error(&e))?));
            }
            out.rows.push(cells);
        }
        out.command = format!("SELECT {}", out.rows.len());
        Ok(out)
    })();
    drop(done);
    let _ = timer.join();
    result
}

// ---- reading, for the routes ----

impl Dbs {
    /// The list facts for one database. Never fails: trouble shows as "down".
    pub async fn info(&self, secrets: &Option<Arc<Secrets>>, reg: &Reg) -> Info {
        let down = Info {
            status: "down",
            size_bytes: 0,
            connections: None,
            tables: 0,
        };
        match &reg.engine {
            Engine::Sqlite(path) => {
                let path = path.clone();
                let limit = self.limit;
                let found = tokio::task::spawn_blocking(move || sqlite_info(&path, limit)).await;
                found.unwrap_or(down)
            }
            Engine::Postgres(_) => {
                let sql = "SELECT pg_database_size(current_database())::text,
                                  (SELECT numbackends FROM pg_stat_database
                                   WHERE datname = current_database())::text,
                                  (SELECT count(*) FROM pg_class c
                                   JOIN pg_namespace n ON n.oid = c.relnamespace
                                   WHERE c.relkind IN ('r', 'p') AND NOT (n.nspname = 'pg_catalog'
                                     OR n.nspname = 'information_schema'
                                     OR n.nspname LIKE 'pg\\_toast%'
                                     OR n.nspname LIKE 'pg\\_temp%'))::text";
                match self.pg_query(secrets, reg, sql, false, 1).await {
                    Ok(out) if out.rows.len() == 1 => {
                        let num = |i: usize| out.rows[0][i].as_deref().and_then(|s| s.parse().ok());
                        Info {
                            status: "ok",
                            size_bytes: num(0).unwrap_or(0),
                            connections: num(1),
                            tables: num(2).unwrap_or(0),
                        }
                    }
                    _ => down,
                }
            }
        }
    }

    /// The tables of a database, with row counts.
    pub async fn tables(
        &self,
        secrets: &Option<Arc<Secrets>>,
        reg: &Reg,
    ) -> Result<Vec<Table>, Error> {
        match &reg.engine {
            Engine::Sqlite(path) => {
                let path = path.clone();
                let limit = self.limit;
                tokio::task::spawn_blocking(move || {
                    if !path.exists() {
                        return Ok(Vec::new());
                    }
                    sqlite_tables(&sqlite_open(&path)?, limit)
                })
                .await
                .map_err(|_| Error::Internal)?
            }
            Engine::Postgres(_) => {
                // Estimates from the planner; exact counts when the table is small.
                let sql = "SELECT n.nspname, c.relname, c.reltuples::bigint::text
                           FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                           WHERE c.relkind IN ('r', 'p') AND NOT (n.nspname = 'pg_catalog'
                             OR n.nspname = 'information_schema'
                             OR n.nspname LIKE 'pg\\_toast%'
                             OR n.nspname LIKE 'pg\\_temp%')
                           ORDER BY 1, 2";
                let pg = self.pg_ro(secrets, reg).await?;
                let listed = pg_run(&pg.client, sql, 5000, self.limit).await?;
                let mut tables = Vec::new();
                for row in listed.rows {
                    let (schema, name) = (
                        row[0].clone().unwrap_or_default(),
                        row[1].clone().unwrap_or_default(),
                    );
                    let estimate: i64 =
                        row[2].as_deref().and_then(|s| s.parse().ok()).unwrap_or(-1);
                    let rows = if (0..10_000).contains(&estimate) || estimate < 0 {
                        let sql = format!(
                            "SELECT count(*)::text FROM {}.{}",
                            quote_ident(&schema),
                            quote_ident(&name)
                        );
                        let counted = pg_run(&pg.client, &sql, 1, self.limit).await?;
                        counted.rows[0][0]
                            .as_deref()
                            .and_then(|s| s.parse().ok())
                            .unwrap_or(0)
                    } else {
                        estimate
                    };
                    tables.push(Table { schema, name, rows });
                }
                Ok(tables)
            }
        }
    }

    /// A page of one table. The table must be one that `tables` lists.
    pub async fn table_rows(
        &self,
        secrets: &Option<Arc<Secrets>>,
        reg: &Reg,
        schema: &str,
        table: &str,
        limit: usize,
        offset: u64,
    ) -> Result<Output, Error> {
        let listed = self.tables(secrets, reg).await?;
        let found = listed.iter().any(|t| {
            t.name == table && (matches!(reg.engine, Engine::Sqlite(_)) || t.schema == schema)
        });
        if !found {
            return Err(Error::Missing("There's no such table.".into()));
        }
        let target = match reg.engine {
            Engine::Postgres(_) => format!("{}.{}", quote_ident(schema), quote_ident(table)),
            Engine::Sqlite(_) => quote_ident(table),
        };
        // One more than asked for, to know if there is more.
        let sql = format!("SELECT * FROM {target} LIMIT {} OFFSET {offset}", limit + 1);
        let mut out = self.exec(secrets, reg, &sql, false, limit + 1).await?;
        if out.rows.len() > limit {
            out.rows.truncate(limit);
            out.truncated = true;
        }
        Ok(out)
    }

    /// Runs SQL. `write` is only for Postgres.
    pub async fn query(
        &self,
        secrets: &Option<Arc<Secrets>>,
        reg: &Reg,
        sql: &str,
        write: bool,
    ) -> Result<Output, Error> {
        if sql.trim().is_empty() {
            return Err(invalid("Write some SQL first."));
        }
        if sql.len() > MAX_SQL {
            return Err(invalid("That SQL is too long (100 KB at most)."));
        }
        if write && matches!(reg.engine, Engine::Sqlite(_)) {
            return Err(invalid("Writing is only for Postgres databases."));
        }
        self.exec(secrets, reg, sql, write, MAX_ROWS).await
    }

    async fn exec(
        &self,
        secrets: &Option<Arc<Secrets>>,
        reg: &Reg,
        sql: &str,
        write: bool,
        cap: usize,
    ) -> Result<Output, Error> {
        match &reg.engine {
            Engine::Postgres(_) => self.pg_query(secrets, reg, sql, write, cap).await,
            Engine::Sqlite(path) => {
                let (path, sql, limit) = (path.clone(), sql.to_string(), self.limit);
                tokio::task::spawn_blocking(move || {
                    sqlite_run(&sqlite_open(&path)?, &sql, cap, limit)
                })
                .await
                .map_err(|_| Error::Internal)?
            }
        }
    }

    /// Writes a consistent copy of a SQLite database to `to` with the online backup API.
    /// `to` must not exist and its folder must.
    pub async fn snapshot(&self, reg: &Reg, to: &Path) -> Result<(), Error> {
        let Engine::Sqlite(from) = &reg.engine else {
            return Err(Error::Conflict(
                "Only SQLite databases can be copied here.".into(),
            ));
        };
        let (from, to) = (from.clone(), to.to_path_buf());
        tokio::task::spawn_blocking(move || sqlite_snapshot(&from, &to))
            .await
            .map_err(|_| Error::Internal)?
    }
}

fn sqlite_info(path: &Path, limit: Duration) -> Info {
    let down = Info {
        status: "down",
        size_bytes: 0,
        connections: None,
        tables: 0,
    };
    let Ok(meta) = std::fs::metadata(path) else {
        return Info {
            status: "empty",
            ..down
        };
    };
    let tables = sqlite_open(path).and_then(|c| sqlite_tables(&c, limit));
    match tables {
        Ok(t) => Info {
            status: "ok",
            size_bytes: meta.len() as i64,
            connections: None,
            tables: t.len() as i64,
        },
        Err(_) => down,
    }
}

fn sqlite_tables(conn: &Connection, limit: Duration) -> Result<Vec<Table>, Error> {
    let names = sqlite_run(
        conn,
        "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY name",
        5000,
        limit,
    )?;
    let mut tables = Vec::new();
    for row in names.rows {
        let name = row[0].clone().unwrap_or_default();
        let counted = sqlite_run(
            conn,
            &format!("SELECT count(*) FROM {}", quote_ident(&name)),
            1,
            limit,
        )?;
        let rows = counted.rows[0][0].as_deref().and_then(|s| s.parse().ok());
        tables.push(Table {
            schema: "main".to_string(),
            name,
            rows: rows.unwrap_or(0),
        });
    }
    Ok(tables)
}

fn sqlite_snapshot(from: &Path, to: &Path) -> Result<(), Error> {
    use rusqlite::backup::Backup;
    if !from.exists() {
        return Err(Error::Conflict(
            "The database file doesn't exist yet.".into(),
        ));
    }
    if !to.is_absolute() {
        return Err(invalid("The target must be an absolute path."));
    }
    if to.exists() {
        return Err(Error::Conflict("The target already exists.".into()));
    }
    let Some(parent) = to.parent().filter(|p| p.is_dir()) else {
        return Err(invalid("The target's folder doesn't exist."));
    };
    // Write next to the target under another name, then move it into place.
    let temp = parent.join(format!(".snapshot-{}.tmp", ulid::Ulid::generate()));
    let copied = (|| -> Result<(), String> {
        let source = Connection::open_with_flags(
            from,
            OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
        )
        .map_err(|e| e.to_string())?;
        source
            .busy_timeout(Duration::from_secs(5))
            .map_err(|e| e.to_string())?;
        let mut dest = Connection::open(&temp).map_err(|e| e.to_string())?;
        {
            let backup = Backup::new(&source, &mut dest).map_err(|e| e.to_string())?;
            backup
                .run_to_completion(256, Duration::from_millis(5), None)
                .map_err(|e| e.to_string())?;
        }
        // One self-contained file, not a WAL database.
        dest.pragma_update(None, "journal_mode", "DELETE")
            .map_err(|e| e.to_string())?;
        dest.close().map_err(|(_, e)| e.to_string())?;
        std::fs::File::open(&temp)
            .and_then(|f| f.sync_all())
            .map_err(|e| e.to_string())?;
        // A hard link fails if the target appeared meanwhile.
        std::fs::hard_link(&temp, to).map_err(|e| e.to_string())
    })();
    let _ = std::fs::remove_file(&temp);
    copied.map_err(|why| {
        tracing::error!("sqlite snapshot failed: {why}");
        Error::Conflict(format!("The copy failed: {why}"))
    })
}

#[cfg(test)]
impl Dbs {
    /// The stored read-only password, for tests that check it is kept or forgotten.
    pub async fn ro_password_for_test(&self, app: &str, env: &str) -> String {
        self.ro_password(app, env).await.unwrap()
    }
}
