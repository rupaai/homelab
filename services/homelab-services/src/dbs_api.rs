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
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, post, put};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::app::{error_response, not_found, AppState};
use crate::caller::{Caller, Kind};
use crate::dbs::{Change, Engine, Error, Info, Output, Reg, MAX_SQL};
use crate::dbs_sql;
use crate::disk::valid_name;

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

pub struct ApiError(StatusCode, String);

fn err(status: StatusCode, message: impl Into<String>) -> ApiError {
    ApiError(status, message.into())
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        error_response(self.0, &self.1)
    }
}

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

type Reply = Result<Response, ApiError>;

fn forbidden(caller: &Caller, why: &str) -> ApiError {
    tracing::warn!(kind = ?caller.kind, app = %caller.app, env = %caller.env, "databases request refused");
    err(StatusCode::FORBIDDEN, why)
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

fn valid_place(app: &str, env: &str) -> bool {
    valid_name(app, 60) && (env == "dev" || env == "prod")
}

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
mod tests {
    use super::*;
    use crate::dbs::Dbs;
    use crate::secrets::Kind as SecretKind;
    use crate::testutil::*;
    use axum::body::Body;
    use axum::http::Request;
    use http_body_util::BodyExt;
    use std::sync::Arc;
    use std::time::Duration;
    use tower::ServiceExt;

    async fn send(
        env: &TestEnv,
        who: &Value,
        method: &str,
        target: &str,
        body: Value,
    ) -> (StatusCode, Value) {
        let bytes = if body.is_null() {
            Vec::new()
        } else {
            serde_json::to_vec(&body).unwrap()
        };
        let extra: &[(&str, &str)] = if body.is_null() {
            &[]
        } else {
            &[("content-type", "application/json")]
        };
        let req: Request<Body> = request(who, method, target, bytes, extra);
        let response = env.router.clone().oneshot(req).await.unwrap();
        let (parts, body) = response.into_parts();
        let bytes = body.collect().await.unwrap().to_bytes();
        (
            parts.status,
            serde_json::from_slice(&bytes).unwrap_or(Value::Null),
        )
    }

    /// Makes a SQLite file with a table of two rows.
    fn sqlite_file(env: &TestEnv, name: &str) -> PathBuf {
        let path = env.dir.path().join(name);
        let conn = rusqlite::Connection::open(&path).unwrap();
        conn.execute_batch(
            "CREATE TABLE notes(id INTEGER PRIMARY KEY, title TEXT, score REAL, data BLOB);
             INSERT INTO notes VALUES(1, 'Dune', 1.5, x'00ff'), (2, NULL, NULL, NULL);",
        )
        .unwrap();
        path
    }

    async fn register_sqlite(env: &TestEnv, app: &str, e: &str, path: &PathBuf) {
        let (status, _) = send(
            env,
            &system(),
            "PUT",
            &format!("/internal/db/{app}/{e}"),
            json!({ "engine": "sqlite", "path": path }),
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
    }

    // ---- who may call what ----

    #[tokio::test]
    async fn internal_routes_are_for_the_system_only() {
        let env = test_env(1024);
        let routes = [
            ("PUT", "/internal/db/notes/dev"),
            ("DELETE", "/internal/db/notes"),
            ("POST", "/internal/db/notes/dev/snapshot"),
            ("GET", "/internal/db"),
            ("GET", "/internal/db/anything/else"),
        ];
        let body = json!({ "engine": "postgres", "port": 5432, "to": "/tmp/x" });
        for who in [
            person(),
            admin(&["db:read"]),
            app_key("notes", "dev"),
            app_scoped("notes", "dev", &["db:read"]),
        ] {
            for (method, target) in routes {
                let (status, _) = send(&env, &who, method, target, body.clone()).await;
                assert_eq!(status, StatusCode::FORBIDDEN, "{method} {target} {who}");
            }
        }
        assert_eq!(env.dbs.list().await.unwrap(), vec![]);
        // The system itself gets through.
        let (status, _) = send(
            &env,
            &system(),
            "PUT",
            "/internal/db/notes/dev",
            json!({ "engine": "postgres", "port": 5432 }),
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
    }

    #[tokio::test]
    async fn forged_callers_are_refused() {
        let env = test_env(1024);
        let mut caller = person();
        caller["method"] = "GET".into();
        caller["path"] = "/api/db".into();
        caller["ts"] = crate::app::unix_now().into();
        let headers = signed_headers(&other_key(), &caller);
        let mut req = Request::builder().method("GET").uri("/api/db");
        for (n, v) in headers.iter() {
            req = req.header(n, v);
        }
        let response = env
            .router
            .clone()
            .oneshot(req.body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn register_validates_the_body() {
        let env = test_env(1024);
        let bad = [
            ("notes", "test", json!({ "engine": "postgres", "port": 1 })),
            ("Notes", "dev", json!({ "engine": "postgres", "port": 1 })),
            ("notes", "dev", json!({ "engine": "mysql" })),
            ("notes", "dev", json!({ "engine": "postgres" })),
            ("notes", "dev", json!({ "engine": "postgres", "port": 0 })),
            (
                "notes",
                "dev",
                json!({ "engine": "sqlite", "path": "rel.db" }),
            ),
            ("notes", "dev", json!({ "engine": "sqlite" })),
            (
                "notes",
                "dev",
                json!({ "engine": "sqlite", "path": "/a", "port": 1 }),
            ),
            (
                "notes",
                "dev",
                json!({ "engine": "sqlite", "path": "/a", "x": 1 }),
            ),
        ];
        for (app, e, body) in bad {
            let (status, _) = send(
                &env,
                &system(),
                "PUT",
                &format!("/internal/db/{app}/{e}"),
                body.clone(),
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{app} {e} {body}");
        }
    }

    #[tokio::test]
    async fn access_rules() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        register_sqlite(&env, "notes", "dev", &file).await;
        register_sqlite(&env, "notes", "prod", &file).await;
        register_sqlite(&env, "blog", "dev", &file).await;
        let one = "/api/db/notes/dev";

        // People see everything.
        let (status, body) = send(&env, &person(), "GET", "/api/db", Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["databases"].as_array().unwrap().len(), 3);
        assert_eq!(body["databases"][0]["app"], "blog");
        assert_eq!(body["databases"][0]["engine"], "sqlite");
        assert_eq!(body["databases"][0]["status"], "ok");
        assert_eq!(body["databases"][0]["tables"], 1);
        assert_eq!(body["databases"][0]["connections"], Value::Null);
        assert!(body["databases"][0]["size_bytes"].as_i64().unwrap() > 0);
        let (status, _) = send(&env, &person(), "GET", one, Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        let (status, _) = send(&env, &person(), "GET", "/api/db/nope/dev", Value::Null).await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        // An admin key needs db:read.
        for target in ["/api/db", one, "/api/db/notes/dev/tables"] {
            let (status, _) = send(&env, &admin(&[]), "GET", target, Value::Null).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{target}");
            let (status, _) = send(&env, &admin(&["files:read"]), "GET", target, Value::Null).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{target}");
            let (status, _) = send(&env, &admin(&["db:read"]), "GET", target, Value::Null).await;
            assert_eq!(status, StatusCode::OK, "{target}");
        }
        let (status, _) = send(
            &env,
            &admin(&[]),
            "POST",
            "/api/db/notes/dev/query",
            json!({"sql": "select 1"}),
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);

        // An app key sees only its own app and environment, with no scope.
        let me = app_key("notes", "dev");
        let (status, body) = send(&env, &me, "GET", "/api/db", Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["databases"].as_array().unwrap().len(), 1);
        assert_eq!(body["databases"][0]["env"], "dev");
        let (status, _) = send(&env, &me, "GET", one, Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        for target in [
            "/api/db/notes/prod",
            "/api/db/blog/dev",
            "/api/db/blog/dev/tables",
            "/api/db/notes/prod/tables/notes/rows",
            "/api/db/nope/dev",
        ] {
            let (status, _) = send(&env, &me, "GET", target, Value::Null).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{target}");
        }
        let (status, _) = send(
            &env,
            &me,
            "POST",
            "/api/db/blog/dev/query",
            json!({"sql": "select 1"}),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);

        // Writes are for people only.
        let write = json!({"sql": "insert into notes(id) values (9)", "write": true});
        let (status, _) = send(&env, &me, "POST", "/api/db/notes/dev/query", write.clone()).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        let (status, _) = send(
            &env,
            &admin(&["db:read"]),
            "POST",
            "/api/db/notes/dev/query",
            write.clone(),
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        // The system is not a viewer.
        let (status, _) = send(&env, &system(), "GET", "/api/db", Value::Null).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
    }

    // ---- registry ----

    #[tokio::test]
    async fn registry_survives_a_restart() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("dbs.db");
        let dbs = Dbs::open(&path).unwrap();
        let pg = Reg {
            app: "notes".into(),
            env: "dev".into(),
            engine: Engine::Postgres(17005),
        };
        let lite = Reg {
            app: "notes".into(),
            env: "prod".into(),
            engine: Engine::Sqlite("/x/app.db".into()),
        };
        dbs.register(pg.clone()).await.unwrap();
        dbs.register(lite.clone()).await.unwrap();
        drop(dbs);
        let dbs = Dbs::open(&path).unwrap();
        assert_eq!(dbs.list().await.unwrap(), vec![pg.clone(), lite.clone()]);
        // Registering again replaces; deleting an app forgets all its envs.
        let moved = Reg {
            engine: Engine::Postgres(17006),
            ..pg
        };
        dbs.register(moved.clone()).await.unwrap();
        assert_eq!(dbs.get("notes", "dev").await.unwrap(), Some(moved));
        dbs.register(Reg {
            app: "blog".into(),
            env: "dev".into(),
            engine: Engine::Postgres(1),
        })
        .await
        .unwrap();
        assert_eq!(dbs.forget_app("notes").await.unwrap(), 2);
        assert_eq!(dbs.list().await.unwrap().len(), 1);
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(&path).unwrap().permissions().mode() & 0o077,
            0
        );
    }

    #[tokio::test]
    async fn delete_route_forgets_the_app() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        register_sqlite(&env, "notes", "dev", &file).await;
        register_sqlite(&env, "notes", "prod", &file).await;
        let (status, _) = send(&env, &system(), "DELETE", "/internal/db/notes", Value::Null).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let (status, _) = send(&env, &person(), "GET", "/api/db/notes/dev", Value::Null).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    // ---- sqlite ----

    #[tokio::test]
    async fn sqlite_browse_and_query() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        register_sqlite(&env, "notes", "dev", &file).await;
        register_sqlite(&env, "notes", "prod", &env.dir.path().join("none.db")).await;
        let p = person();

        let (_, body) = send(&env, &p, "GET", "/api/db/notes/prod", Value::Null).await;
        assert_eq!(body["status"], "empty");
        let (status, body) = send(&env, &p, "GET", "/api/db/notes/prod/tables", Value::Null).await;
        assert_eq!((status, body), (StatusCode::OK, json!({ "tables": [] })));
        let (status, _) = send(
            &env,
            &p,
            "POST",
            "/api/db/notes/prod/query",
            json!({"sql": "select 1"}),
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT);

        let (_, body) = send(&env, &p, "GET", "/api/db/notes/dev/tables", Value::Null).await;
        assert_eq!(
            body,
            json!({ "tables": [{"schema": "main", "name": "notes", "rows": 2}] })
        );
        let (_, body) = send(
            &env,
            &p,
            "GET",
            "/api/db/notes/dev/tables/notes/rows?limit=1",
            Value::Null,
        )
        .await;
        assert_eq!(body["columns"], json!(["id", "title", "score", "data"]));
        assert_eq!(
            body["rows"],
            json!([["1", "Dune", "1.5", "<blob 2 bytes>"]])
        );
        assert_eq!(body["truncated"], true);
        let (_, body) = send(
            &env,
            &p,
            "GET",
            "/api/db/notes/dev/tables/notes/rows?offset=1",
            Value::Null,
        )
        .await;
        assert_eq!(body["rows"], json!([["2", null, null, null]]));
        assert_eq!(body["truncated"], false);
        for bad in ["limit=0", "limit=201", "limit=x", "offset=-1"] {
            let target = format!("/api/db/notes/dev/tables/notes/rows?{bad}");
            let (status, _) = send(&env, &p, "GET", &target, Value::Null).await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{bad}");
        }
        // Only listed tables, never SQL from the path.
        for t in [
            "nope",
            "sqlite_master",
            "notes%22%3B%20drop%20table%20notes%3B%20--",
        ] {
            let target = format!("/api/db/notes/dev/tables/{t}/rows");
            let (status, _) = send(&env, &p, "GET", &target, Value::Null).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{t}");
        }

        let (status, body) = send(
            &env,
            &p,
            "POST",
            "/api/db/notes/dev/query",
            json!({"sql": "select id, title from notes order by id; "}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["rows"], json!([["1", "Dune"], ["2", null]]));
        assert_eq!(body["row_count"], 2);
        assert_eq!(body["command"], "SELECT 2");
        assert_eq!(body["truncated"], false);
        assert!(body["ms"].is_u64());
    }

    #[tokio::test]
    async fn sqlite_is_read_only() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        let other = env.dir.path().join("other.db");
        register_sqlite(&env, "notes", "dev", &file).await;
        let attach = format!("attach database '{}' as o", other.display());
        let vacuum = format!("vacuum into '{}'", env.dir.path().join("copy.db").display());
        let cases = [
            ("insert into notes(id) values (3)", "readonly"),
            ("delete from notes", "readonly"),
            ("drop table notes", "readonly"),
            ("create table t(x)", "readonly"),
            (
                "pragma query_only = off; insert into notes(id) values (3)",
                "one statement",
            ),
            ("select 1; select 2", "one statement"),
            ("select 1; delete from notes", "one statement"),
            (attach.as_str(), "attach"),
            (vacuum.as_str(), ""),
            ("select load_extension('x')", ""),
        ];
        for (sql, expect) in cases {
            let (status, body) = send(
                &env,
                &person(),
                "POST",
                "/api/db/notes/dev/query",
                json!({ "sql": sql }),
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{sql}: {body}");
            let message = body["error"].as_str().unwrap().to_lowercase();
            assert!(message.contains(expect), "{sql}: {message}");
        }
        assert!(!other.exists());
        assert!(!env.dir.path().join("copy.db").exists());
        // Nothing changed.
        let (_, body) = send(
            &env,
            &person(),
            "GET",
            "/api/db/notes/dev/tables",
            Value::Null,
        )
        .await;
        assert_eq!(body["tables"][0]["rows"], 2);
        // Writing is for Postgres.
        let (status, body) = send(
            &env,
            &person(),
            "POST",
            "/api/db/notes/dev/query",
            json!({"sql": "delete from notes", "write": true}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(body["error"], "Writing is only for Postgres databases.");
        // Bad bodies and sizes.
        let (status, _) = send(
            &env,
            &person(),
            "POST",
            "/api/db/notes/dev/query",
            json!({"nope": 1}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let (status, _) = send(
            &env,
            &person(),
            "POST",
            "/api/db/notes/dev/query",
            json!({"sql": "  "}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let big = format!("select '{}'", "x".repeat(101 * 1024));
        let (status, _) = send(
            &env,
            &person(),
            "POST",
            "/api/db/notes/dev/query",
            json!({"sql": big}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn sqlite_caps_rows_and_stops_slow_queries() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        register_sqlite(&env, "notes", "dev", &file).await;
        let many = "with recursive n(i) as (select 1 union all select i + 1 from n where i < 600) select i from n";
        let (_, body) = send(
            &env,
            &person(),
            "POST",
            "/api/db/notes/dev/query",
            json!({ "sql": many }),
        )
        .await;
        assert_eq!(body["rows"].as_array().unwrap().len(), 500);
        assert_eq!(body["row_count"], 500);
        assert_eq!(body["truncated"], true);

        // A short limit, called directly.
        let dbs =
            Dbs::open_with_limit(&env.dir.path().join("short.db"), Duration::from_millis(300))
                .unwrap();
        let reg = Reg {
            app: "notes".into(),
            env: "dev".into(),
            engine: Engine::Sqlite(file),
        };
        let forever = "with recursive n(i) as (select 1 union all select i + 1 from n) select count(*) from n";
        let started = std::time::Instant::now();
        let result = dbs.query(&None, &reg, forever, false).await;
        assert_eq!(
            result,
            Err(Error::Invalid("The query took too long.".into()))
        );
        assert!(started.elapsed() < Duration::from_secs(5));
    }

    #[tokio::test]
    async fn sqlite_snapshot_makes_a_consistent_copy() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        // Keep a WAL database with an open writer, like a running app.
        let writer = rusqlite::Connection::open(&file).unwrap();
        writer.pragma_update(None, "journal_mode", "WAL").unwrap();
        writer
            .execute("INSERT INTO notes(id, title) VALUES (3, 'Emma')", [])
            .unwrap();
        register_sqlite(&env, "notes", "dev", &file).await;
        let to = env.dir.path().join("copy.db");
        let target = "/internal/db/notes/dev/snapshot";
        let (status, _) = send(&env, &system(), "POST", target, json!({ "to": to })).await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let copy = rusqlite::Connection::open(&to).unwrap();
        let count: i64 = copy
            .query_row("SELECT count(*) FROM notes", [], |r| r.get(0))
            .unwrap();
        assert_eq!(count, 3);
        let mode: String = copy
            .query_row("PRAGMA journal_mode", [], |r| r.get(0))
            .unwrap();
        assert_eq!(mode, "delete");
        drop(copy);
        // No temp files left behind.
        let left: Vec<_> = std::fs::read_dir(env.dir.path())
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .collect();
        assert!(
            left.iter()
                .all(|n| !n.to_string_lossy().contains("snapshot")),
            "{left:?}"
        );

        // The target must not exist, and its folder must.
        let (status, _) = send(&env, &system(), "POST", target, json!({ "to": to })).await;
        assert_eq!(status, StatusCode::CONFLICT);
        let missing = env.dir.path().join("no/such/copy.db");
        let (status, _) = send(&env, &system(), "POST", target, json!({ "to": missing })).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let (status, _) = send(&env, &system(), "POST", target, json!({ "to": "rel.db" })).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        // Unknown database, no source file yet, and Postgres.
        let (status, _) = send(
            &env,
            &system(),
            "POST",
            "/internal/db/x/dev/snapshot",
            json!({ "to": to }),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        register_sqlite(&env, "later", "dev", &env.dir.path().join("later.db")).await;
        let to2 = env.dir.path().join("copy2.db");
        let (status, _) = send(
            &env,
            &system(),
            "POST",
            "/internal/db/later/dev/snapshot",
            json!({ "to": to2 }),
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT);
        assert!(!to2.exists());
        env.dbs
            .register(Reg {
                app: "pg".into(),
                env: "dev".into(),
                engine: Engine::Postgres(1),
            })
            .await
            .unwrap();
        let (status, _) = send(
            &env,
            &system(),
            "POST",
            "/internal/db/pg/dev/snapshot",
            json!({ "to": to2 }),
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT);
    }

    #[tokio::test]
    async fn a_down_database_never_fails_the_list() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "a.db");
        register_sqlite(&env, "notes", "dev", &file).await;
        // Nothing listens on port 1.
        env.dbs
            .register(Reg {
                app: "pg".into(),
                env: "dev".into(),
                engine: Engine::Postgres(1),
            })
            .await
            .unwrap();
        env.secrets
            .put(
                "pg",
                "dev",
                "DATABASE_PASSWORD",
                SecretKind::Config,
                "pw",
                "t",
                1,
            )
            .unwrap();
        let started = std::time::Instant::now();
        let (status, body) = send(&env, &person(), "GET", "/api/db", Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        assert!(started.elapsed() < Duration::from_secs(5));
        let list = body["databases"].as_array().unwrap();
        assert_eq!(list[0]["app"], "notes");
        assert_eq!(list[0]["status"], "ok");
        assert_eq!(list[1]["app"], "pg");
        assert_eq!(list[1]["status"], "down");
        let (status, _) = send(
            &env,
            &person(),
            "POST",
            "/api/db/pg/dev/query",
            json!({"sql": "select 1"}),
        )
        .await;
        assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE);
    }

    // ---- postgres (needs HOMELAB_TEST_POSTGRES, a superuser URL) ----

    static PG_LOCK: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

    struct Pg {
        env: TestEnv,
        port: u16,
        _guard: tokio::sync::MutexGuard<'static, ()>,
    }

    /// A fresh `app` role and database on the test server, registered as notes/dev.
    /// None when HOMELAB_TEST_POSTGRES is not set.
    async fn pg() -> Option<Pg> {
        let url = std::env::var("HOMELAB_TEST_POSTGRES").ok()?;
        let guard = PG_LOCK.lock().await;
        let config: tokio_postgres::Config = url.parse().unwrap();
        let port = config.get_ports()[0];
        let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
        tokio::spawn(connection);
        for sql in [
            "DROP DATABASE IF EXISTS app WITH (FORCE)",
            "DROP ROLE IF EXISTS homelab_ro",
            "DROP ROLE IF EXISTS app",
            "CREATE ROLE app SUPERUSER LOGIN PASSWORD 'app-pw'",
            "CREATE DATABASE app OWNER app",
        ] {
            client.batch_execute(sql).await.unwrap();
        }
        let env = test_env(1024);
        env.secrets
            .put(
                "notes",
                "dev",
                "DATABASE_PASSWORD",
                SecretKind::Config,
                "app-pw",
                "t",
                1,
            )
            .unwrap();
        env.dbs
            .register(Reg {
                app: "notes".into(),
                env: "dev".into(),
                engine: Engine::Postgres(port),
            })
            .await
            .unwrap();
        Some(Pg {
            env,
            port,
            _guard: guard,
        })
    }

    async fn q(env: &TestEnv, who: &Value, sql: &str, write: bool) -> (StatusCode, Value) {
        send(
            env,
            who,
            "POST",
            "/api/db/notes/dev/query",
            json!({ "sql": sql, "write": write }),
        )
        .await
    }

    #[tokio::test]
    async fn postgres_read_role_cannot_write() {
        let Some(pg) = pg().await else { return };
        let p = person();
        let (status, body) = q(&pg.env, &p, "create table notes(id int, title text)", true).await;
        assert_eq!(status, StatusCode::OK, "{body}");
        assert_eq!(body["command"], "OK 0");
        let (status, body) = q(
            &pg.env,
            &p,
            "insert into notes values (1, 'Dune'), (2, null)",
            true,
        )
        .await;
        assert_eq!(
            (status, &body["command"], &body["row_count"]),
            (StatusCode::OK, &json!("OK 2"), &json!(2))
        );

        // Read mode sees the data.
        let (status, body) = q(
            &pg.env,
            &p,
            "select id, title, now() is not null as ok from notes order by id",
            false,
        )
        .await;
        assert_eq!(status, StatusCode::OK, "{body}");
        assert_eq!(body["columns"], json!(["id", "title", "ok"]));
        assert_eq!(body["rows"], json!([["1", "Dune", "t"], ["2", null, "t"]]));
        assert_eq!(body["command"], "SELECT 2");
        // The query ran as the read-only role.
        let (_, body) = q(
            &pg.env,
            &p,
            "select current_user, current_setting('statement_timeout')",
            false,
        )
        .await;
        assert_eq!(body["rows"][0][0], "homelab_ro");

        // But not write, in any form.
        for sql in [
            "insert into notes values (3, 'x')",
            "update notes set title = 'x'",
            "delete from notes",
            "drop table notes",
            "create table t2(x int)",
            "select 1; insert into notes values (3, 'x')",
            "begin; insert into notes values (3, 'x'); commit",
            "insert into notes values (3, 'x'); select 1",
            "set role app",
            "create role evil superuser",
            "alter role app nosuperuser",
        ] {
            let (status, body) = q(&pg.env, &p, sql, false).await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{sql}: {body}");
            assert!(body["error"].is_string());
        }
        let (_, body) = q(&pg.env, &p, "select count(*) from notes", false).await;
        assert_eq!(body["rows"], json!([["2"]]));

        // Neither the password nor the role's secret appears anywhere we answer.
        let (_, list) = send(&pg.env, &p, "GET", "/api/db", Value::Null).await;
        assert!(!list.to_string().contains("pw"));
        let stored = pg.env.dbs.ro_password_for_test("notes", "dev").await;
        assert_eq!(stored.len(), 40);
        assert!(!list.to_string().contains(&stored));
    }

    #[tokio::test]
    async fn postgres_write_mode_and_access() {
        let Some(pg) = pg().await else { return };
        let p = person();
        // Several statements are fine when writing; the last result is returned.
        let (status, body) = q(
            &pg.env,
            &p,
            "create table a(x int); insert into a values (1), (2), (3); select sum(x) from a",
            true,
        )
        .await;
        assert_eq!(status, StatusCode::OK, "{body}");
        assert_eq!(body["rows"], json!([["6"]]));
        let (_, body) = q(&pg.env, &p, "select current_user", true).await;
        assert_eq!(body["rows"][0][0], "app");
        // The database's own message comes back as a 400.
        let (status, body) = q(&pg.env, &p, "selec 1", true).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(body["error"].as_str().unwrap().contains("syntax error"));
        // App keys and admin keys never write, but may read.
        let me = app_key("notes", "dev");
        let (status, _) = q(&pg.env, &me, "delete from a", true).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        let (status, body) = q(&pg.env, &me, "select count(*) from a", false).await;
        assert_eq!((status, &body["rows"]), (StatusCode::OK, &json!([["3"]])));
        let (status, _) = q(&pg.env, &admin(&["db:read"]), "delete from a", true).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        let (status, _) = send(
            &pg.env,
            &app_key("notes", "prod"),
            "POST",
            "/api/db/notes/dev/query",
            json!({"sql": "select 1"}),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn postgres_caps_rows() {
        let Some(pg) = pg().await else { return };
        let p = person();
        let (status, body) = q(
            &pg.env,
            &p,
            "select i, repeat('x', 10) from generate_series(1, 700) i",
            false,
        )
        .await;
        assert_eq!(status, StatusCode::OK, "{body}");
        assert_eq!(body["rows"].as_array().unwrap().len(), 500);
        assert_eq!(body["row_count"], 500);
        assert_eq!(body["truncated"], true);
        let (_, body) = q(
            &pg.env,
            &p,
            "select i from generate_series(1, 500) i",
            false,
        )
        .await;
        assert_eq!(body["truncated"], false);
        // The connection to the database is dropped; the next query still works.
        let (status, _) = q(&pg.env, &p, "select 1", false).await;
        assert_eq!(status, StatusCode::OK);
    }

    #[tokio::test]
    async fn postgres_statement_timeout() {
        let Some(pg) = pg().await else { return };
        let dbs = Dbs::open_with_limit(
            &pg.env.dir.path().join("short.db"),
            Duration::from_millis(500),
        )
        .unwrap();
        let reg = Reg {
            app: "notes".into(),
            env: "dev".into(),
            engine: Engine::Postgres(pg.port),
        };
        let secrets = Some(pg.env.secrets.clone());
        dbs.register(reg.clone()).await.unwrap();
        for write in [false, true] {
            let started = std::time::Instant::now();
            let result = dbs.query(&secrets, &reg, "select pg_sleep(5)", write).await;
            match result {
                Err(Error::Invalid(m)) => assert!(m.contains("statement timeout"), "{m}"),
                other => panic!("expected a timeout, got {other:?}"),
            }
            assert!(started.elapsed() < Duration::from_secs(4));
        }
    }

    #[tokio::test]
    async fn postgres_browse_info_and_role_repair() {
        let Some(pg) = pg().await else { return };
        let p = person();
        let setup = "create table notes(id int primary key, title text);
                     insert into notes select i, 'n' || i from generate_series(1, 120) i;
                     create schema other; create table other.\"we\"\"ird\"(v int); insert into other.\"we\"\"ird\" values (7)";
        let (status, body) = q(&pg.env, &p, setup, true).await;
        assert_eq!(status, StatusCode::OK, "{body}");

        let (_, body) = send(&pg.env, &p, "GET", "/api/db/notes/dev", Value::Null).await;
        assert_eq!(body["engine"], "postgres");
        assert_eq!(body["status"], "ok");
        assert_eq!(body["tables"], 2);
        assert!(body["size_bytes"].as_i64().unwrap() > 0);
        assert!(body["connections"].is_i64());

        let (_, body) = send(&pg.env, &p, "GET", "/api/db/notes/dev/tables", Value::Null).await;
        assert_eq!(
            body,
            json!({ "tables": [
                {"schema": "other", "name": "we\"ird", "rows": 1},
                {"schema": "public", "name": "notes", "rows": 120},
            ]})
        );
        let (_, body) = send(
            &pg.env,
            &p,
            "GET",
            "/api/db/notes/dev/tables/notes/rows?limit=5&offset=10",
            Value::Null,
        )
        .await;
        assert_eq!(body["columns"], json!(["id", "title"]));
        assert_eq!(body["rows"][0], json!(["11", "n11"]));
        assert_eq!(body["rows"].as_array().unwrap().len(), 5);
        assert_eq!(body["truncated"], true);
        let (_, body) = send(
            &pg.env,
            &p,
            "GET",
            "/api/db/notes/dev/tables/we%22ird/rows?schema=other",
            Value::Null,
        )
        .await;
        assert_eq!(body["rows"], json!([["7"]]));
        assert_eq!(body["truncated"], false);
        for t in [
            "/api/db/notes/dev/tables/pg_class/rows",
            "/api/db/notes/dev/tables/notes/rows?schema=other",
            "/api/db/notes/dev/tables/we%22ird/rows",
        ] {
            let (status, _) = send(&pg.env, &p, "GET", t, Value::Null).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{t}");
        }

        // The role is repaired when its password is lost, changed or the role is gone.
        let url = std::env::var("HOMELAB_TEST_POSTGRES").unwrap();
        let config: tokio_postgres::Config = url.parse().unwrap();
        let (admin_client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
        tokio::spawn(connection);
        for damage in [
            "ALTER ROLE homelab_ro PASSWORD 'changed'",
            "DROP ROLE homelab_ro",
        ] {
            admin_client.batch_execute(damage).await.unwrap();
            let (status, body) = q(&pg.env, &p, "select current_user", false).await;
            assert_eq!(status, StatusCode::OK, "{damage}: {body}");
            assert_eq!(body["rows"][0][0], "homelab_ro");
        }
        pg.env
            .dbs
            .register(Reg {
                app: "notes".into(),
                env: "dev".into(),
                engine: Engine::Postgres(pg.port),
            })
            .await
            .unwrap();
        // Same port: the stored password is kept.
        assert_eq!(
            pg.env.dbs.ro_password_for_test("notes", "dev").await.len(),
            40
        );
        // A changed port forgets it.
        pg.env
            .dbs
            .register(Reg {
                app: "notes".into(),
                env: "dev".into(),
                engine: Engine::Postgres(pg.port.wrapping_add(1)),
            })
            .await
            .unwrap();
        assert_eq!(pg.env.dbs.ro_password_for_test("notes", "dev").await, "");
    }

    #[tokio::test]
    async fn postgres_without_secrets_is_unavailable() {
        let Some(pg) = pg().await else { return };
        // A router whose secrets store failed to open.
        let (dbs, key) = (pg.env.dbs.clone(), test_key());
        let router = crate::app::app(crate::app::AppState {
            key: Arc::new(key.verifying_key()),
            version: "test",
            files: pg.env.files.clone(),
            secrets: None,
            secrets_problem: "broken".into(),
            dbs,
            logs: pg.env.logs.clone(),
            github_api: String::new(),
        });
        let req = request(
            &person(),
            "POST",
            "/api/db/notes/dev/query",
            br#"{"sql":"select 1"}"#.to_vec(),
            &[("content-type", "application/json")],
        );
        let response = router.oneshot(req).await.unwrap();
        // No stored password yet and no way to read the app's: a plain 503.
        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
        let bytes = response.into_body().collect().await.unwrap().to_bytes();
        assert!(!String::from_utf8_lossy(&bytes).contains("broken"));
    }

    // ---- safe writes: confirm, no BEGIN, the changes list ----

    /// A router with a Postgres entry that nothing listens on. Checks that happen before
    /// the database is reached can be tested with it.
    async fn nowhere() -> TestEnv {
        let env = test_env(1024);
        env.dbs
            .register(Reg {
                app: "notes".into(),
                env: "prod".into(),
                engine: Engine::Postgres(1),
            })
            .await
            .unwrap();
        env
    }

    async fn qp(env: &TestEnv, sql: &str, confirm: &str) -> (StatusCode, Value) {
        send(
            env,
            &person(),
            "POST",
            "/api/db/notes/prod/query?copy=c1",
            json!({ "sql": sql, "write": true, "confirm": confirm }),
        )
        .await
    }

    async fn changes_of(env: &TestEnv, target: &str) -> Vec<Value> {
        let (status, body) = send(env, &person(), "GET", target, Value::Null).await;
        assert_eq!(status, StatusCode::OK, "{body}");
        body["changes"].as_array().unwrap().clone()
    }

    #[tokio::test]
    async fn risky_statements_need_the_env_name() {
        let env = nowhere().await;
        for sql in [
            "drop table a",
            "truncate a",
            "alter table a add c int",
            "grant select on a to u",
            "revoke select on a from u",
            "delete from a",
            "update a set x = 1",
            "select 1; /* hi */ -- there\n DROP TABLE a",
        ] {
            for confirm in ["", "dev", "PROD", "prod2"] {
                let (status, body) = qp(&env, sql, confirm).await;
                assert_eq!(status, StatusCode::CONFLICT, "{sql} / {confirm}: {body}");
                assert_eq!(
                    body,
                    json!({
                        "error": "This can't be undone easily. Type prod to run it.",
                        "needs_confirm": true,
                    })
                );
            }
            // The right word gets past the check (and then finds no database).
            let (status, _) = qp(&env, sql, "prod").await;
            assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE, "{sql}");
        }
        // Reads never need it.
        let (status, _) = send(
            &env,
            &person(),
            "POST",
            "/api/db/notes/prod/query",
            json!({ "sql": "drop table a" }),
        )
        .await;
        assert_ne!(status, StatusCode::CONFLICT);
        // A confirm that was refused is not a write attempt; one that got through is.
        assert_eq!(
            changes_of(&env, "/api/db/notes/prod/changes").await.len(),
            8
        );
    }

    #[tokio::test]
    async fn statements_with_a_where_need_no_confirm() {
        let env = nowhere().await;
        for sql in [
            "delete from a where id = 1",
            "update a set x = 1 where id = 1",
            "insert into a values (1)",
            "select 1",
            "delete from a where id in (select id from b where x); select 2",
        ] {
            let (status, body) = qp(&env, sql, "").await;
            // Past the check, then no database is there.
            assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE, "{sql}: {body}");
        }
    }

    #[tokio::test]
    async fn transaction_statements_are_refused() {
        let env = nowhere().await;
        for sql in [
            "begin",
            "BEGIN; delete from a where id = 1; COMMIT",
            "insert into a values (1); commit",
            "rollback",
            "end",
            "start transaction",
            "-- hi\n begin",
        ] {
            let (status, body) = qp(&env, sql, "prod").await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{sql}");
            assert_eq!(
                body["error"],
                "Leave out BEGIN and COMMIT; Homelab runs it all in one go."
            );
        }
        // Refused SQL was not tried, so nothing is recorded.
        assert!(changes_of(&env, "/api/db/notes/prod/changes")
            .await
            .is_empty());
        // Empty SQL isn't an attempt either.
        let (status, _) = qp(&env, "  ", "").await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(changes_of(&env, "/api/db/notes/prod/changes")
            .await
            .is_empty());
    }

    #[tokio::test]
    async fn changes_are_for_people_only() {
        let env = nowhere().await;
        let target = "/api/db/notes/prod/changes";
        for who in [
            admin(&["db:read"]),
            admin(&[]),
            app_key("notes", "prod"),
            system(),
        ] {
            let (status, body) = send(&env, &who, "GET", target, Value::Null).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{who}");
            assert!(body["error"].is_string());
        }
        let (status, body) = send(&env, &person(), "GET", target, Value::Null).await;
        assert_eq!((status, body), (StatusCode::OK, json!({ "changes": [] })));
        let (status, _) = send(
            &env,
            &person(),
            "GET",
            "/api/db/nope/prod/changes",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        for bad in ["limit=0", "limit=201", "limit=x", "before=x"] {
            let (status, _) = send(
                &env,
                &person(),
                "GET",
                &format!("{target}?{bad}"),
                Value::Null,
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{bad}");
        }
        // Only the exact route; other methods don't reach it.
        let (status, _) = send(&env, &person(), "POST", target, json!({})).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    fn change(i: i64, env: &str) -> crate::dbs::Change {
        crate::dbs::Change {
            id: 0,
            app: "notes".into(),
            env: env.into(),
            person: "p1".into(),
            at: i,
            sql: format!("insert into a values ({i})"),
            ok: true,
            rows: 1,
            ms: 2,
            error: String::new(),
            copy: String::new(),
        }
    }

    #[tokio::test]
    async fn changes_keep_the_newest_thousand_per_database() {
        let env = nowhere().await;
        for i in 1..=1005 {
            env.dbs.record_change(change(i, "prod")).await.unwrap();
        }
        for i in 1..=3 {
            env.dbs.record_change(change(i, "dev")).await.unwrap();
        }
        let all = env.dbs.changes("notes", "prod", 5000, None).await.unwrap();
        assert_eq!(all.len(), 1000);
        // Newest first; the five oldest are gone.
        assert_eq!(all[0].at, 1005);
        assert_eq!(all[999].at, 6);
        assert_eq!(
            env.dbs
                .changes("notes", "dev", 50, None)
                .await
                .unwrap()
                .len(),
            3
        );

        // Paging through the route with limit and before.
        let target = "/api/db/notes/prod/changes";
        let first = changes_of(&env, &format!("{target}?limit=200")).await;
        assert_eq!(first.len(), 200);
        assert_eq!(first[0]["sql"], "insert into a values (1005)");
        assert_eq!(first[0]["copy"], Value::Null);
        let last = first[199]["id"].as_i64().unwrap();
        let next = changes_of(&env, &format!("{target}?limit=2&before={last}")).await;
        assert_eq!(next.len(), 2);
        assert_eq!(next[0]["id"].as_i64().unwrap(), last - 1);
        assert_eq!(changes_of(&env, target).await.len(), 50);

        // Forgetting the app forgets its changes too.
        env.dbs.forget_app("notes").await.unwrap();
        assert!(env
            .dbs
            .changes("notes", "dev", 50, None)
            .await
            .unwrap()
            .is_empty());
    }

    #[tokio::test]
    async fn an_adopted_sqlite_file_can_have_any_name() {
        let env = test_env(1024);
        let file = sqlite_file(&env, "catalogue_full.sqlite3");
        register_sqlite(&env, "shop", "prod", &file).await;
        let (status, body) = send(&env, &person(), "GET", "/api/db/shop/prod", Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            (&body["engine"], &body["status"], &body["tables"]),
            (&json!("sqlite"), &json!("ok"), &json!(1))
        );
        let (_, body) = send(
            &env,
            &person(),
            "GET",
            "/api/db/shop/prod/tables",
            Value::Null,
        )
        .await;
        assert_eq!(body["tables"][0]["rows"], 2);
        let (status, body) = send(
            &env,
            &person(),
            "POST",
            "/api/db/shop/prod/query",
            json!({ "sql": "select title from notes order by id" }),
        )
        .await;
        assert_eq!(
            (status, &body["rows"]),
            (StatusCode::OK, &json!([["Dune"], [null]]))
        );
        // Still read only, and writing is still for Postgres.
        let (status, body) = send(
            &env,
            &person(),
            "POST",
            "/api/db/shop/prod/query",
            json!({ "sql": "delete from notes", "write": true, "confirm": "prod" }),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(body["error"], "Writing is only for Postgres databases.");
        assert!(changes_of(&env, "/api/db/shop/prod/changes")
            .await
            .is_empty());
        // The online copy works whatever the file is called.
        let to = env.dir.path().join("copy.db");
        let (status, _) = send(
            &env,
            &system(),
            "POST",
            "/internal/db/shop/prod/snapshot",
            json!({ "to": to }),
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let copy = rusqlite::Connection::open(&to).unwrap();
        let n: i64 = copy
            .query_row("select count(*) from notes", [], |r| r.get(0))
            .unwrap();
        assert_eq!(n, 2);
    }

    /// Writes to notes/dev (the pg() database) as a person.
    async fn w(env: &TestEnv, sql: &str, confirm: &str, copy: &str) -> (StatusCode, Value) {
        let target = if copy.is_empty() {
            "/api/db/notes/dev/query".to_string()
        } else {
            format!("/api/db/notes/dev/query?copy={copy}")
        };
        send(
            env,
            &person(),
            "POST",
            &target,
            json!({ "sql": sql, "write": true, "confirm": confirm }),
        )
        .await
    }

    #[tokio::test]
    async fn postgres_risky_kinds_need_the_right_confirm() {
        let Some(pg) = pg().await else { return };
        let e = &pg.env;
        // The risky statement, and a query that shows whether it ran: (before, after).
        let kinds = [
            ("drop table a", "select to_regclass('a')::text", "a", "NULL"),
            ("truncate a", "select count(*)::text from a", "2", "0"),
            ("alter table a add column y int", "select count(*)::text from information_schema.columns where table_name = 'a'", "1", "2"),
            ("grant select on a to public", "select count(*)::text from information_schema.role_table_grants where table_name = 'a' and grantee = 'PUBLIC'", "0", "1"),
            ("delete from a", "select count(*)::text from a", "2", "0"),
            ("update a set x = 9", "select sum(x)::text from a", "3", "18"),
            ("/* c */ DELETE FROM a -- where x = 1", "select count(*)::text from a", "2", "0"),
        ];
        for (risky, probe, before, after) in kinds {
            let (status, body) = w(
                e,
                "drop table if exists a; create table a(x int); insert into a values (1), (2)",
                "dev",
                "",
            )
            .await;
            assert_eq!(status, StatusCode::OK, "{body}");
            let shows = |body: &Value| match &body["rows"][0][0] {
                Value::Null => "NULL".to_string(),
                v => v.as_str().unwrap().to_string(),
            };
            let (_, body) = w(e, probe, "", "").await;
            let start = shows(&body);
            // `drop`'s probe is a table name before and NULL after.
            assert!(start == before || before == "a", "{risky}: {start}");
            // Wrong words: asked again, nothing ran.
            for confirm in ["", "prod", "DEV", "d", "dev dev"] {
                let (status, body) = w(e, risky, confirm, "").await;
                assert_eq!(status, StatusCode::CONFLICT, "{risky} / {confirm}: {body}");
                assert_eq!(body["needs_confirm"], true);
                assert_eq!(
                    body["error"],
                    "This can't be undone easily. Type dev to run it."
                );
            }
            let (_, body) = w(e, probe, "", "").await;
            assert_eq!(shows(&body), start, "{risky} ran without confirm");
            // The right word runs it.
            let (status, body) = w(e, risky, "dev", "").await;
            assert_eq!(status, StatusCode::OK, "{risky}: {body}");
            let (_, body) = w(e, probe, "", "").await;
            assert_eq!(shows(&body), after, "{risky}");
        }
        // A DELETE or UPDATE with a WHERE runs with no confirm.
        w(
            e,
            "drop table if exists a; create table a(x int); insert into a values (1), (2)",
            "dev",
            "",
        )
        .await;
        let (status, _) = w(e, "delete from a where x = 2", "", "").await;
        assert_eq!(status, StatusCode::OK);
        let (status, _) = w(e, "update a set x = 3 where x = 1", "", "").await;
        assert_eq!(status, StatusCode::OK);
        let (_, body) = w(e, "select x from a", "", "").await;
        assert_eq!(body["rows"], json!([["3"]]));
    }

    #[tokio::test]
    async fn postgres_writes_are_all_or_nothing() {
        let Some(pg) = pg().await else { return };
        let e = &pg.env;
        w(e, "create table a(x int primary key)", "", "").await;
        let (status, body) = w(
            e,
            "insert into a values (1); insert into a values (1)",
            "",
            "",
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(body["error"].as_str().unwrap().contains("duplicate key"));
        let (_, rows) = w(e, "select count(*) from a", "", "").await;
        assert_eq!(rows["rows"], json!([["0"]]));
        // Also when the second statement is a syntax error, or the first is a change of shape.
        let (status, _) = w(e, "insert into a values (2); selec 1", "", "").await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let (status, _) = w(
            e,
            "create table z(x int); insert into nope values (1)",
            "",
            "",
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let (_, rows) = w(
            e,
            "select (select count(*) from a)::text, to_regclass('z')::text",
            "",
            "",
        )
        .await;
        assert_eq!(rows["rows"], json!([["0", null]]));
        // A COMMIT in the middle can't split it: it is refused before anything runs.
        let (status, _) = w(
            e,
            "insert into a values (3); commit; insert into nope values (1)",
            "",
            "",
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let (_, rows) = w(e, "select count(*) from a", "", "").await;
        assert_eq!(rows["rows"], json!([["0"]]));
        // All good: all of it.
        let (status, _) = w(
            e,
            "insert into a values (4); insert into a values (5)",
            "",
            "",
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let (_, rows) = w(e, "select count(*) from a", "", "").await;
        assert_eq!(rows["rows"], json!([["2"]]));
    }

    #[tokio::test]
    async fn postgres_writes_are_recorded() {
        let Some(pg) = pg().await else { return };
        let e = &pg.env;
        let (status, _) = w(e, "create table a(x int primary key)", "", "").await;
        assert_eq!(status, StatusCode::OK);
        let (status, _) = w(
            e,
            "insert into a values (1), (2), (3)",
            "",
            "20261023-120000-before-change",
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let (status, _) = w(
            e,
            "insert into a values (1)",
            "",
            "20261023-120001-before-change",
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        // A copy name with odd characters is not written down.
        w(e, "select 1", "", "a%20b").await;
        // Reads and refused SQL leave nothing.
        q(e, &person(), "select 1", false).await;
        w(e, "drop table a", "", "").await;
        w(e, "begin", "dev", "").await;

        let list = changes_of(e, "/api/db/notes/dev/changes").await;
        assert_eq!(list.len(), 4);
        // Newest first.
        assert_eq!(list[0]["sql"], "select 1");
        assert_eq!(list[0]["copy"], Value::Null);
        assert_eq!(list[1]["ok"], false);
        assert_eq!(list[1]["rows"], 0);
        assert_eq!(list[1]["copy"], "20261023-120001-before-change");
        assert!(list[1]["error"].as_str().unwrap().contains("duplicate key"));
        assert_eq!(list[2]["ok"], true);
        assert_eq!(list[2]["rows"], 3);
        assert_eq!(list[2]["error"], "");
        assert_eq!(list[2]["copy"], "20261023-120000-before-change");
        assert_eq!(list[2]["person"], "p1");
        assert_eq!(list[2]["sql"], "insert into a values (1), (2), (3)");
        assert!(list[2]["at"].as_i64().unwrap() > 1_700_000_000);
        assert!(list[2]["ms"].is_i64());
        assert!(list[3]["id"].as_i64().unwrap() < list[2]["id"].as_i64().unwrap());
        assert_eq!(list[3]["copy"], Value::Null);
        // Only that database's list.
        assert!(
            changes_of(e, "/api/db/notes/dev/changes?limit=1")
                .await
                .len()
                == 1
        );
        let (status, _) = send(
            e,
            &person(),
            "GET",
            "/api/db/notes/prod/changes",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }
}
