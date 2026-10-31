//! The log routes.
//!
//! * `POST /internal/logs/ingest` and `POST /internal/logs/event` are for homelabd only (the
//!   `system` caller): it sends the lines of the apps' containers and Homelab's own events.
//! * `GET /api/logs` is for people, admin keys with `logs:read`, and app keys with
//!   `logs:read`. An app sees only its own lines, and Homelab's lines about it.

use std::collections::HashMap;
use std::sync::Arc;

use axum::body::Body;
use axum::extract::{Query as UrlQuery, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
use chrono::DateTime;
use serde::Deserialize;
use serde_json::{json, Value};

use super::{Level, Logs, Query, RawLine, Scope, MAX_EXTRA, MAX_LIMIT};
use crate::api::{err, valid_place, ApiError, Reply};
use crate::app::{not_found, AppState};
use crate::caller::{Caller, Kind};
use crate::disk::valid_name;

/// The scope an admin key or app key needs to read logs.
pub const SCOPE_READ: &str = "logs:read";

const APP_PATH: &str = "/api/logs";
const INTERNAL_PREFIX: &str = "/internal/logs";

/// The biggest ingest or event body.
const BODY_LIMIT: usize = 4 * 1024 * 1024;
/// The most lines in one ingest.
const MAX_INGEST_LINES: usize = 2000;

pub fn routes() -> Router<AppState> {
    Router::new()
        .route(APP_PATH, get(search).fallback(not_found))
        .route("/internal/logs/ingest", post(ingest).fallback(not_found))
        .route("/internal/logs/event", post(event).fallback(not_found))
}

/// Refuses a caller who may not use the path at all, whether or not a route is there:
/// anyone but homelabd under /internal/logs, and homelabd on /api/logs.
pub fn refuse_path(caller: &Caller, path: &str) -> Option<ApiError> {
    let under = |prefix: &str| {
        path == prefix
            || path
                .strip_prefix(prefix)
                .is_some_and(|r| r.starts_with('/'))
    };
    if under(INTERNAL_PREFIX) && caller.kind != Kind::System {
        return Some(forbidden(
            caller,
            "Only Homelab itself can use these routes.",
        ));
    }
    if under(APP_PATH) && caller.kind == Kind::System {
        return Some(forbidden(
            caller,
            "These routes are for people and keys. Homelab itself uses /internal/logs.",
        ));
    }
    None
}

fn forbidden(caller: &Caller, why: &str) -> ApiError {
    crate::api::forbidden("logs", caller, why)
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

/// Reads a JSON body of at most 4 MB.
async fn read_body<T: serde::de::DeserializeOwned>(body: Body) -> Result<T, ApiError> {
    let bytes = axum::body::to_bytes(body, BODY_LIMIT)
        .await
        .map_err(|_| err(StatusCode::PAYLOAD_TOO_LARGE, "That body is over 4 MB."))?;
    serde_json::from_slice(&bytes)
        .map_err(|_| err(StatusCode::BAD_REQUEST, "That isn't a valid request body."))
}

/// Runs a store call on a blocking thread.
async fn blocking<T: Send + 'static>(
    logs: Arc<Logs>,
    f: impl FnOnce(&Logs) -> Result<T, String> + Send + 'static,
) -> Result<T, ApiError> {
    tokio::task::spawn_blocking(move || f(&logs))
        .await
        .map_err(|_| err(StatusCode::INTERNAL_SERVER_ERROR, "Something went wrong."))?
        .map_err(|m| err(StatusCode::INTERNAL_SERVER_ERROR, m))
}

// ---- for homelabd ----

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct IngestBody {
    app: String,
    env: String,
    service: String,
    /// Lines below this are dropped. Default: debug in dev, info in prod.
    #[serde(default)]
    min_level: Option<String>,
    lines: Vec<LineBody>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct LineBody {
    /// Unix milliseconds.
    t: i64,
    stream: String,
    text: String,
}

/// POST /internal/logs/ingest: `{"stored": n}`.
async fn ingest(State(s): State<AppState>, caller: Caller, body: Body) -> Reply {
    system_caller(&caller)?;
    let body: IngestBody = read_body(body).await?;
    if !valid_place(&body.app, &body.env) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "The app must be a valid app name and the env must be dev or prod.",
        ));
    }
    if !valid_name(&body.service, 60) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "A service name needs 1-60 lowercase letters, digits, _ or -.",
        ));
    }
    let min = match body.min_level.as_deref() {
        None | Some("") => {
            if body.env == "dev" {
                Level::Debug
            } else {
                Level::Info
            }
        }
        Some(l) => Level::parse(l)
            .filter(|l| *l != Level::Unknown)
            .ok_or_else(|| {
                err(
                    StatusCode::BAD_REQUEST,
                    "The min_level is debug, info, warn, error or fatal.",
                )
            })?,
    };
    if body.lines.len() > MAX_INGEST_LINES {
        return Err(err(
            StatusCode::BAD_REQUEST,
            format!("Send at most {MAX_INGEST_LINES} lines at a time."),
        ));
    }
    if body
        .lines
        .iter()
        .any(|l| !matches!(l.stream.as_str(), "stdout" | "stderr"))
    {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "A line's stream is stdout or stderr.",
        ));
    }
    let lines: Vec<RawLine> = body
        .lines
        .into_iter()
        .map(|l| RawLine {
            t: l.t,
            stream: l.stream,
            text: l.text,
        })
        .collect();
    let stored = blocking(s.logs.clone(), move |logs| {
        logs.ingest(&body.app, &body.env, &body.service, min, &lines)
    })
    .await?;
    Ok(Json(json!({ "stored": stored })).into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct EventBody {
    service: String,
    level: String,
    msg: String,
    #[serde(default)]
    extra: Value,
    #[serde(default)]
    about_app: String,
    #[serde(default)]
    about_env: String,
}

/// POST /internal/logs/event: `{"stored": 0|1}`.
async fn event(State(s): State<AppState>, caller: Caller, body: Body) -> Reply {
    system_caller(&caller)?;
    let body: EventBody = read_body(body).await?;
    if !valid_name(&body.service, 60) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "A service name needs 1-60 lowercase letters, digits, _ or -.",
        ));
    }
    let level = Level::parse(&body.level)
        .filter(|l| *l != Level::Unknown)
        .ok_or_else(|| {
            err(
                StatusCode::BAD_REQUEST,
                "The level is debug, info, warn, error or fatal.",
            )
        })?;
    if body.msg.trim().is_empty() {
        return Err(err(StatusCode::BAD_REQUEST, "The msg can't be empty."));
    }
    if !matches!(body.extra, Value::Null | Value::Object(_)) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "The extra must be a JSON object.",
        ));
    }
    if body.extra.to_string().len() > MAX_EXTRA {
        return Err(err(StatusCode::BAD_REQUEST, "The extra is over 8 KB."));
    }
    let about = (!body.about_app.is_empty(), !body.about_env.is_empty());
    if about != (false, false) && !valid_place(&body.about_app, &body.about_env) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "The about_app must be a valid app name and the about_env must be dev or prod, \
             or leave both out.",
        ));
    }
    let stored = s.logs.event(
        &body.service,
        level,
        &body.msg,
        body.extra,
        &body.about_app,
        &body.about_env,
    );
    Ok(Json(json!({ "stored": u8::from(stored) })).into_response())
}

// ---- for people and keys ----

/// What the caller may see.
fn scope_of(caller: &Caller) -> Result<Scope, ApiError> {
    let needs = || {
        forbidden(
            caller,
            &format!("This key can't do that. It needs the {SCOPE_READ} scope."),
        )
    };
    let has_scope = caller.scopes.iter().any(|s| s == SCOPE_READ);
    match caller.kind {
        Kind::Person => Ok(Scope::All),
        Kind::Admin if has_scope => Ok(Scope::All),
        Kind::Admin => Err(needs()),
        Kind::App if !has_scope => Err(needs()),
        Kind::App if !valid_place(&caller.app, &caller.env) => {
            Err(forbidden(caller, "This key isn't an app's own key."))
        }
        Kind::App => Ok(Scope::App {
            app: caller.app.clone(),
            env: caller.env.clone(),
        }),
        Kind::System => Err(forbidden(
            caller,
            "These routes are for people and keys. Homelab itself uses /internal/logs.",
        )),
    }
}

fn bad(message: &str) -> ApiError {
    err(StatusCode::BAD_REQUEST, message)
}

/// A time as RFC 3339 or unix milliseconds.
fn parse_time(name: &str, text: &str) -> Result<i64, ApiError> {
    let text = text.trim();
    if !text.is_empty() && text.bytes().all(|b| b.is_ascii_digit()) {
        return text
            .parse()
            .map_err(|_| bad(&format!("The {name} is a time like 2026-10-23T22:00:00Z.")));
    }
    DateTime::parse_from_rfc3339(text)
        .map(|t| t.timestamp_millis())
        .map_err(|_| bad(&format!("The {name} is a time like 2026-10-23T22:00:00Z.")))
}

fn parse_id(name: &str, text: &str) -> Result<i64, ApiError> {
    match text.trim().parse::<i64>() {
        Ok(n) if n >= 0 => Ok(n),
        _ => Err(bad(&format!("The {name} is a line id."))),
    }
}

fn parse_query(params: &HashMap<String, String>) -> Result<Query, ApiError> {
    let get = |k: &str| params.get(k).map(String::as_str).unwrap_or("");
    let mut q = Query {
        q: get("q").to_string(),
        app: get("app").to_string(),
        env: get("env").to_string(),
        service: get("service").to_string(),
        ..Query::default()
    };
    for name in get("level")
        .split(',')
        .map(str::trim)
        .filter(|n| !n.is_empty())
    {
        q.levels.push(Level::parse(name).ok_or_else(|| {
            bad("The level is a list of debug, info, warn, error, fatal or unknown.")
        })?);
    }
    if !get("min_level").is_empty() {
        q.min_level = Some(
            Level::parse(get("min_level"))
                .filter(|l| *l != Level::Unknown)
                .ok_or_else(|| bad("The min_level is debug, info, warn, error or fatal."))?,
        );
    }
    if !get("since").is_empty() {
        q.since = Some(parse_time("since", get("since"))?);
    }
    if !get("until").is_empty() {
        q.until = Some(parse_time("until", get("until"))?);
    }
    if !get("before").is_empty() {
        q.before = Some(parse_id("before", get("before"))?);
    }
    if !get("after").is_empty() {
        q.after = Some(parse_id("after", get("after"))?);
    }
    if !get("limit").is_empty() {
        q.limit = match get("limit").parse::<usize>() {
            Ok(n) if (1..=MAX_LIMIT).contains(&n) => n,
            _ => return Err(bad("The limit is a number from 1 to 500.")),
        };
    }
    Ok(q)
}

/// GET /api/logs
async fn search(
    State(s): State<AppState>,
    caller: Caller,
    UrlQuery(params): UrlQuery<HashMap<String, String>>,
) -> Reply {
    let scope = scope_of(&caller)?;
    let query = parse_query(&params)?;
    // An app asking for another app or environment finds nothing.
    if let Scope::App { app, env } = &scope {
        if (!query.app.is_empty() && &query.app != app)
            || (!query.env.is_empty() && &query.env != env)
        {
            return Err(err(StatusCode::NOT_FOUND, "There's no such app."));
        }
    }
    let found = blocking(s.logs.clone(), move |logs| logs.search(&scope, &query)).await?;
    let counts: serde_json::Map<String, Value> = found
        .counts
        .iter()
        .map(|(level, n)| (level.name().to_string(), json!(n)))
        .collect();
    let lines: Vec<Value> = found.lines.iter().map(|l| l.to_json()).collect();
    Ok(Json(json!({
        "lines": lines,
        "counts": counts,
        "next": found.next.map(|n| n.to_string()),
    }))
    .into_response())
}
