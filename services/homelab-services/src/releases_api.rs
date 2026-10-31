//! The release routes.
//!
//! * `/internal/releases/...` is for homelabd only (the `system` caller). It publishes (or
//!   plans) the version of a commit and lists a repo's releases. homelabd sends the GitHub
//!   token in the request body; it is used for the GitHub calls and never logged, stored or
//!   returned.
//! * `GET /api/releases` is for apps. An app lists its own releases with its own key and the
//!   `releases:read` scope. homelabd knows the app's repo, so it forwards the call with the
//!   repo and token in two extra headers. People and admin API keys get 403.

use axum::extract::{Query, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::api::{err, ApiError, Reply};
use crate::app::{not_found, AppState};
use crate::caller::{Caller, Kind};
use crate::releases::{create, plan, summary_of_notes, Channel, Options, Repo};
use crate::releases_github::GitHub;

/// The scope an app key needs.
pub const SCOPE_READ: &str = "releases:read";

/// Headers homelabd adds to an app's call: its repo and the GitHub token.
pub const REPO_HEADER: &str = "x-homelab-repo";
pub const TOKEN_HEADER: &str = "x-homelab-github-token";

const APP_PATH: &str = "/api/releases";
const INTERNAL_PREFIX: &str = "/internal/releases";

/// How many releases a list gives unless the caller asks for fewer or more.
const DEFAULT_LIMIT: usize = 100;
const MAX_LIMIT: usize = 500;

pub fn routes() -> Router<AppState> {
    Router::new()
        .route(APP_PATH, get(app_list).fallback(not_found))
        .route(
            "/internal/releases/publish",
            post(sys_publish).fallback(not_found),
        )
        .route(
            "/internal/releases/list",
            post(sys_list).fallback(not_found),
        )
}

/// Refuses a caller who may not use the path at all, whether or not a route is there:
/// people and admin keys on the app route, and anyone but homelabd under /internal.
pub fn refuse_path(caller: &Caller, path: &str) -> Option<ApiError> {
    let internal = path == INTERNAL_PREFIX
        || path
            .strip_prefix(INTERNAL_PREFIX)
            .is_some_and(|r| r.starts_with('/'));
    let app = path == APP_PATH
        || path
            .strip_prefix(APP_PATH)
            .is_some_and(|r| r.starts_with('/'));
    if internal && caller.kind != Kind::System {
        return Some(err(
            StatusCode::FORBIDDEN,
            "Only Homelab itself can use these routes.",
        ));
    }
    if app && matches!(caller.kind, Kind::Person | Kind::Admin) {
        return Some(err(
            StatusCode::FORBIDDEN,
            "Release versions can only be read by the app itself.",
        ));
    }
    None
}

/// A GitHub failure. GitHub saying no to the token (401, 403, or 404 on a write) is 403, so
/// homelabd can tell the owner to fix the token; anything else is 424. Neither carries the
/// token: the messages come from GitHub's answers, which never have it.
fn github_error(message: String) -> ApiError {
    let refused = [" gave 401:", " gave 403:", " gave 404:"]
        .iter()
        .any(|s| message.contains(s));
    tracing::warn!("releases: {message}");
    if refused {
        err(StatusCode::FORBIDDEN, message)
    } else {
        err(StatusCode::FAILED_DEPENDENCY, message)
    }
}

fn repo_of(text: &str) -> Result<Repo, ApiError> {
    Repo::parse(text).map_err(|m| err(StatusCode::BAD_REQUEST, m))
}

fn client(s: &AppState, token: &str) -> Result<GitHub, ApiError> {
    if token.trim().is_empty() {
        return Err(err(StatusCode::BAD_REQUEST, "There's no GitHub token."));
    }
    GitHub::new(&s.github_api, token.trim()).map_err(github_error)
}

// ---- for homelabd ----

/// Only homelabd.
fn system_caller(caller: &Caller) -> Result<(), ApiError> {
    if caller.kind != Kind::System {
        tracing::warn!(kind = ?caller.kind, "releases request refused");
        return Err(err(
            StatusCode::FORBIDDEN,
            "Only Homelab itself can use these routes.",
        ));
    }
    Ok(())
}

// No Debug: the token must never end up in a log line by accident.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PublishBody {
    repo: String,
    commit: String,
    /// "dev" or "prod".
    channel: String,
    token: String,
    /// Release even when no change counts, so every build gets a version.
    #[serde(default)]
    always: bool,
    /// Only work out the version; make nothing.
    #[serde(default)]
    dry_run: bool,
}

/// POST /internal/releases/publish: the version of a commit, made on GitHub unless `dry_run`.
async fn sys_publish(
    State(s): State<AppState>,
    caller: Caller,
    Json(body): Json<PublishBody>,
) -> Reply {
    system_caller(&caller)?;
    let repo = repo_of(&body.repo)?;
    let channel = match body.channel.as_str() {
        "dev" => Channel::Dev,
        "prod" => Channel::Prod,
        _ => return Err(err(StatusCode::BAD_REQUEST, "The channel is dev or prod.")),
    };
    let gh = client(&s, &body.token)?;
    let options = Options {
        first: None,
        always: body.always,
    };
    let planned = plan(&gh, &repo, &body.commit, &channel, &options)
        .await
        .map_err(github_error)?;
    let reply = json!({
        "version": if planned.release { planned.version.to_string() } else { String::new() },
        "released": planned.release,
        "existing": planned.existing,
        "previous": planned.previous.map(|v| v.to_string()).unwrap_or_default(),
        "summary": planned.summary,
        "notes": planned.notes,
        "commit": planned.commit,
    });
    if !body.dry_run {
        create(&gh, &repo, &channel, planned)
            .await
            .map_err(github_error)?;
    }
    Ok(Json(reply).into_response())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ListBody {
    repo: String,
    token: String,
    #[serde(default)]
    limit: Option<usize>,
}

/// POST /internal/releases/list: a repo's releases, newest first.
async fn sys_list(State(s): State<AppState>, caller: Caller, Json(body): Json<ListBody>) -> Reply {
    system_caller(&caller)?;
    let repo = repo_of(&body.repo)?;
    list(&s, &repo, &body.token, body.limit).await
}

async fn list(s: &AppState, repo: &Repo, token: &str, limit: Option<usize>) -> Reply {
    let gh = client(s, token)?;
    let limit = limit.unwrap_or(DEFAULT_LIMIT).clamp(1, MAX_LIMIT);
    let releases = gh.list_releases(repo).await.map_err(github_error)?;
    let items: Vec<Value> = releases
        .iter()
        .take(limit)
        .map(|r| {
            json!({
                "tag": r.tag,
                "prerelease": r.prerelease,
                "commit": r.commit,
                "date": r.date,
                "summary": summary_of_notes(&r.body),
                "notes": r.body,
            })
        })
        .collect();
    Ok(Json(json!({ "releases": items })).into_response())
}

// ---- for apps ----

/// GET /api/releases[?limit=n]: the app's own releases.
async fn app_list(
    State(s): State<AppState>,
    caller: Caller,
    headers: HeaderMap,
    Query(q): Query<std::collections::HashMap<String, String>>,
) -> Reply {
    if caller.kind != Kind::App
        || caller.app.is_empty()
        || !matches!(caller.env.as_str(), "dev" | "prod")
    {
        return Err(err(
            StatusCode::FORBIDDEN,
            "This key isn't an app's own key.",
        ));
    }
    if !caller.scopes.iter().any(|s| s == SCOPE_READ) {
        return Err(err(
            StatusCode::FORBIDDEN,
            format!("This key can't do that. It needs the {SCOPE_READ} scope."),
        ));
    }
    let limit = match q.get("limit") {
        None => None,
        Some(v) => match v.parse::<usize>() {
            Ok(n) if n >= 1 => Some(n),
            _ => {
                return Err(err(
                    StatusCode::BAD_REQUEST,
                    "The limit is a number from 1.",
                ))
            }
        },
    };
    let header = |name: &str| {
        headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    };
    let (repo, token) = (header(REPO_HEADER), header(TOKEN_HEADER));
    if repo.is_empty() || token.is_empty() {
        return Err(err(
            StatusCode::SERVICE_UNAVAILABLE,
            "Release versions aren't available for this app.",
        ));
    }
    list(&s, &repo_of(repo)?, token, limit).await
}
