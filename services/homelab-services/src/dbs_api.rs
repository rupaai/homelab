//! The databases routes.
//!
//! * `/api/db...` is for people, admin keys (scope `db:read`) and app keys (their own
//!   database only; any other is 404). Writing is for people, on Postgres only.
//! * `/internal/db...` is for homelabd only (the `system` caller): it tells us which
//!   databases exist and asks for SQLite copies.
//!
//! SQL text and values are never logged.

use std::collections::HashMap;
use std::path::PathBuf;
use std::time::Instant;

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::{delete, get, post, put};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::api::{err, valid_place, ApiError, Reply};
use crate::app::{not_found, AppState};
use crate::caller::{Caller, Kind};
use crate::dbs::{Change, Engine, Error, Info, Output, Reg, MAX_SQL};
use crate::dbs_sql;

/// The scope an admin key needs.
pub const SCOPE_READ: &str = "db:read";

const INTERNAL_PREFIX: &str = "/internal/db";

/// Whether a path is under /internal/db.
fn is_internal_path(path: &str) -> bool {
    path == INTERNAL_PREFIX || path.starts_with("/internal/db/")
}

/// Refuses anyone but homelabd on /internal/db, whether or not a route is there.
pub fn refuse_path(caller: &Caller, path: &str) -> Option<ApiError> {
    if is_internal_path(path) && caller.kind != Kind::System {
        return Some(forbidden(
            caller,
            "Only Homelab itself can use these routes.",
        ));
    }
    None
}

pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/db", get(list).fallback(not_found))
        .route("/api/db/{app}/{env}", get(one).fallback(not_found))
        .route(
            "/api/db/{app}/{env}/tables",
            get(tables).fallback(not_found),
        )
        .route(
            "/api/db/{app}/{env}/tables/{table}/rows",
            get(table_rows).fallback(not_found),
        )
        .route("/api/db/{app}/{env}/query", post(query).fallback(not_found))
        .route(
            "/api/db/{app}/{env}/changes",
            get(changes).fallback(not_found),
        )
        .route(
            "/internal/db/{app}/{env}",
            put(sys_register).fallback(not_found),
        )
        .route("/internal/db/{app}", delete(sys_forget).fallback(not_found))
        .route(
            "/internal/db/{app}/{env}/snapshot",
            post(sys_snapshot).fallback(not_found),
        )
}

// ---- errors ----

impl ApiError {
    /// The message a database error shows.
    fn from_ref(e: &Error) -> String {
        match e {
            Error::Invalid(m) | Error::Missing(m) | Error::Conflict(m) | Error::Down(m) => {
                m.clone()
            }
            Error::Internal => "Something went wrong.".to_string(),
        }
    }
}

impl From<Error> for ApiError {
    fn from(e: Error) -> ApiError {
        match e {
            Error::Invalid(m) => err(StatusCode::BAD_REQUEST, m),
            Error::Missing(m) => err(StatusCode::NOT_FOUND, m),
            Error::Conflict(m) => err(StatusCode::CONFLICT, m),
            Error::Down(m) => err(StatusCode::SERVICE_UNAVAILABLE, m),
            Error::Internal => err(StatusCode::INTERNAL_SERVER_ERROR, "Something went wrong."),
        }
    }
}

fn forbidden(caller: &Caller, why: &str) -> ApiError {
    crate::api::forbidden("databases", caller, why)
}

fn no_such_db() -> ApiError {
    err(StatusCode::NOT_FOUND, "There's no such database.")
}

// ---- access ----

/// Who may look at databases at all.
fn viewer(caller: &Caller) -> Result<(), ApiError> {
    match caller.kind {
        Kind::Person => Ok(()),
        Kind::Admin if caller.scopes.iter().any(|s| s == SCOPE_READ) => Ok(()),
        Kind::Admin => Err(forbidden(
            caller,
            &format!("This key can't do that. It needs the {SCOPE_READ} scope."),
        )),
        Kind::App => Ok(()),
        Kind::System => Err(forbidden(
            caller,
            "These routes are for people and keys. Homelab itself uses /internal/db.",
        )),
    }
}

/// The registered database this caller may use, or 404.
async fn find(s: &AppState, caller: &Caller, app: &str, env: &str) -> Result<Reg, ApiError> {
    viewer(caller)?;
    if caller.kind == Kind::App && (caller.app != app || caller.env != env) {
        return Err(no_such_db());
    }
    s.dbs.get(app, env).await?.ok_or_else(no_such_db)
}

fn system_caller(caller: &Caller) -> Result<(), ApiError> {
    if caller.kind != Kind::System {
        return Err(forbidden(
            caller,
            "Only Homelab itself can use these routes.",
        ));
    }
    Ok(())
}

// ---- for people and keys ----

fn info_json(reg: &Reg, info: &Info) -> Value {
    json!({
        "app": reg.app,
        "env": reg.env,
        "engine": reg.engine.name(),
        "status": info.status,
        "size_bytes": info.size_bytes,
        "connections": info.connections,
        "tables": info.tables,
    })
}

/// GET /api/db
async fn list(State(s): State<AppState>, caller: Caller) -> Reply {
    viewer(&caller)?;
    let mut regs = s.dbs.list().await?;
    if caller.kind == Kind::App {
        regs.retain(|r| r.app == caller.app && r.env == caller.env);
    }
    // All at once, so one database that is down only costs its own wait.
    let infos =
        futures_util::future::join_all(regs.iter().map(|r| s.dbs.info(&s.secrets, r))).await;
    let databases: Vec<Value> = regs
        .iter()
        .zip(&infos)
        .map(|(r, i)| info_json(r, i))
        .collect();
    Ok(Json(json!({ "databases": databases })).into_response())
}

/// GET /api/db/{app}/{env}
async fn one(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env)): Path<(String, String)>,
) -> Reply {
    let reg = find(&s, &caller, &app, &env).await?;
    let info = s.dbs.info(&s.secrets, &reg).await;
    Ok(Json(info_json(&reg, &info)).into_response())
}

/// GET /api/db/{app}/{env}/tables
async fn tables(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env)): Path<(String, String)>,
) -> Reply {
    let reg = find(&s, &caller, &app, &env).await?;
    let tables = s.dbs.tables(&s.secrets, &reg).await?;
    let tables: Vec<Value> = tables
        .iter()
        .map(|t| json!({ "schema": t.schema, "name": t.name, "rows": t.rows }))
        .collect();
    Ok(Json(json!({ "tables": tables })).into_response())
}

/// GET /api/db/{app}/{env}/tables/{table}/rows?schema=public&limit=50&offset=0
async fn table_rows(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env, table)): Path<(String, String, String)>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    let reg = find(&s, &caller, &app, &env).await?;
    let limit = match q.get("limit") {
        None => 50,
        Some(v) => match v.parse::<usize>() {
            Ok(n) if (1..=200).contains(&n) => n,
            _ => {
                return Err(err(
                    StatusCode::BAD_REQUEST,
                    "The limit is a number from 1 to 200.",
                ))
            }
        },
    };
    let offset = match q.get("offset") {
        None => 0,
        Some(v) => v
            .parse::<u64>()
            .map_err(|_| err(StatusCode::BAD_REQUEST, "The offset is a number from 0."))?,
    };
    let schema = q.get("schema").map(String::as_str).unwrap_or("public");
    let started = Instant::now();
    let result = s
        .dbs
        .table_rows(&s.secrets, &reg, schema, &table, limit, offset)
        .await;
    log_use(&caller, &reg, started, false, "browse");
    let out = result?;
    s.logs.event(
        "databases",
        crate::logs::Level::Debug,
        &format!("table rows read in {}/{}", reg.app, reg.env),
        json!({
            "table": table,
            "rows": out.rows.len(),
            "ms": started.elapsed().as_millis() as u64,
            "by": crate::logs::who(&caller),
        }),
        &reg.app,
        &reg.env,
    );
    Ok(Json(json!({
        "columns": out.columns,
        "rows": out.rows,
        "truncated": out.truncated,
    }))
    .into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct QueryBody {
    sql: String,
    #[serde(default)]
    write: bool,
    /// The env's name, typed by the person, for risky statements.
    #[serde(default)]
    confirm: String,
}

/// The longest error we keep in a change record.
const MAX_ERROR: usize = 300;

/// The longest copy name we accept.
const MAX_COPY: usize = 100;

/// POST /api/db/{app}/{env}/query. In write mode, `?copy=<name>` says which copy homelabd
/// made just before; it is only written down in the changes.
async fn query(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env)): Path<(String, String)>,
    Query(params): Query<HashMap<String, String>>,
    body: Result<Json<QueryBody>, axum::extract::rejection::JsonRejection>,
) -> Reply {
    let reg = find(&s, &caller, &app, &env).await?;
    let Json(body) =
        body.map_err(|_| err(StatusCode::BAD_REQUEST, "That isn't a valid request body."))?;
    if body.write {
        if caller.kind != Kind::Person {
            return Err(forbidden(&caller, "Only a signed-in person can write."));
        }
        if !matches!(reg.engine, Engine::Postgres(_)) {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "Writing is only for Postgres databases.",
            ));
        }
    }
    if body.write {
        if body.sql.trim().is_empty() {
            return Err(err(StatusCode::BAD_REQUEST, "Write some SQL first."));
        }
        if body.sql.len() > MAX_SQL {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "That SQL is too long (100 KB at most).",
            ));
        }
        if dbs_sql::has_transaction_control(&body.sql) {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "Leave out BEGIN and COMMIT; Homelab runs it all in one go.",
            ));
        }
        if dbs_sql::is_risky(&body.sql) && body.confirm.trim() != reg.env {
            return Ok((
                StatusCode::CONFLICT,
                Json(json!({
                    "error": format!("This can't be undone easily. Type {} to run it.", reg.env),
                    "needs_confirm": true,
                })),
            )
                .into_response());
        }
    }
    let started = Instant::now();
    let result = s.dbs.query(&s.secrets, &reg, &body.sql, body.write).await;
    log_use(&caller, &reg, started, body.write, "query");
    if body.write {
        let (ok, rows, error) = match &result {
            Ok(out) => (true, row_count(out), String::new()),
            Err(e) => (false, 0, ApiError::from_ref(e)),
        };
        let copy = params
            .get("copy")
            .filter(|c| valid_copy(c))
            .cloned()
            .unwrap_or_default();
        let change = Change {
            id: 0,
            app: reg.app.clone(),
            env: reg.env.clone(),
            person: caller.person.clone(),
            at: crate::app::unix_now(),
            sql: body.sql.clone(),
            ok,
            rows: rows as i64,
            ms: started.elapsed().as_millis() as i64,
            error: error.chars().take(MAX_ERROR).collect(),
            copy,
        };
        // The write already happened; if we can't write it down, say so in the log only.
        if s.dbs.record_change(change).await.is_err() {
            tracing::error!(app = %reg.app, env = %reg.env, "cannot record a database change");
        }
    }
    let out: Output = result?;
    s.logs.event(
        "databases",
        crate::logs::Level::Info,
        &format!("query run on {}/{}", reg.app, reg.env),
        json!({
            "ms": started.elapsed().as_millis() as u64,
            "rows": row_count(&out),
            "write": body.write,
            "by": crate::logs::who(&caller),
        }),
        &reg.app,
        &reg.env,
    );
    Ok(Json(json!({
        "columns": out.columns,
        "rows": out.rows,
        "row_count": row_count(&out),
        "truncated": out.truncated,
        "command": out.command,
        "ms": started.elapsed().as_millis() as u64,
    }))
    .into_response())
}

fn row_count(out: &Output) -> usize {
    if out.columns.is_empty() {
        out.affected as usize
    } else {
        out.rows.len()
    }
}

/// A copy name is letters, digits, dots, dashes and underscores.
fn valid_copy(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= MAX_COPY
        && name
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'.' | b'-' | b'_'))
}

#[derive(Deserialize)]
struct ChangesQuery {
    limit: Option<usize>,
    before: Option<i64>,
}

/// GET /api/db/{app}/{env}/changes. People only: the SQL may hold private values.
async fn changes(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env)): Path<(String, String)>,
    Query(q): Query<ChangesQuery>,
) -> Reply {
    if caller.kind != Kind::Person {
        return Err(forbidden(
            &caller,
            "Only a signed-in person can see changes.",
        ));
    }
    let reg = find(&s, &caller, &app, &env).await?;
    let limit = q.limit.unwrap_or(50);
    if !(1..=200).contains(&limit) {
        return Err(err(StatusCode::BAD_REQUEST, "limit must be 1 to 200."));
    }
    let found = s.dbs.changes(&reg.app, &reg.env, limit, q.before).await?;
    let changes: Vec<Value> = found
        .into_iter()
        .map(|c| {
            json!({
                "id": c.id,
                "person": c.person,
                "at": c.at,
                "sql": c.sql,
                "ok": c.ok,
                "rows": c.rows,
                "ms": c.ms,
                "error": c.error,
                "copy": if c.copy.is_empty() { Value::Null } else { c.copy.into() },
            })
        })
        .collect();
    Ok(Json(json!({ "changes": changes })).into_response())
}

/// One log line per use. Never the SQL or any value.
fn log_use(caller: &Caller, reg: &Reg, started: Instant, write: bool, what: &str) {
    tracing::info!(
        app = %reg.app,
        env = %reg.env,
        kind = ?caller.kind,
        ms = started.elapsed().as_millis() as u64,
        write,
        "database {what}"
    );
}

// ---- for homelabd ----

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RegisterBody {
    engine: String,
    port: Option<u16>,
    path: Option<String>,
}

/// PUT /internal/db/{app}/{env}
async fn sys_register(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env)): Path<(String, String)>,
    body: Result<Json<RegisterBody>, axum::extract::rejection::JsonRejection>,
) -> Reply {
    system_caller(&caller)?;
    if !valid_place(&app, &env) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "That isn't a valid app or environment.",
        ));
    }
    let Json(body) =
        body.map_err(|_| err(StatusCode::BAD_REQUEST, "That isn't a valid request body."))?;
    let engine = match (body.engine.as_str(), body.port, body.path) {
        ("postgres", Some(port), None) if port > 0 => Engine::Postgres(port),
        ("sqlite", None, Some(path)) if PathBuf::from(&path).is_absolute() => {
            Engine::Sqlite(PathBuf::from(path))
        }
        ("postgres", _, _) => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "A Postgres database needs a port.",
            ))
        }
        ("sqlite", _, _) => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "A SQLite database needs an absolute path.",
            ))
        }
        _ => {
            return Err(err(
                StatusCode::BAD_REQUEST,
                "The engine is postgres or sqlite.",
            ))
        }
    };
    s.dbs.register(Reg { app, env, engine }).await?;
    Ok(StatusCode::NO_CONTENT.into_response())
}

/// DELETE /internal/db/{app}
async fn sys_forget(State(s): State<AppState>, caller: Caller, Path(app): Path<String>) -> Reply {
    system_caller(&caller)?;
    s.dbs.forget_app(&app).await?;
    Ok(StatusCode::NO_CONTENT.into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SnapshotBody {
    to: String,
}

/// POST /internal/db/{app}/{env}/snapshot
async fn sys_snapshot(
    State(s): State<AppState>,
    caller: Caller,
    Path((app, env)): Path<(String, String)>,
    body: Result<Json<SnapshotBody>, axum::extract::rejection::JsonRejection>,
) -> Reply {
    system_caller(&caller)?;
    let Json(body) =
        body.map_err(|_| err(StatusCode::BAD_REQUEST, "That isn't a valid request body."))?;
    let reg = s
        .dbs
        .get(&app, &env)
        .await?
        .ok_or_else(|| err(StatusCode::NOT_FOUND, "That database isn't registered."))?;
    let started = Instant::now();
    let result = s.dbs.snapshot(&reg, std::path::Path::new(&body.to)).await;
    log_use(&caller, &reg, started, false, "snapshot");
    result?;
    Ok(StatusCode::NO_CONTENT.into_response())
}

#[cfg(test)]
#[path = "dbs_api_tests.rs"]
mod tests;
