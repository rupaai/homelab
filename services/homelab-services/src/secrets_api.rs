//! The secrets routes.
//!
//! Two sets of routes, with different callers:
//!
//! * `/api/secrets...` is for apps (reached through the app door). An app sees only its
//!   own app and environment, needs `secrets:read` to read and `secrets:write` to change,
//!   may read the config secrets of its own app and environment but never change them, and
//!   never sees platform secrets or names starting with `HOMELAB_`.
//! * `/internal/secrets...` is for homelabd only (the `system` caller). Homelab applies its
//!   own rules (owner, session) before it calls these.
//!
//! People and admin API keys get 403 on every secrets route, always. That is checked in
//! `app.rs` for the whole path (so unknown paths under it are 403 too) and again here.
//!
//! No value is ever written to a log or an error message.

use std::collections::HashMap;
use std::sync::Arc;

use axum::body::Bytes;
use axum::extract::{DefaultBodyLimit, Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::app::{error_response, not_found, AppState};
use crate::caller::{Caller, Kind};
use crate::secrets::{self, Error, Info, Kind as SecretKind, Secrets};

/// Scopes an app key needs.
pub const SCOPE_READ: &str = "secrets:read";
pub const SCOPE_WRITE: &str = "secrets:write";

/// The biggest request body: a 64 KiB value, written as JSON with every byte escaped,
/// still fits.
const BODY_LIMIT: usize = 512 * 1024;

/// Whether a path is one of the secrets routes (app or internal).
pub fn is_secrets_path(path: &str) -> bool {
    [APP_PREFIX, INTERNAL_PREFIX]
        .iter()
        .any(|p| path == *p || path.strip_prefix(p).is_some_and(|r| r.starts_with('/')))
}

const APP_PREFIX: &str = "/api/secrets";
const INTERNAL_PREFIX: &str = "/internal/secrets";

pub fn routes() -> Router<AppState> {
    Router::new()
        // For apps.
        .route("/api/secrets", get(app_list).fallback(not_found))
        .route(
            "/api/secrets/{name}",
            get(app_get)
                .put(app_put)
                .delete(app_delete)
                .fallback(not_found)
                .layer(DefaultBodyLimit::max(BODY_LIMIT)),
        )
        .route(
            "/api/secrets/{name}/versions",
            get(app_versions).fallback(not_found),
        )
        // For homelabd.
        .route("/internal/secrets", get(sys_list).fallback(not_found))
        .route(
            "/internal/secrets/item/{name}",
            get(sys_get)
                .put(sys_put)
                .delete(sys_delete)
                .fallback(not_found)
                .layer(DefaultBodyLimit::max(BODY_LIMIT)),
        )
        .route(
            "/internal/secrets/item/{name}/versions",
            get(sys_versions).fallback(not_found),
        )
        .route(
            "/internal/secrets/item/{name}/restore",
            post(sys_restore).fallback(not_found),
        )
        .route(
            "/internal/secrets/missing",
            post(sys_missing).fallback(not_found),
        )
        .route(
            "/internal/secrets/values",
            post(sys_values).fallback(not_found),
        )
        .route(
            "/internal/secrets/app/{app}",
            delete(sys_delete_app).fallback(not_found),
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

impl From<Error> for ApiError {
    fn from(e: Error) -> ApiError {
        match e {
            Error::Invalid(m) => err(StatusCode::BAD_REQUEST, m),
            Error::Missing(m) => err(StatusCode::NOT_FOUND, m),
            Error::Conflict(m) => err(StatusCode::CONFLICT, m),
            Error::Forbidden(m) => err(StatusCode::FORBIDDEN, m),
            Error::Seal => {
                tracing::error!("a secret could not be decrypted");
                err(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "That secret couldn't be decrypted.",
                )
            }
            Error::Db(why) => {
                tracing::error!("secrets database error: {why}");
                err(StatusCode::INTERNAL_SERVER_ERROR, "Something went wrong.")
            }
        }
    }
}

type Reply = Result<Response, ApiError>;

/// What a caller who may not use secrets gets. Logged without any value.
pub fn forbidden(caller: &Caller, why: &str) -> ApiError {
    tracing::warn!(kind = ?caller.kind, app = %caller.app, env = %caller.env, "secrets request refused");
    err(StatusCode::FORBIDDEN, why)
}

/// The answer for a caller kind that never gets secrets (people, admin keys).
pub fn refuse_kind(caller: &Caller) -> Option<ApiError> {
    match caller.kind {
        Kind::Person | Kind::Admin => Some(forbidden(
            caller,
            "Secrets can only be used by the app itself.",
        )),
        _ => None,
    }
}

/// Refuses a caller who may not use the path at all, whether or not a route is there:
/// people and admin keys on any secrets path, and anyone but homelabd under /internal.
pub fn refuse_path(caller: &Caller, path: &str) -> Option<ApiError> {
    if !is_secrets_path(path) {
        return None;
    }
    if let Some(e) = refuse_kind(caller) {
        return Some(e);
    }
    let internal = path == INTERNAL_PREFIX || path.starts_with("/internal/secrets/");
    if internal && caller.kind != Kind::System {
        return Some(forbidden(
            caller,
            "Only Homelab itself can use these routes.",
        ));
    }
    None
}

// ---- access ----

/// An app caller that may use secrets, and where.
struct AppPlace {
    app: String,
    env: String,
    by: String,
}

/// Lets an app through when it has the scope, for its own app and environment only.
fn app_caller(caller: &Caller, scope: &str) -> Result<AppPlace, ApiError> {
    if let Some(e) = refuse_kind(caller) {
        return Err(e);
    }
    if caller.kind != Kind::App {
        return Err(forbidden(
            caller,
            "These routes are for apps. Homelab itself uses /internal/secrets.",
        ));
    }
    if caller.app.is_empty() || (caller.env != "dev" && caller.env != "prod") {
        return Err(forbidden(caller, "This key isn't an app's own key."));
    }
    if !caller.scopes.iter().any(|s| s == scope) {
        return Err(forbidden(
            caller,
            &format!("This key can't do that. It needs the {scope} scope."),
        ));
    }
    Ok(AppPlace {
        app: caller.app.clone(),
        env: caller.env.clone(),
        by: format!("app:{}/{}", caller.app, caller.env),
    })
}

/// Lets homelabd through, and nobody else.
fn system_caller(caller: &Caller) -> Result<(), ApiError> {
    if let Some(e) = refuse_kind(caller) {
        return Err(e);
    }
    if caller.kind != Kind::System {
        return Err(forbidden(
            caller,
            "Only Homelab itself can use these routes.",
        ));
    }
    Ok(())
}

/// The store, or why there is none.
/// Only homelabd is told why; an app gets a short answer.
fn store(s: &AppState, caller: &Caller) -> Result<Arc<Secrets>, ApiError> {
    s.secrets.clone().ok_or_else(|| {
        err(
            StatusCode::SERVICE_UNAVAILABLE,
            if caller.kind == Kind::System && !s.secrets_problem.is_empty() {
                format!("The secrets service isn't running: {}.", s.secrets_problem)
            } else {
                "The secrets service isn't running right now.".to_string()
            },
        )
    })
}

/// Runs a store call on a blocking thread.
async fn blocking<T: Send + 'static>(
    store: Arc<Secrets>,
    f: impl FnOnce(&Secrets) -> Result<T, Error> + Send + 'static,
) -> Result<T, ApiError> {
    tokio::task::spawn_blocking(move || f(&store))
        .await
        .map_err(|_| err(StatusCode::INTERNAL_SERVER_ERROR, "Something went wrong."))?
        .map_err(ApiError::from)
}

fn now_ms() -> i64 {
    crate::app::unix_now().saturating_mul(1000)
}

fn parse_body<T: serde::de::DeserializeOwned>(body: &Bytes) -> Result<T, ApiError> {
    serde_json::from_slice(body).map_err(|_| {
        err(
            StatusCode::BAD_REQUEST,
            "That isn't a valid request body.".to_string(),
        )
    })
}

fn parse_version(q: &HashMap<String, String>) -> Result<Option<i64>, ApiError> {
    match q.get("version") {
        None => Ok(None),
        Some(v) => match v.parse::<i64>() {
            Ok(n) if n >= 1 => Ok(Some(n)),
            _ => Err(err(
                StatusCode::BAD_REQUEST,
                "The version is a number from 1.",
            )),
        },
    }
}

fn info_json(i: &Info) -> Value {
    json!({
        "name": i.name,
        "kind": i.kind.as_str(),
        "version": i.version,
        "updated_at": i.updated_at / 1000,
    })
}

// ---- for apps ----

/// GET /api/secrets: names, kinds, versions. No values.
async fn app_list(State(s): State<AppState>, caller: Caller) -> Reply {
    let p = app_caller(&caller, SCOPE_READ)?;
    let store = store(&s, &caller)?;
    let list = blocking(store, move |st| st.list(&p.app, &p.env, None)).await?;
    let secrets: Vec<Value> = list
        .iter()
        .filter(|i| !secrets::is_reserved(&i.name))
        .map(info_json)
        .collect();
    Ok(Json(json!({ "secrets": secrets })).into_response())
}

/// GET /api/secrets/{name}[?version=n]
async fn app_get(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    let p = app_caller(&caller, SCOPE_READ)?;
    let store = store(&s, &caller)?;
    let version = parse_version(&q)?;
    let missing = || err(StatusCode::NOT_FOUND, "There's no such secret.");
    if secrets::is_reserved(&name) {
        return Err(missing());
    }
    let key = name.clone();
    let found = blocking(store, move |st| st.get(&p.app, &p.env, &key, version))
        .await?
        .ok_or_else(missing)?;
    Ok(Json(json!({
        "name": name,
        "value": found.value.as_str(),
        "version": found.version,
    }))
    .into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PutBody {
    value: String,
}

/// PUT /api/secrets/{name}: saves an app secret as the next version.
async fn app_put(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    body: Bytes,
) -> Reply {
    let p = app_caller(&caller, SCOPE_WRITE)?;
    let store = store(&s, &caller)?;
    let body: PutBody = parse_body(&body)?;
    if secrets::is_reserved(&name) {
        return Err(err(
            StatusCode::BAD_REQUEST,
            "Names starting with HOMELAB_ are Homelab's own.",
        ));
    }
    let key = name.clone();
    let version = blocking(store, move |st| {
        st.put(
            &p.app,
            &p.env,
            &key,
            SecretKind::App,
            &body.value,
            &p.by,
            now_ms(),
        )
    })
    .await?;
    Ok(Json(json!({ "name": name, "version": version })).into_response())
}

/// DELETE /api/secrets/{name}: removes an app secret. Config secrets can't be removed
/// by the app. Removing one that isn't there is fine.
async fn app_delete(State(s): State<AppState>, caller: Caller, Path(name): Path<String>) -> Reply {
    let p = app_caller(&caller, SCOPE_WRITE)?;
    let store = store(&s, &caller)?;
    if secrets::is_reserved(&name) {
        return Err(forbidden(
            &caller,
            "Names starting with HOMELAB_ are Homelab's own.",
        ));
    }
    blocking(store, move |st| {
        st.delete(&p.app, &p.env, &name, Some(SecretKind::App))
    })
    .await?;
    Ok(StatusCode::NO_CONTENT.into_response())
}

/// GET /api/secrets/{name}/versions: the kept versions, no values.
async fn app_versions(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
) -> Reply {
    let p = app_caller(&caller, SCOPE_READ)?;
    let store = store(&s, &caller)?;
    if secrets::is_reserved(&name) {
        return Err(err(StatusCode::NOT_FOUND, "There's no such secret."));
    }
    let list = blocking(store, move |st| st.versions(&p.app, &p.env, &name)).await?;
    if list.is_empty() {
        return Err(err(StatusCode::NOT_FOUND, "There's no such secret."));
    }
    let versions: Vec<Value> = list
        .iter()
        .map(|v| json!({ "version": v.version, "set_at": v.set_at / 1000 }))
        .collect();
    Ok(Json(json!({ "versions": versions })).into_response())
}

// ---- for homelabd ----

/// App and environment, from the query. Both empty means the platform.
fn place(q: &HashMap<String, String>) -> (String, String) {
    (
        q.get("app").cloned().unwrap_or_default(),
        q.get("env").cloned().unwrap_or_default(),
    )
}

fn clean_by(by: &str) -> Result<String, ApiError> {
    if by.chars().count() > 200 || by.chars().any(char::is_control) {
        return Err(err(StatusCode::BAD_REQUEST, "That isn't a valid name."));
    }
    Ok(by.to_string())
}

fn sys_info_json(i: &Info) -> Value {
    let mut v = info_json(i);
    v["updated_by"] = json!(i.updated_by);
    v
}

/// GET /internal/secrets?app=&env=&kind=
async fn sys_list(
    State(s): State<AppState>,
    caller: Caller,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let (app, env) = place(&q);
    let kind = match q.get("kind").map(String::as_str) {
        None | Some("") => None,
        Some(k) => Some(
            SecretKind::parse(k)
                .ok_or_else(|| err(StatusCode::BAD_REQUEST, "The kind is config or app."))?,
        ),
    };
    let list = blocking(store, move |st| st.list(&app, &env, kind)).await?;
    let secrets: Vec<Value> = list.iter().map(sys_info_json).collect();
    Ok(Json(json!({ "secrets": secrets })).into_response())
}

/// GET /internal/secrets/item/{name}?app=&env=&version=
async fn sys_get(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let (app, env) = place(&q);
    let version = parse_version(&q)?;
    let key = name.clone();
    let found = blocking(store, move |st| st.get(&app, &env, &key, version))
        .await?
        .ok_or_else(|| err(StatusCode::NOT_FOUND, "There's no such secret."))?;
    Ok(Json(json!({
        "name": name,
        "kind": found.kind.as_str(),
        "value": found.value.as_str(),
        "version": found.version,
    }))
    .into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SysPutBody {
    value: String,
    #[serde(default)]
    kind: Option<String>,
    #[serde(default)]
    by: String,
}

/// PUT /internal/secrets/item/{name}?app=&env=
async fn sys_put(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    Query(q): Query<HashMap<String, String>>,
    body: Bytes,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let (app, env) = place(&q);
    let body: SysPutBody = parse_body(&body)?;
    let kind = match body.kind.as_deref() {
        None | Some("") => SecretKind::Config,
        Some(k) => SecretKind::parse(k)
            .ok_or_else(|| err(StatusCode::BAD_REQUEST, "The kind is config or app."))?,
    };
    let by = clean_by(&body.by)?;
    let key = name.clone();
    let version = blocking(store, move |st| {
        st.put(&app, &env, &key, kind, &body.value, &by, now_ms())
    })
    .await?;
    Ok(Json(json!({ "name": name, "version": version })).into_response())
}

/// DELETE /internal/secrets/item/{name}?app=&env=[&kind=]
async fn sys_delete(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let (app, env) = place(&q);
    let only = match q.get("kind").map(String::as_str) {
        None | Some("") => None,
        Some(k) => Some(
            SecretKind::parse(k)
                .ok_or_else(|| err(StatusCode::BAD_REQUEST, "The kind is config or app."))?,
        ),
    };
    blocking(store, move |st| st.delete(&app, &env, &name, only)).await?;
    Ok(StatusCode::NO_CONTENT.into_response())
}

/// GET /internal/secrets/item/{name}/versions?app=&env=
async fn sys_versions(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    Query(q): Query<HashMap<String, String>>,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let (app, env) = place(&q);
    let list = blocking(store, move |st| st.versions(&app, &env, &name)).await?;
    let versions: Vec<Value> = list
        .iter()
        .map(|v| json!({ "version": v.version, "set_at": v.set_at / 1000, "set_by": v.set_by }))
        .collect();
    Ok(Json(json!({ "versions": versions })).into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RestoreBody {
    version: i64,
    #[serde(default)]
    by: String,
}

/// POST /internal/secrets/item/{name}/restore?app=&env=
async fn sys_restore(
    State(s): State<AppState>,
    caller: Caller,
    Path(name): Path<String>,
    Query(q): Query<HashMap<String, String>>,
    body: Bytes,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let (app, env) = place(&q);
    let body: RestoreBody = parse_body(&body)?;
    let by = clean_by(&body.by)?;
    let version = blocking(store, move |st| {
        st.restore(&app, &env, &name, body.version, &by, now_ms())
    })
    .await?;
    Ok(Json(json!({ "version": version })).into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct NamesBody {
    #[serde(default)]
    app: String,
    #[serde(default)]
    env: String,
    names: Vec<String>,
}

/// POST /internal/secrets/missing: which of the names have no config value.
async fn sys_missing(State(s): State<AppState>, caller: Caller, body: Bytes) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let body: NamesBody = parse_body(&body)?;
    let missing = blocking(store, move |st| {
        st.missing(&body.app, &body.env, &body.names)
    })
    .await?;
    Ok(Json(json!({ "missing": missing })).into_response())
}

/// POST /internal/secrets/values: the config values for a deploy, for the names that are
/// set. App secrets are never given out here.
async fn sys_values(State(s): State<AppState>, caller: Caller, body: Bytes) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let body: NamesBody = parse_body(&body)?;
    let found = blocking(store, move |st| {
        st.values_for_deploy(&body.app, &body.env, &body.names)
    })
    .await?;
    let values: serde_json::Map<String, Value> = found
        .iter()
        .map(|(k, v)| (k.clone(), json!(v.as_str())))
        .collect();
    Ok(Json(json!({ "values": values })).into_response())
}

/// DELETE /internal/secrets/app/{app}: every secret of an app (an app being removed).
async fn sys_delete_app(
    State(s): State<AppState>,
    caller: Caller,
    Path(app): Path<String>,
) -> Reply {
    system_caller(&caller)?;
    let store = store(&s, &caller)?;
    let n = blocking(store, move |st| st.delete_app(&app)).await?;
    Ok(Json(json!({ "deleted": n })).into_response())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::secrets::Kind as SK;
    use crate::testutil::*;
    use axum::http::HeaderMap;
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    async fn call(
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
        let req = request(who, method, target, bytes, &[]);
        let res = env.router.clone().oneshot(req).await.unwrap();
        let (parts, body) = res.into_parts();
        let bytes = body.collect().await.unwrap().to_bytes();
        let _: &HeaderMap = &parts.headers;
        (
            parts.status,
            serde_json::from_slice(&bytes).unwrap_or(Value::Null),
        )
    }

    fn rw(app: &str, env: &str) -> Value {
        app_scoped(app, env, &[SCOPE_READ, SCOPE_WRITE])
    }

    fn ro(app: &str, env: &str) -> Value {
        app_scoped(app, env, &[SCOPE_READ])
    }

    fn wo(app: &str, env: &str) -> Value {
        app_scoped(app, env, &[SCOPE_WRITE])
    }

    /// An environment with some secrets of several apps, environments and kinds.
    fn seeded() -> TestEnv {
        let env = test_env(1024);
        let s = &env.secrets;
        let put = |app, e, name, kind, value| {
            s.put(app, e, name, kind, value, "seed", 1_800_000_000_000)
                .unwrap();
        };
        put("movies", "prod", "TMDB_API_KEY", SK::Config, "tmdb-prod");
        put(
            "movies",
            "prod",
            "HOMELAB_TOKEN",
            SK::Config,
            "hlk_movies_prod",
        );
        put("movies", "prod", "google_token:u1", SK::App, "tok-u1");
        put("movies", "dev", "TMDB_API_KEY", SK::Config, "tmdb-dev");
        put("notes", "prod", "DB_URL", SK::Config, "db-url-notes");
        put("notes", "prod", "saved", SK::App, "notes-saved");
        put("", "", "GITHUB_TOKEN", SK::Config, "ghp_platform");
        env
    }

    // ---- who may use which route ----

    /// Every app route, as (method, path, body).
    fn app_routes() -> Vec<(&'static str, &'static str, Value)> {
        vec![
            ("GET", "/api/secrets", Value::Null),
            ("GET", "/api/secrets/TMDB_API_KEY", Value::Null),
            ("GET", "/api/secrets/TMDB_API_KEY?version=1", Value::Null),
            ("PUT", "/api/secrets/new", json!({"value": "v"})),
            ("DELETE", "/api/secrets/google_token:u1", Value::Null),
            ("GET", "/api/secrets/TMDB_API_KEY/versions", Value::Null),
            // Paths that are not routes, and wrong methods.
            ("GET", "/api/secrets/a/b/c", Value::Null),
            ("POST", "/api/secrets", json!({})),
            ("POST", "/api/secrets/x", json!({})),
            ("GET", "/api/secrets/x/restore", Value::Null),
        ]
    }

    /// Every route for homelabd.
    fn internal_routes() -> Vec<(&'static str, &'static str, Value)> {
        let v = json!({"value": "v"});
        vec![
            ("GET", "/internal/secrets?app=movies&env=prod", Value::Null),
            (
                "GET",
                "/internal/secrets/item/TMDB_API_KEY?app=movies&env=prod",
                Value::Null,
            ),
            ("PUT", "/internal/secrets/item/NEW?app=movies&env=prod", v),
            (
                "DELETE",
                "/internal/secrets/item/TMDB_API_KEY?app=movies&env=prod",
                Value::Null,
            ),
            (
                "GET",
                "/internal/secrets/item/TMDB_API_KEY/versions?app=movies&env=prod",
                Value::Null,
            ),
            (
                "POST",
                "/internal/secrets/item/TMDB_API_KEY/restore?app=movies&env=prod",
                json!({"version": 1}),
            ),
            (
                "POST",
                "/internal/secrets/missing",
                json!({"app": "movies", "env": "prod", "names": ["A"]}),
            ),
            (
                "POST",
                "/internal/secrets/values",
                json!({"app": "movies", "env": "prod", "names": ["TMDB_API_KEY"]}),
            ),
            ("DELETE", "/internal/secrets/app/movies", Value::Null),
            ("GET", "/internal/secrets/nothing/here", Value::Null),
        ]
    }

    #[tokio::test]
    async fn people_and_admin_keys_get_403_on_every_secrets_route() {
        let env = seeded();
        let mut callers = vec![
            ("person", person()),
            ("admin without scopes", admin(&[])),
            (
                "admin with every scope",
                admin(&["files:read", "files:write", "users:read"]),
            ),
            (
                "admin with the secrets scopes",
                admin(&[SCOPE_READ, SCOPE_WRITE]),
            ),
        ];
        // Even a person or admin who claims an app and the scopes of one.
        let mut sneaky = person();
        sneaky["app"] = "movies".into();
        sneaky["env"] = "prod".into();
        sneaky["scopes"] = json!([SCOPE_READ, SCOPE_WRITE]);
        callers.push(("person naming an app", sneaky));
        let mut sneaky = admin(&[SCOPE_READ, SCOPE_WRITE]);
        sneaky["app"] = "movies".into();
        sneaky["env"] = "prod".into();
        callers.push(("admin naming an app", sneaky));

        for (who, caller) in &callers {
            for (method, path, body) in app_routes().into_iter().chain(internal_routes()) {
                let (status, out) = call(&env, caller, method, path, body).await;
                assert_eq!(status, StatusCode::FORBIDDEN, "{who}: {method} {path}");
                let text = out.to_string();
                for leaked in ["tmdb-prod", "tok-u1", "ghp_platform", "db-url-notes"] {
                    assert!(!text.contains(leaked), "{who}: {path} leaked a value");
                }
            }
        }
        // Nothing was changed.
        assert_eq!(env.secrets.list("movies", "prod", None).unwrap().len(), 3);
    }

    #[tokio::test]
    async fn people_and_admin_keys_get_403_even_when_the_store_is_off() {
        // The kind check comes before anything else, so 403 doesn't depend on the store.
        let env = seeded();
        let state = AppState {
            key: Arc::new(test_key().verifying_key()),
            version: "test",
            files: env.files.clone(),
            secrets: None,
            secrets_problem: "broken".into(),
        };
        let router = crate::app::app(state);
        for caller in [person(), admin(&[SCOPE_READ])] {
            let req = request(&caller, "GET", "/api/secrets", vec![], &[]);
            let res = router.clone().oneshot(req).await.unwrap();
            assert_eq!(res.status(), StatusCode::FORBIDDEN);
        }
        // The files service still works without secrets.
        let req = request(&person(), "GET", "/api/files/folders", vec![], &[]);
        let res = router.clone().oneshot(req).await.unwrap();
        assert_eq!(res.status(), StatusCode::OK);
        // An app is told, briefly, and homelabd is told why.
        let req = request(&ro("movies", "prod"), "GET", "/api/secrets", vec![], &[]);
        let res = router.clone().oneshot(req).await.unwrap();
        assert_eq!(res.status(), StatusCode::SERVICE_UNAVAILABLE);
        let body = res.into_body().collect().await.unwrap().to_bytes();
        assert!(!String::from_utf8_lossy(&body).contains("broken"));
        let req = request(&system(), "GET", "/internal/secrets", vec![], &[]);
        let res = router.clone().oneshot(req).await.unwrap();
        assert_eq!(res.status(), StatusCode::SERVICE_UNAVAILABLE);
        let body = res.into_body().collect().await.unwrap().to_bytes();
        assert!(String::from_utf8_lossy(&body).contains("broken"));
    }

    #[tokio::test]
    async fn apps_cannot_use_the_internal_routes() {
        let env = seeded();
        for (method, path, body) in internal_routes() {
            let (status, _) = call(&env, &rw("movies", "prod"), method, path, body).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{method} {path}");
        }
        assert!(env
            .secrets
            .get("movies", "prod", "TMDB_API_KEY", None)
            .unwrap()
            .is_some());
    }

    #[tokio::test]
    async fn the_system_caller_uses_the_internal_routes_not_the_app_routes() {
        let env = seeded();
        for (method, path, body) in app_routes().into_iter().take(6) {
            let (status, _) = call(&env, &system(), method, path, body).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{method} {path}");
        }
    }

    #[tokio::test]
    async fn an_app_without_a_proper_place_is_refused() {
        let env = seeded();
        for who in [
            app_scoped("", "", &[SCOPE_READ, SCOPE_WRITE]),
            app_scoped("movies", "", &[SCOPE_READ]),
            app_scoped("movies", "staging", &[SCOPE_READ]),
        ] {
            let (status, _) = call(&env, &who, "GET", "/api/secrets", Value::Null).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{who}");
        }
    }

    // ---- scopes ----

    #[tokio::test]
    async fn reading_needs_secrets_read_and_changing_needs_secrets_write() {
        let env = seeded();
        let reads = [
            ("GET", "/api/secrets"),
            ("GET", "/api/secrets/TMDB_API_KEY"),
            ("GET", "/api/secrets/TMDB_API_KEY/versions"),
        ];
        for who in [
            app_scoped("movies", "prod", &[]),
            wo("movies", "prod"),
            app_scoped("movies", "prod", &["files:read", "users:read"]),
        ] {
            for (method, path) in reads {
                let (status, _) = call(&env, &who, method, path, Value::Null).await;
                assert_eq!(status, StatusCode::FORBIDDEN, "{who} {path}");
            }
        }
        for who in [app_scoped("movies", "prod", &[]), ro("movies", "prod")] {
            let (status, _) =
                call(&env, &who, "PUT", "/api/secrets/new", json!({"value": "v"})).await;
            assert_eq!(status, StatusCode::FORBIDDEN);
            let (status, _) = call(
                &env,
                &who,
                "DELETE",
                "/api/secrets/google_token:u1",
                Value::Null,
            )
            .await;
            assert_eq!(status, StatusCode::FORBIDDEN);
        }
        assert!(env
            .secrets
            .get("movies", "prod", "new", None)
            .unwrap()
            .is_none());
        assert!(env
            .secrets
            .get("movies", "prod", "google_token:u1", None)
            .unwrap()
            .is_some());
        // Write alone may write; read alone may read.
        let (status, _) = call(
            &env,
            &wo("movies", "prod"),
            "PUT",
            "/api/secrets/new",
            json!({"value": "v"}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let (status, _) = call(
            &env,
            &ro("movies", "prod"),
            "GET",
            "/api/secrets/new",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::OK);
    }

    // ---- the app's own place ----

    #[tokio::test]
    async fn an_app_sees_only_its_own_app_and_environment() {
        let env = seeded();
        let who = rw("movies", "prod");
        let (status, out) = call(&env, &who, "GET", "/api/secrets", Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        let names: Vec<&str> = out["secrets"]
            .as_array()
            .unwrap()
            .iter()
            .map(|s| s["name"].as_str().unwrap())
            .collect();
        // HOMELAB_TOKEN is Homelab's own and never listed.
        assert_eq!(names, vec!["TMDB_API_KEY", "google_token:u1"]);
        let kinds: Vec<&str> = out["secrets"]
            .as_array()
            .unwrap()
            .iter()
            .map(|s| s["kind"].as_str().unwrap())
            .collect();
        assert_eq!(kinds, vec!["config", "app"]);
        assert_eq!(out["secrets"][0]["version"], 1);
        assert_eq!(out["secrets"][0]["updated_at"], 1_800_000_000);
        for s in out["secrets"].as_array().unwrap() {
            assert!(s.get("value").is_none());
        }

        // Another environment of the same app, and another app, are separate.
        let (_, out) = call(
            &env,
            &rw("movies", "dev"),
            "GET",
            "/api/secrets",
            Value::Null,
        )
        .await;
        assert_eq!(out["secrets"].as_array().unwrap().len(), 1);
        let (status, out) = call(&env, &who, "GET", "/api/secrets/DB_URL", Value::Null).await;
        assert_eq!(
            (status, out["error"].is_string()),
            (StatusCode::NOT_FOUND, true)
        );
        let (status, _) = call(&env, &who, "GET", "/api/secrets/saved", Value::Null).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        let (status, _) = call(
            &env,
            &rw("notes", "prod"),
            "GET",
            "/api/secrets/google_token:u1",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        let (status, _) = call(
            &env,
            &rw("notes", "prod"),
            "GET",
            "/api/secrets/google_token:u1/versions",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        // The app can't name another place by query: the place comes from the signed caller.
        let (status, out) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/DB_URL?app=notes&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND, "{out}");
    }

    #[tokio::test]
    async fn an_app_writes_and_deletes_only_its_own_place() {
        let env = seeded();
        let (status, out) = call(
            &env,
            &rw("movies", "dev"),
            "PUT",
            "/api/secrets/token",
            json!({"value": "dev-token"}),
        )
        .await;
        assert_eq!((status, out["version"].clone()), (StatusCode::OK, json!(1)));
        // It went to movies/dev and nowhere else.
        let get = |app: &str, e: &str| {
            env.secrets
                .get(app, e, "token", None)
                .unwrap()
                .map(|o| o.value.to_string())
        };
        assert_eq!(get("movies", "dev").as_deref(), Some("dev-token"));
        assert_eq!(get("movies", "prod"), None);
        assert_eq!(get("notes", "dev"), None);
        let by = env
            .secrets
            .info("movies", "dev", "token")
            .unwrap()
            .unwrap()
            .updated_by;
        assert_eq!(by, "app:movies/dev");
        // Deleting what another app has does nothing to it.
        let (status, _) = call(
            &env,
            &rw("movies", "prod"),
            "DELETE",
            "/api/secrets/saved",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert_eq!(
            env.secrets
                .get("notes", "prod", "saved", None)
                .unwrap()
                .unwrap()
                .value
                .as_str(),
            "notes-saved"
        );
        // Deleting its own works, and again is fine.
        for _ in 0..2 {
            let (status, _) = call(
                &env,
                &rw("movies", "prod"),
                "DELETE",
                "/api/secrets/google_token:u1",
                Value::Null,
            )
            .await;
            assert_eq!(status, StatusCode::NO_CONTENT);
        }
        assert!(env
            .secrets
            .get("movies", "prod", "google_token:u1", None)
            .unwrap()
            .is_none());
    }

    // ---- config secrets and platform secrets ----

    #[tokio::test]
    async fn an_app_reads_its_config_secrets_but_cannot_change_them() {
        let env = seeded();
        let who = rw("movies", "prod");
        let (status, out) = call(&env, &who, "GET", "/api/secrets/TMDB_API_KEY", Value::Null).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            out,
            json!({"name": "TMDB_API_KEY", "value": "tmdb-prod", "version": 1})
        );
        let (status, out) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/TMDB_API_KEY/versions",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(out["versions"][0]["version"], 1);
        assert!(out["versions"][0].get("value").is_none());

        let (status, _) = call(
            &env,
            &who,
            "PUT",
            "/api/secrets/TMDB_API_KEY",
            json!({"value": "mine"}),
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        let (status, _) = call(
            &env,
            &who,
            "DELETE",
            "/api/secrets/TMDB_API_KEY",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        let o = env
            .secrets
            .get("movies", "prod", "TMDB_API_KEY", None)
            .unwrap()
            .unwrap();
        assert_eq!(
            (o.kind, o.version, o.value.as_str()),
            (SK::Config, 1, "tmdb-prod")
        );
    }

    #[tokio::test]
    async fn an_app_never_sees_platform_secrets_or_homelabs_own_names() {
        let env = seeded();
        let who = rw("movies", "prod");
        let (_, out) = call(&env, &who, "GET", "/api/secrets", Value::Null).await;
        assert!(!out.to_string().contains("GITHUB_TOKEN"));
        let (status, out) = call(&env, &who, "GET", "/api/secrets/GITHUB_TOKEN", Value::Null).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        assert!(!out.to_string().contains("ghp_platform"));
        // Writing a name that is only a platform secret makes the app's own secret instead.
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            "/api/secrets/GITHUB_TOKEN",
            json!({"value": "mine"}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            env.secrets
                .get("", "", "GITHUB_TOKEN", None)
                .unwrap()
                .unwrap()
                .value
                .as_str(),
            "ghp_platform"
        );
        let (status, _) = call(
            &env,
            &who,
            "DELETE",
            "/api/secrets/GITHUB_TOKEN",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert_eq!(
            env.secrets
                .get("", "", "GITHUB_TOKEN", None)
                .unwrap()
                .unwrap()
                .value
                .as_str(),
            "ghp_platform"
        );
        // HOMELAB_ names: not read, not written, not deleted.
        for (method, path, body, want) in [
            (
                "GET",
                "/api/secrets/HOMELAB_TOKEN",
                Value::Null,
                StatusCode::NOT_FOUND,
            ),
            (
                "GET",
                "/api/secrets/HOMELAB_TOKEN/versions",
                Value::Null,
                StatusCode::NOT_FOUND,
            ),
            (
                "PUT",
                "/api/secrets/HOMELAB_TOKEN",
                json!({"value": "x"}),
                StatusCode::BAD_REQUEST,
            ),
            (
                "PUT",
                "/api/secrets/HOMELAB_NEW",
                json!({"value": "x"}),
                StatusCode::BAD_REQUEST,
            ),
            (
                "DELETE",
                "/api/secrets/HOMELAB_TOKEN",
                Value::Null,
                StatusCode::FORBIDDEN,
            ),
        ] {
            let (status, out) = call(&env, &who, method, path, body).await;
            assert_eq!(status, want, "{method} {path}");
            assert!(!out.to_string().contains("hlk_movies_prod"));
        }
        assert_eq!(
            env.secrets
                .get("movies", "prod", "HOMELAB_TOKEN", None)
                .unwrap()
                .unwrap()
                .value
                .as_str(),
            "hlk_movies_prod"
        );
    }

    // ---- the calls ----

    #[tokio::test]
    async fn put_get_versions_and_delete_work_end_to_end() {
        let env = seeded();
        let who = rw("movies", "prod");
        for i in 1..=7 {
            let (status, out) = call(
                &env,
                &who,
                "PUT",
                "/api/secrets/google_token:u2",
                json!({"value": format!("t{i}")}),
            )
            .await;
            assert_eq!(status, StatusCode::OK);
            assert_eq!(out, json!({"name": "google_token:u2", "version": i}));
        }
        let (_, out) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/google_token:u2",
            Value::Null,
        )
        .await;
        assert_eq!(
            out,
            json!({"name": "google_token:u2", "value": "t7", "version": 7})
        );
        let (_, out) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/google_token:u2?version=4",
            Value::Null,
        )
        .await;
        assert_eq!(
            out,
            json!({"name": "google_token:u2", "value": "t4", "version": 4})
        );
        let (status, _) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/google_token:u2?version=2",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        for bad in ["0", "-1", "x", ""] {
            let (status, _) = call(
                &env,
                &who,
                "GET",
                &format!("/api/secrets/google_token:u2?version={bad}"),
                Value::Null,
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{bad}");
        }
        let (_, out) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/google_token:u2/versions",
            Value::Null,
        )
        .await;
        let numbers: Vec<i64> = out["versions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v["version"].as_i64().unwrap())
            .collect();
        assert_eq!(numbers, vec![7, 6, 5, 4, 3]);
        let (status, _) = call(
            &env,
            &who,
            "DELETE",
            "/api/secrets/google_token:u2",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        let (status, _) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/google_token:u2",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        let (status, _) = call(
            &env,
            &who,
            "GET",
            "/api/secrets/google_token:u2/versions",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn names_values_and_limits_are_checked() {
        let env = seeded();
        let who = rw("movies", "prod");
        for bad in ["a%20b", "a%2Fb", "%2E%2E", "a*b"] {
            let (status, _) = call(
                &env,
                &who,
                "PUT",
                &format!("/api/secrets/{bad}"),
                json!({"value": "v"}),
            )
            .await;
            assert!(
                status == StatusCode::BAD_REQUEST || status == StatusCode::NOT_FOUND,
                "{bad}: {status}"
            );
        }
        let long = "n".repeat(201);
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            &format!("/api/secrets/{long}"),
            json!({"value": "v"}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let ok = "n".repeat(200);
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            &format!("/api/secrets/{ok}"),
            json!({"value": "v"}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        // Values: 64 KiB is fine (even when JSON escapes make the body bigger), more is not.
        let big = "q".repeat(crate::secrets::MAX_APP_VALUE);
        let (status, _) = call(&env, &who, "PUT", "/api/secrets/big", json!({"value": big})).await;
        assert_eq!(status, StatusCode::OK);
        let (_, out) = call(&env, &who, "GET", "/api/secrets/big", Value::Null).await;
        assert_eq!(
            out["value"].as_str().unwrap().len(),
            crate::secrets::MAX_APP_VALUE
        );
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            "/api/secrets/big",
            json!({"value": "q".repeat(crate::secrets::MAX_APP_VALUE + 1)}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        let escaped = "\n".repeat(crate::secrets::MAX_APP_VALUE);
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            "/api/secrets/lines",
            json!({"value": escaped}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            "/api/secrets/empty",
            json!({"value": ""}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        // Bodies: missing value, extra fields, not JSON, wrong type.
        for body in [
            json!({}),
            json!({"value": "v", "kind": "config"}),
            json!({"value": 5}),
            json!([1]),
        ] {
            let (status, _) = call(&env, &who, "PUT", "/api/secrets/x", body).await;
            assert_eq!(status, StatusCode::BAD_REQUEST);
        }
        let req = request(&who, "PUT", "/api/secrets/x", b"not json".to_vec(), &[]);
        let res = env.router.clone().oneshot(req).await.unwrap();
        assert_eq!(res.status(), StatusCode::BAD_REQUEST);
        // The app can't make a config secret by saying so.
        let (status, _) = call(
            &env,
            &who,
            "PUT",
            "/api/secrets/x",
            json!({"value": "v", "kind": "config"}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn error_messages_never_contain_a_value() {
        let env = seeded();
        let who = rw("movies", "prod");
        let secret = "do-not-print-this-value";
        let mut seen = String::new();
        for (method, path, body) in [
            ("PUT", "/api/secrets/TMDB_API_KEY", json!({"value": secret})),
            ("PUT", "/api/secrets/HOMELAB_X", json!({"value": secret})),
            ("PUT", "/api/secrets/bad name", json!({"value": secret})),
            (
                "PUT",
                "/api/secrets/x",
                json!({"value": secret, "extra": 1}),
            ),
        ] {
            let target = path.replace(' ', "%20");
            let (_, out) = call(&env, &who, method, &target, body).await;
            seen.push_str(&out.to_string());
        }
        assert!(!seen.contains(secret));
    }

    #[tokio::test]
    async fn an_unsigned_or_unknown_route_is_still_refused_as_usual() {
        let env = seeded();
        let req = axum::http::Request::builder()
            .uri("/api/secrets")
            .body(axum::body::Body::empty())
            .unwrap();
        let res = env.router.clone().oneshot(req).await.unwrap();
        assert_eq!(res.status(), StatusCode::UNAUTHORIZED);
        let (status, _) = call(
            &env,
            &rw("movies", "prod"),
            "GET",
            "/api/secretsx",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    // ---- homelabd's routes ----

    #[tokio::test]
    async fn the_system_caller_can_do_everything_on_any_place_and_kind() {
        let env = seeded();
        let s = system();
        // List, with and without a kind.
        let (status, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets?app=movies&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(out["secrets"].as_array().unwrap().len(), 3);
        assert_eq!(out["secrets"][0]["updated_by"], "seed");
        let (_, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets?app=movies&env=prod&kind=app",
            Value::Null,
        )
        .await;
        assert_eq!(out["secrets"].as_array().unwrap().len(), 1);
        let (_, out) = call(&env, &s, "GET", "/internal/secrets", Value::Null).await;
        assert_eq!(out["secrets"][0]["name"], "GITHUB_TOKEN");
        let (status, _) = call(&env, &s, "GET", "/internal/secrets?kind=other", Value::Null).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        // Get: any kind, platform too.
        let (_, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets/item/GITHUB_TOKEN",
            Value::Null,
        )
        .await;
        assert_eq!(
            out,
            json!({"name": "GITHUB_TOKEN", "kind": "config", "value": "ghp_platform", "version": 1})
        );
        let (_, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets/item/google_token:u1?app=movies&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(out["kind"], "app");
        assert_eq!(out["value"], "tok-u1");
        let (_, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets/item/HOMELAB_TOKEN?app=movies&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(out["value"], "hlk_movies_prod");
        let (status, _) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets/item/NOPE?app=movies&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        // Put (config by default, HOMELAB_ names allowed), by recorded.
        let (status, out) = call(
            &env,
            &s,
            "PUT",
            "/internal/secrets/item/HOMELAB_TOKEN?app=notes&env=dev",
            json!({"value": "hlk_x", "by": "homelab"}),
        )
        .await;
        assert_eq!((status, out["version"].clone()), (StatusCode::OK, json!(1)));
        assert_eq!(
            env.secrets
                .info("notes", "dev", "HOMELAB_TOKEN")
                .unwrap()
                .unwrap()
                .updated_by,
            "homelab"
        );
        let (status, _) = call(
            &env,
            &s,
            "PUT",
            "/internal/secrets/item/NEW?app=notes&env=dev",
            json!({"value": "v", "kind": "app"}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            env.secrets
                .info("notes", "dev", "NEW")
                .unwrap()
                .unwrap()
                .kind,
            SK::App
        );
        let (status, _) = call(
            &env,
            &s,
            "PUT",
            "/internal/secrets/item/PLAT?app=&env=",
            json!({"value": "v"}),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        // Bad place, kind, by.
        for (path, body) in [
            (
                "/internal/secrets/item/X?app=notes&env=staging",
                json!({"value": "v"}),
            ),
            ("/internal/secrets/item/X?app=notes", json!({"value": "v"})),
            ("/internal/secrets/item/X?env=prod", json!({"value": "v"})),
            (
                "/internal/secrets/item/X?app=notes&env=dev",
                json!({"value": "v", "kind": "x"}),
            ),
            (
                "/internal/secrets/item/X?app=notes&env=dev",
                json!({"value": "v", "by": "a\nb"}),
            ),
            (
                "/internal/secrets/item/X?app=notes&env=dev",
                json!({"value": "a\nb"}),
            ),
            (
                "/internal/secrets/item/1X?app=notes&env=dev",
                json!({"value": "v"}),
            ),
        ] {
            let (status, _) = call(&env, &s, "PUT", path, body.clone()).await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{path} {body}");
        }
        // A name taken by the other kind.
        let (status, _) = call(
            &env,
            &s,
            "PUT",
            "/internal/secrets/item/saved?app=notes&env=prod",
            json!({"value": "v"}),
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT);
        // Versions, restore, delete.
        call(
            &env,
            &s,
            "PUT",
            "/internal/secrets/item/DB_URL?app=notes&env=prod",
            json!({"value": "second", "by": "owner"}),
        )
        .await;
        let (_, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets/item/DB_URL/versions?app=notes&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(out["versions"][0]["version"], 2);
        assert_eq!(out["versions"][0]["set_by"], "owner");
        assert!(out.to_string().contains("seed"));
        let (status, out) = call(
            &env,
            &s,
            "POST",
            "/internal/secrets/item/DB_URL/restore?app=notes&env=prod",
            json!({"version": 1, "by": "owner"}),
        )
        .await;
        assert_eq!((status, out["version"].clone()), (StatusCode::OK, json!(3)));
        let (_, out) = call(
            &env,
            &s,
            "GET",
            "/internal/secrets/item/DB_URL?app=notes&env=prod",
            Value::Null,
        )
        .await;
        assert_eq!(out["value"], "db-url-notes");
        let (status, _) = call(
            &env,
            &s,
            "POST",
            "/internal/secrets/item/DB_URL/restore?app=notes&env=prod",
            json!({"version": 99}),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        for (path, want) in [
            ("/internal/secrets/item/DB_URL?app=notes&env=prod", 1),
            ("/internal/secrets/item/saved?app=notes&env=prod", 1),
        ] {
            let (status, _) = call(&env, &s, "DELETE", path, Value::Null).await;
            assert_eq!(status, StatusCode::NO_CONTENT);
            let _ = want;
        }
        assert!(env
            .secrets
            .get("notes", "prod", "DB_URL", None)
            .unwrap()
            .is_none());
        assert!(env
            .secrets
            .get("notes", "prod", "saved", None)
            .unwrap()
            .is_none());
        // Delete with a kind only removes that kind.
        call(
            &env,
            &s,
            "PUT",
            "/internal/secrets/item/saved2?app=notes&env=dev",
            json!({"value": "v", "kind": "app"}),
        )
        .await;
        let (status, _) = call(
            &env,
            &s,
            "DELETE",
            "/internal/secrets/item/saved2?app=notes&env=dev&kind=config",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert!(env
            .secrets
            .get("notes", "dev", "saved2", None)
            .unwrap()
            .is_some());
        let (status, _) = call(
            &env,
            &s,
            "DELETE",
            "/internal/secrets/item/saved2?app=notes&env=dev&kind=app",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert!(env
            .secrets
            .get("notes", "dev", "saved2", None)
            .unwrap()
            .is_none());
        let (status, _) = call(
            &env,
            &s,
            "DELETE",
            "/internal/secrets/item/x?kind=zzz",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        // Delete a platform secret.
        let (status, _) = call(
            &env,
            &s,
            "DELETE",
            "/internal/secrets/item/GITHUB_TOKEN",
            Value::Null,
        )
        .await;
        assert_eq!(status, StatusCode::NO_CONTENT);
        assert!(env
            .secrets
            .get("", "", "GITHUB_TOKEN", None)
            .unwrap()
            .is_none());
    }

    #[tokio::test]
    async fn missing_values_for_deploy_and_delete_app() {
        let env = seeded();
        let s = system();
        let (status, out) = call(&env, &s, "POST", "/internal/secrets/missing", json!({"app": "movies", "env": "prod", "names": ["TMDB_API_KEY", "OTHER", "google_token:u1"]})).await;
        assert_eq!(status, StatusCode::OK);
        // An app secret doesn't count as a config value that is set.
        assert_eq!(out["missing"], json!(["OTHER", "google_token:u1"]));
        let (_, out) = call(&env, &s, "POST", "/internal/secrets/values", json!({"app": "movies", "env": "prod", "names": ["TMDB_API_KEY", "OTHER", "google_token:u1", "HOMELAB_TOKEN"]})).await;
        assert_eq!(
            out["values"],
            json!({"TMDB_API_KEY": "tmdb-prod", "HOMELAB_TOKEN": "hlk_movies_prod"})
        );
        let (_, out) = call(
            &env,
            &s,
            "POST",
            "/internal/secrets/values",
            json!({"names": ["GITHUB_TOKEN"]}),
        )
        .await;
        assert_eq!(out["values"], json!({"GITHUB_TOKEN": "ghp_platform"}));
        for body in [
            json!({"names": "x"}),
            json!({"app": "movies", "env": "prod"}),
            json!({"names": [], "extra": 1}),
        ] {
            let (status, _) = call(&env, &s, "POST", "/internal/secrets/values", body).await;
            assert_eq!(status, StatusCode::BAD_REQUEST);
        }
        let (status, out) = call(
            &env,
            &s,
            "DELETE",
            "/internal/secrets/app/movies",
            Value::Null,
        )
        .await;
        assert_eq!((status, out["deleted"].clone()), (StatusCode::OK, json!(4)));
        assert!(env.secrets.list("movies", "prod", None).unwrap().is_empty());
        assert_eq!(env.secrets.list("notes", "prod", None).unwrap().len(), 2);
        assert_eq!(env.secrets.list("", "", None).unwrap().len(), 1);
    }

    #[test]
    fn secrets_paths_are_recognised() {
        for p in [
            "/api/secrets",
            "/api/secrets/x",
            "/api/secrets/x/versions",
            "/internal/secrets",
            "/internal/secrets/item/x",
        ] {
            assert!(is_secrets_path(p), "{p}");
        }
        for p in [
            "/api/secretsx",
            "/api/files/secrets",
            "/internal/secretsx",
            "/api",
            "/healthz",
            "/api/secret",
        ] {
            assert!(!is_secrets_path(p), "{p}");
        }
    }
}
