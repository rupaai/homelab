//! The logs service: log lines from apps and Homelab's own events, in one SQLite file
//! (`logs.db`) with full-text search.
//!
//! * homelabd reads the apps' containers and sends the lines (`/internal/logs/ingest`) and its
//!   own events (`/internal/logs/event`). See `api`.
//! * `parse` reads one line (JSON or plain text).
//! * Lines older than 14 days go, and the file is kept under a size cap. See `retain`.
//!
//! Events never hold secret values, SQL, query values, file contents or keys.

mod api;
mod parse;
#[cfg(test)]
mod tests;

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use chrono::{DateTime, SecondsFormat};
use rusqlite::types::Value as Sql;
use rusqlite::{params, params_from_iter, Connection};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::caller::{Caller, Kind};
use crate::disk::valid_name;

pub use api::{refuse_path, routes};
pub use parse::{parse_line, Parsed, MAX_EXTRA, MAX_LINE};

/// Lines older than this are deleted.
pub const KEEP_DAYS: i64 = 14;
/// The size cap when the settings don't say.
pub const DEFAULT_MAX_MB: u64 = 2048;
/// The most lines one search gives back.
pub const MAX_LIMIT: usize = 500;
/// The most search words and characters used.
const MAX_WORDS: usize = 10;
const MAX_QUERY_CHARS: usize = 200;
/// Deletes happen in steps of this many lines, so a reader never waits long.
const DELETE_STEP: i64 = 5000;

/// How bad a line is.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Level {
    Unknown = 0,
    Debug = 1,
    Info = 2,
    Warn = 3,
    Error = 4,
    Fatal = 5,
}

impl Level {
    pub const ALL: [Level; 6] = [
        Level::Debug,
        Level::Info,
        Level::Warn,
        Level::Error,
        Level::Fatal,
        Level::Unknown,
    ];

    pub fn name(self) -> &'static str {
        match self {
            Level::Unknown => "unknown",
            Level::Debug => "debug",
            Level::Info => "info",
            Level::Warn => "warn",
            Level::Error => "error",
            Level::Fatal => "fatal",
        }
    }

    /// Reads a level name as the API and the settings spell it (lowercase).
    pub fn parse(text: &str) -> Option<Level> {
        Level::ALL.into_iter().find(|l| l.name() == text.trim())
    }

    fn from_number(n: i64) -> Level {
        Level::ALL
            .into_iter()
            .find(|l| *l as i64 == n)
            .unwrap_or(Level::Unknown)
    }
}

/// The settings from `HOMELAB_SERVICES_LOGS`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Settings {
    /// Homelab's own events below this are not stored.
    pub min_level: Level,
    /// A different minimum for some of Homelab's services (`files`, `deploys`...).
    pub services: BTreeMap<String, Level>,
    /// The size cap of the file, in MB.
    pub max_mb: u64,
}

impl Default for Settings {
    fn default() -> Settings {
        Settings {
            min_level: Level::Info,
            services: BTreeMap::new(),
            max_mb: DEFAULT_MAX_MB,
        }
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RawSettings {
    min_level: Option<String>,
    services: Option<BTreeMap<String, String>>,
    max_mb: Option<u64>,
}

impl Settings {
    /// Reads the JSON text. Unset or empty means the defaults. Errors are one plain line.
    pub fn parse(text: Option<&str>) -> Result<Settings, String> {
        let Some(text) = text.map(str::trim).filter(|t| !t.is_empty()) else {
            return Ok(Settings::default());
        };
        let bad = |why: &str| {
            format!(
                "HOMELAB_SERVICES_LOGS must be JSON like \
                 {{\"min_level\":\"info\",\"services\":{{\"files\":\"debug\"}},\"max_mb\":2048}}: {why}"
            )
        };
        let raw: RawSettings = serde_json::from_str(text).map_err(|e| bad(&e.to_string()))?;
        let level = |text: &str| match Level::parse(text) {
            Some(l) if l != Level::Unknown => Ok(l),
            _ => Err(bad(&format!(
                "\"{text}\" is not a level (debug, info, warn, error or fatal)"
            ))),
        };
        let mut settings = Settings::default();
        if let Some(l) = &raw.min_level {
            settings.min_level = level(l)?;
        }
        for (name, l) in raw.services.unwrap_or_default() {
            if !valid_name(&name, 60) {
                return Err(bad(&format!(
                    "the service name \"{name}\" must be 1-60 lowercase letters, digits, _ or -"
                )));
            }
            settings.services.insert(name, level(&l)?);
        }
        if let Some(mb) = raw.max_mb {
            if mb == 0 {
                return Err(bad("max_mb must be 1 or more"));
            }
            settings.max_mb = mb;
        }
        Ok(settings)
    }

    /// The lowest level stored for events of this service.
    pub fn min_for(&self, service: &str) -> Level {
        self.services
            .get(service)
            .copied()
            .unwrap_or(self.min_level)
    }
}

/// One line as an app wrote it, from homelabd.
pub struct RawLine {
    /// Unix milliseconds, from Docker.
    pub t: i64,
    /// `stdout` or `stderr`.
    pub stream: String,
    pub text: String,
}

/// A stored line.
#[derive(Debug, Clone)]
pub struct Line {
    pub id: i64,
    pub ts: i64,
    pub level: Level,
    pub app: String,
    pub env: String,
    pub service: String,
    pub stream: String,
    pub msg: String,
    pub extra: String,
}

impl Line {
    /// The line as the API gives it.
    pub fn to_json(&self) -> Value {
        let extra = if self.extra.is_empty() {
            json!({})
        } else {
            serde_json::from_str(&self.extra).unwrap_or_else(|_| json!({}))
        };
        json!({
            "id": self.id,
            "time": iso(self.ts),
            "level": self.level.name(),
            "msg": self.msg,
            "app": self.app,
            "env": self.env,
            "service": self.service,
            "stream": self.stream,
            "extra": extra,
        })
    }
}

/// What a search is limited to, by who asks.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Scope {
    /// Everything.
    All,
    /// Lines of this app environment and Homelab's lines about it.
    App { app: String, env: String },
}

/// A search. Empty fields are not filters.
#[derive(Debug, Clone, Default)]
pub struct Query {
    /// Words to find (see `fts_query`).
    pub q: String,
    pub app: String,
    pub env: String,
    pub service: String,
    /// Exactly these levels.
    pub levels: Vec<Level>,
    /// This level and above (and lines of unknown level).
    pub min_level: Option<Level>,
    pub since: Option<i64>,
    pub until: Option<i64>,
    /// Lines older than this id.
    pub before: Option<i64>,
    /// Lines newer than this id.
    pub after: Option<i64>,
    /// 0 means 100.
    pub limit: usize,
}

/// The answer to a search.
#[derive(Debug)]
pub struct Found {
    /// Newest first.
    pub lines: Vec<Line>,
    /// Lines per level (every filter except the level ones and the paging).
    pub counts: BTreeMap<Level, i64>,
    /// The `before` for the next page, if there is one.
    pub next: Option<i64>,
}

/// What a clean-up removed.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Removed {
    pub old: usize,
    pub for_size: usize,
}

/// The log store.
pub struct Logs {
    conn: Mutex<Connection>,
    path: PathBuf,
    settings: Settings,
}

const SCHEMA: &str = "
CREATE TABLE IF NOT EXISTS lines(
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  level INTEGER NOT NULL,
  app TEXT NOT NULL,
  env TEXT NOT NULL,
  service TEXT NOT NULL,
  stream TEXT NOT NULL,
  msg TEXT NOT NULL,
  extra TEXT NOT NULL DEFAULT '',
  about_app TEXT NOT NULL DEFAULT '',
  about_env TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS lines_ts ON lines(ts);
CREATE INDEX IF NOT EXISTS lines_app ON lines(app, env, ts);
CREATE INDEX IF NOT EXISTS lines_about ON lines(about_app, about_env, ts);
CREATE VIRTUAL TABLE IF NOT EXISTS lines_fts USING fts5(
  msg, extra, content='lines', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS lines_ai AFTER INSERT ON lines BEGIN
  INSERT INTO lines_fts(rowid, msg, extra) VALUES (new.id, new.msg, new.extra);
END;
CREATE TRIGGER IF NOT EXISTS lines_ad AFTER DELETE ON lines BEGIN
  INSERT INTO lines_fts(lines_fts, rowid, msg, extra) VALUES ('delete', old.id, old.msg, old.extra);
END;
PRAGMA user_version = 1;";

impl Logs {
    /// Opens (and if needed creates) the database file.
    pub fn open(path: &Path, settings: Settings) -> Result<Logs, String> {
        let conn = Connection::open(path)
            .map_err(|e| format!("cannot open the logs database {}: {e}", path.display()))?;
        // auto_vacuum only works if it is set before the first table is made.
        conn.execute_batch(
            "PRAGMA auto_vacuum = INCREMENTAL; PRAGMA journal_mode = WAL; \
             PRAGMA synchronous = NORMAL; PRAGMA busy_timeout = 5000;",
        )
        .and_then(|_| conn.execute_batch(SCHEMA))
        .map_err(|e| format!("cannot set up the logs database: {e}"))?;
        Ok(Logs {
            conn: Mutex::new(conn),
            path: path.to_path_buf(),
            settings,
        })
    }

    pub fn settings(&self) -> &Settings {
        &self.settings
    }

    fn lock(&self) -> MutexGuard<'_, Connection> {
        self.conn.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// Stores an app's lines, leaving out the ones below `min_level` (lines of unknown level
    /// are always kept). Returns how many were stored.
    pub fn ingest(
        &self,
        app: &str,
        env: &str,
        service: &str,
        min_level: Level,
        lines: &[RawLine],
    ) -> Result<usize, String> {
        let now = now_ms();
        let mut conn = self.lock();
        let tx = conn.transaction().map_err(db_error)?;
        let mut stored = 0;
        {
            let mut insert = tx
                .prepare_cached(
                    "INSERT INTO lines(ts, level, app, env, service, stream, msg, extra) \
                     VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                )
                .map_err(db_error)?;
            for raw in lines {
                if raw.text.trim().is_empty() {
                    continue;
                }
                let t = if raw.t > 0 { raw.t } else { now };
                let p = parse_line(&raw.text, &raw.stream, t);
                if p.level != Level::Unknown && p.level < min_level {
                    continue;
                }
                insert
                    .execute(params![
                        p.ts,
                        p.level as i64,
                        app,
                        env,
                        service,
                        raw.stream,
                        p.msg,
                        p.extra
                    ])
                    .map_err(db_error)?;
                stored += 1;
            }
        }
        tx.commit().map_err(db_error)?;
        Ok(stored)
    }

    /// Stores one Homelab event, if its level is high enough for the service. Returns whether
    /// it was stored. It is short and never fails the caller: a problem goes to the log.
    ///
    /// `about_app` and `about_env` say which app environment the event is about (empty for
    /// none); the line then also shows in that app's logs. Never put secret values, SQL,
    /// query values, file contents or keys in an event.
    pub fn event(
        &self,
        service: &str,
        level: Level,
        msg: &str,
        extra: Value,
        about_app: &str,
        about_env: &str,
    ) -> bool {
        if level == Level::Unknown || level < self.settings.min_for(service) {
            return false;
        }
        let extra = match extra {
            Value::Object(map) => parse::extra_text(&map),
            _ => String::new(),
        };
        let result = self.lock().execute(
            "INSERT INTO lines(ts, level, app, env, service, stream, msg, extra, about_app, about_env) \
             VALUES (?, ?, 'homelab', '', ?, 'event', ?, ?, ?, ?)",
            params![
                now_ms(),
                level as i64,
                service,
                parse::cut(msg),
                extra,
                about_app,
                about_env
            ],
        );
        match result {
            Ok(_) => true,
            Err(e) => {
                tracing::warn!("cannot store a log event: {e}");
                false
            }
        }
    }

    /// Finds lines. See `Query` and `Scope`.
    pub fn search(&self, scope: &Scope, query: &Query) -> Result<Found, String> {
        let limit = match query.limit {
            0 => 100,
            n => n.min(MAX_LIMIT),
        };
        let base = base_filter(scope, query);

        // The lines: the filters, the level filters and the paging.
        let mut filter = base.clone();
        if !query.levels.is_empty() {
            let marks = vec!["?"; query.levels.len()].join(",");
            filter.add(
                format!("level IN ({marks})"),
                query.levels.iter().map(|l| Sql::Integer(*l as i64)),
            );
        }
        if let Some(min) = query.min_level {
            filter.add(
                "(level >= ? OR level = 0)".to_string(),
                [Sql::Integer(min as i64)],
            );
        }
        if let Some(id) = query.before {
            filter.add("id < ?".to_string(), [Sql::Integer(id)]);
        }
        if let Some(id) = query.after {
            filter.add("id > ?".to_string(), [Sql::Integer(id)]);
        }
        // Following new lines gives the oldest new ones first, so none is skipped.
        let tail = query.after.is_some();
        let sql = format!(
            "SELECT id, ts, level, app, env, service, stream, msg, extra FROM lines {} \
             ORDER BY id {} LIMIT {}",
            filter.sql(),
            if tail { "ASC" } else { "DESC" },
            limit + 1
        );

        let conn = self.lock();
        let mut stmt = conn.prepare(&sql).map_err(db_error)?;
        let mut lines = stmt
            .query_map(params_from_iter(filter.args.iter()), |r| {
                Ok(Line {
                    id: r.get(0)?,
                    ts: r.get(1)?,
                    level: Level::from_number(r.get(2)?),
                    app: r.get(3)?,
                    env: r.get(4)?,
                    service: r.get(5)?,
                    stream: r.get(6)?,
                    msg: r.get(7)?,
                    extra: r.get(8)?,
                })
            })
            .map_err(db_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(db_error)?;
        let more = lines.len() > limit;
        lines.truncate(limit);
        if tail {
            lines.reverse();
        }
        let next = if more && !tail {
            lines.last().map(|l| l.id)
        } else {
            None
        };

        // The counts: the filters only.
        let sql = format!(
            "SELECT level, COUNT(*) FROM lines {} GROUP BY level",
            base.sql()
        );
        let mut counts: BTreeMap<Level, i64> = Level::ALL.into_iter().map(|l| (l, 0)).collect();
        let mut stmt = conn.prepare(&sql).map_err(db_error)?;
        let rows = stmt
            .query_map(params_from_iter(base.args.iter()), |r| {
                Ok((r.get::<_, i64>(0)?, r.get::<_, i64>(1)?))
            })
            .map_err(db_error)?;
        for row in rows {
            let (level, n) = row.map_err(db_error)?;
            counts.insert(Level::from_number(level), n);
        }
        Ok(Found {
            lines,
            counts,
            next,
        })
    }

    /// Deletes lines older than 14 days, then, while the file is over its cap, the oldest
    /// tenth, then gives the free space back.
    pub fn retain(&self, now: i64) -> Result<Removed, String> {
        self.retain_with(now, self.settings.max_mb.saturating_mul(1024 * 1024))
    }

    /// `retain` with the cap in bytes.
    pub fn retain_with(&self, now: i64, max_bytes: u64) -> Result<Removed, String> {
        let mut removed = Removed::default();
        let cutoff = now - KEEP_DAYS * 24 * 3600 * 1000;
        removed.old = self.delete_where("ts < ?", &[Sql::Integer(cutoff)])?;
        self.shrink()?;
        // Each round removes a tenth, so this ends; the limit is only a safety net.
        for _ in 0..200 {
            let (size, count) = {
                let conn = self.lock();
                (file_size(&conn)?, count_lines(&conn)?)
            };
            if size <= max_bytes || count == 0 {
                break;
            }
            let tenth = (count / 10).max(1);
            let edge: Option<i64> = self
                .lock()
                .query_row(
                    "SELECT MAX(id) FROM (SELECT id FROM lines ORDER BY id LIMIT ?)",
                    [tenth],
                    |r| r.get(0),
                )
                .map_err(db_error)?;
            let Some(edge) = edge else { break };
            removed.for_size += self.delete_where("id <= ?", &[Sql::Integer(edge)])?;
            self.shrink()?;
        }
        // Best effort: it only trims the write-ahead file.
        let _ = self
            .lock()
            .query_row("PRAGMA wal_checkpoint(TRUNCATE)", [], |_| Ok(()));
        Ok(removed)
    }

    /// Deletes the matching lines in steps, letting others in between.
    fn delete_where(&self, condition: &str, args: &[Sql]) -> Result<usize, String> {
        let sql = format!(
            "DELETE FROM lines WHERE id IN (SELECT id FROM lines WHERE {condition} LIMIT {DELETE_STEP})"
        );
        let mut total = 0;
        loop {
            let n = self
                .lock()
                .execute(&sql, params_from_iter(args.iter()))
                .map_err(db_error)?;
            total += n;
            if (n as i64) < DELETE_STEP {
                return Ok(total);
            }
        }
    }

    /// Gives free pages back to the disk.
    fn shrink(&self) -> Result<(), String> {
        loop {
            let conn = self.lock();
            let free: i64 = conn
                .query_row("PRAGMA freelist_count", [], |r| r.get(0))
                .map_err(db_error)?;
            if free == 0 {
                return Ok(());
            }
            conn.execute_batch("PRAGMA incremental_vacuum(1000)")
                .map_err(db_error)?;
        }
    }

    /// The path of the database file.
    pub fn path(&self) -> &Path {
        &self.path
    }
}

/// Runs `retain` now and then every hour.
pub fn spawn_retention(logs: Arc<Logs>) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(3600));
        loop {
            tick.tick().await;
            let logs = logs.clone();
            let result = tokio::task::spawn_blocking(move || logs.retain(now_ms())).await;
            match result {
                Ok(Ok(r)) if r == Removed::default() => {}
                Ok(Ok(r)) => {
                    tracing::info!(old = r.old, for_size = r.for_size, "removed old log lines")
                }
                Ok(Err(e)) => tracing::warn!("log clean-up failed: {e}"),
                Err(_) => tracing::warn!("log clean-up stopped unexpectedly"),
            }
        }
    });
}

// ---- helpers ----

/// A WHERE clause being built.
#[derive(Clone, Default)]
struct Filter {
    parts: Vec<String>,
    args: Vec<Sql>,
}

impl Filter {
    fn add(&mut self, part: String, args: impl IntoIterator<Item = Sql>) {
        self.parts.push(part);
        self.args.extend(args);
    }

    fn sql(&self) -> String {
        if self.parts.is_empty() {
            String::new()
        } else {
            format!("WHERE {}", self.parts.join(" AND "))
        }
    }
}

fn text(s: &str) -> Sql {
    Sql::Text(s.to_string())
}

/// The filters shared by the lines and the counts: who may see, the words, app, env, service
/// and the time range. An `app` or `env` also finds Homelab's lines about it.
fn base_filter(scope: &Scope, q: &Query) -> Filter {
    let mut f = Filter::default();
    if let Scope::App { app, env } = scope {
        f.add(
            "((app = ? AND env = ?) OR (about_app = ? AND about_env = ?))".to_string(),
            [text(app), text(env), text(app), text(env)],
        );
    }
    if let Some(m) = fts_query(&q.q) {
        f.add(
            "id IN (SELECT rowid FROM lines_fts WHERE lines_fts MATCH ?)".to_string(),
            [Sql::Text(m)],
        );
    }
    match (q.app.is_empty(), q.env.is_empty()) {
        (false, false) => f.add(
            "((app = ? AND env = ?) OR (about_app = ? AND about_env = ?))".to_string(),
            [text(&q.app), text(&q.env), text(&q.app), text(&q.env)],
        ),
        (false, true) => f.add(
            "(app = ? OR about_app = ?)".to_string(),
            [text(&q.app), text(&q.app)],
        ),
        (true, false) => f.add(
            "(env = ? OR about_env = ?)".to_string(),
            [text(&q.env), text(&q.env)],
        ),
        (true, true) => {}
    }
    if !q.service.is_empty() {
        f.add("service = ?".to_string(), [text(&q.service)]);
    }
    if let Some(t) = q.since {
        f.add("ts >= ?".to_string(), [Sql::Integer(t)]);
    }
    if let Some(t) = q.until {
        f.add("ts <= ?".to_string(), [Sql::Integer(t)]);
    }
    f
}

/// Turns what a person typed into an FTS5 query that can't be anything but words: the first
/// 200 characters, split on spaces, at most 10 words, each in double quotes (a quote inside
/// is doubled), all of them required. None when there is nothing to search for.
pub fn fts_query(q: &str) -> Option<String> {
    let q: String = q.chars().take(MAX_QUERY_CHARS).collect();
    let words: Vec<String> = q
        .split_whitespace()
        .take(MAX_WORDS)
        .map(|w| format!("\"{}\"", w.replace('"', "\"\"")))
        .collect();
    if words.is_empty() {
        None
    } else {
        Some(words.join(" "))
    }
}

fn db_error(e: rusqlite::Error) -> String {
    tracing::error!("logs database error: {e}");
    "The logs database had a problem.".to_string()
}

fn file_size(conn: &Connection) -> Result<u64, String> {
    let pages: i64 = conn
        .query_row("PRAGMA page_count", [], |r| r.get(0))
        .map_err(db_error)?;
    let size: i64 = conn
        .query_row("PRAGMA page_size", [], |r| r.get(0))
        .map_err(db_error)?;
    Ok((pages * size).max(0) as u64)
}

fn count_lines(conn: &Connection) -> Result<i64, String> {
    conn.query_row("SELECT COUNT(*) FROM lines", [], |r| r.get(0))
        .map_err(db_error)
}

/// The current time in unix milliseconds.
pub fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// Unix milliseconds as `2026-10-23T22:00:00.123Z`.
pub fn iso(ms: i64) -> String {
    DateTime::from_timestamp_millis(ms)
        .map(|d| d.to_rfc3339_opts(SecondsFormat::Millis, true))
        .unwrap_or_default()
}

/// Who did something, for an event: `person:p1`, `key:k1`, `app:notes/dev` or `system`.
pub fn who(caller: &Caller) -> String {
    match caller.kind {
        Kind::Person => format!("person:{}", caller.person),
        Kind::Admin => format!("key:{}", caller.key),
        Kind::App => format!("app:{}/{}", caller.app, caller.env),
        Kind::System => "system".to_string(),
    }
}

/// The app environment an event is about: the caller's own when it is an app, else the
/// owner given (the app or environment of a folder or a database, maybe empty).
pub fn about(caller: &Caller, app: &str, env: &str) -> (String, String) {
    if caller.kind == Kind::App {
        (caller.app.clone(), caller.env.clone())
    } else {
        (app.to_string(), env.to_string())
    }
}
