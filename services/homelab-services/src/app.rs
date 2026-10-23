//! The HTTP routes.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use axum::extract::{Request, State};
use axum::http::header::{CACHE_CONTROL, CONTENT_TYPE};
use axum::http::{HeaderValue, Method, StatusCode};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use ed25519_dalek::VerifyingKey;
use serde_json::json;

use crate::caller::verify;
use crate::dbs::Dbs;
use crate::dbs_api;
use crate::files::{self, Files};
use crate::secrets::Secrets;
use crate::secrets_api;

/// What the handlers share.
#[derive(Clone)]
pub struct AppState {
    /// homelabd's public key.
    pub key: Arc<VerifyingKey>,
    /// Shown by /healthz.
    pub version: &'static str,
    /// The files service.
    pub files: Arc<Files>,
    /// The secrets store, or None when it could not be opened (see `secrets_problem`).
    /// The files service works without it.
    pub secrets: Option<Arc<Secrets>>,
    /// Why there is no secrets store, in a few words. Empty when there is one.
    pub secrets_problem: String,
    /// The databases service.
    pub dbs: Arc<Dbs>,
}

/// Builds the whole router. Used by `main` and by the tests.
pub fn app(state: AppState) -> Router {
    Router::new()
        // Other methods on /healthz are not special: they get the 404 below
        // (after the caller check), like any other unknown route.
        .route("/healthz", get(healthz).fallback(not_found))
        .merge(files::routes())
        .merge(secrets_api::routes())
        .merge(dbs_api::routes())
        .fallback(not_found)
        .layer(middleware::from_fn_with_state(
            state.clone(),
            require_caller,
        ))
        .layer(middleware::from_fn(json_no_store))
        .with_state(state)
}

/// GET /healthz: open to anyone on this machine.
async fn healthz(State(state): State<AppState>) -> Response {
    Json(json!({ "ok": true, "version": state.version })).into_response()
}

/// Nothing matched the route (and the caller was valid).
pub async fn not_found() -> Response {
    error_response(StatusCode::NOT_FOUND, "There's nothing at that address.")
}

/// Builds a JSON error body: `{"error": "..."}`.
pub fn error_response(status: StatusCode, message: &str) -> Response {
    (status, Json(json!({ "error": message }))).into_response()
}

/// Checks the signed caller. On success the `Caller` goes into the request
/// extensions for later handlers.
async fn require_caller(State(state): State<AppState>, mut req: Request, next: Next) -> Response {
    // The health check is the only route that needs no caller.
    if req.method() == Method::GET && req.uri().path() == "/healthz" {
        return next.run(req).await;
    }

    let method = req.method().as_str().to_string();
    // The path and query exactly as received.
    let target = req
        .uri()
        .path_and_query()
        .map(|p| p.as_str())
        .unwrap_or("")
        .to_string();

    match verify(req.headers(), &method, &target, unix_now(), &state.key) {
        Ok(caller) => {
            // People and admin API keys never use secrets, and only homelabd uses the
            // internal routes, whatever the path under them.
            if let Some(refusal) = secrets_api::refuse_path(&caller, req.uri().path()) {
                return refusal.into_response();
            }
            if let Some(refusal) = dbs_api::refuse_path(&caller, req.uri().path()) {
                return refusal.into_response();
            }
            req.extensions_mut().insert(caller);
            next.run(req).await
        }
        Err(reason) => {
            // Log only the reason, method and path. Never header values.
            tracing::warn!(%reason, %method, path = req.uri().path(), "refused request");
            error_response(StatusCode::UNAUTHORIZED, &reason)
        }
    }
}

/// Makes every response uncacheable, and JSON unless the handler chose a type.
async fn json_no_store(req: Request, next: Next) -> Response {
    let mut response = next.run(req).await;
    let headers = response.headers_mut();
    headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
    if !headers.contains_key(CONTENT_TYPE) {
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
    }
    response
}

/// The current time in unix seconds.
pub fn unix_now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::caller::{CALLER_HEADER, SIGNATURE_HEADER};
    use crate::testutil::*;
    use axum::body::Body;
    use axum::http::HeaderMap;
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    fn test_app() -> (Router, tempfile::TempDir) {
        let env = test_env(1024);
        (env.router, env.dir)
    }

    /// Sends one request and returns the status, headers and parsed JSON body.
    async fn send(
        method: Method,
        target: &str,
        headers: HeaderMap,
    ) -> (StatusCode, HeaderMap, serde_json::Value) {
        let mut req = Request::builder().method(method).uri(target);
        for (name, value) in headers.iter() {
            req = req.header(name, value);
        }
        let (router, _dir) = test_app();
        let response = router
            .oneshot(req.body(Body::empty()).unwrap())
            .await
            .unwrap();
        let (parts, body) = response.into_parts();
        let bytes = body.collect().await.unwrap().to_bytes();
        let json = serde_json::from_slice(&bytes).unwrap_or(serde_json::Value::Null);
        (parts.status, parts.headers, json)
    }

    /// Headers signed for this request, with the given timestamp.
    fn signed(method: &str, target: &str, ts: i64) -> HeaderMap {
        signed_headers(&test_key(), &caller_json(method, target, ts))
    }

    #[tokio::test]
    async fn healthz_needs_no_caller() {
        let (status, headers, body) = send(Method::GET, "/healthz", HeaderMap::new()).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body, json!({ "ok": true, "version": "test" }));
        assert_eq!(headers["content-type"], "application/json");
        assert_eq!(headers["cache-control"], "no-store");
    }

    #[tokio::test]
    async fn unsigned_request_is_refused() {
        let (status, headers, body) = send(Method::GET, "/api/files/x", HeaderMap::new()).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        assert!(body["error"].is_string());
        assert_eq!(headers["content-type"], "application/json");
        assert_eq!(headers["cache-control"], "no-store");
    }

    #[tokio::test]
    async fn healthz_with_other_method_still_needs_caller() {
        let (status, _, _) = send(Method::POST, "/healthz", HeaderMap::new()).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        let (status, _, _) = send(
            Method::POST,
            "/healthz",
            signed("POST", "/healthz", unix_now()),
        )
        .await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn signed_request_to_unknown_route_is_404() {
        let target = "/api/other/x?y=1";
        let (status, headers, body) =
            send(Method::GET, target, signed("GET", target, unix_now())).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        assert_eq!(body, json!({ "error": "There's nothing at that address." }));
        assert_eq!(headers["content-type"], "application/json");
        assert_eq!(headers["cache-control"], "no-store");
    }

    #[tokio::test]
    async fn any_method_is_checked() {
        let (status, _, _) = send(Method::DELETE, "/x", HeaderMap::new()).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        let (status, _, _) = send(Method::DELETE, "/x", signed("DELETE", "/x", unix_now())).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn forged_signature_is_refused() {
        let headers = signed_headers(&other_key(), &caller_json("GET", "/x", unix_now()));
        let (status, _, _) = send(Method::GET, "/x", headers).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn tampered_caller_is_refused() {
        let mut headers = signed("GET", "/x", unix_now());
        let mut admin = caller_json("GET", "/x", unix_now());
        admin["kind"] = "admin".into();
        let other = signed_headers(&test_key(), &admin);
        headers.insert(CALLER_HEADER, other[CALLER_HEADER].clone());
        let (status, _, _) = send(Method::GET, "/x", headers).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn old_and_future_timestamps_are_refused() {
        for ts in [unix_now() - 35, unix_now() + 35] {
            let (status, _, _) = send(Method::GET, "/x", signed("GET", "/x", ts)).await;
            assert_eq!(status, StatusCode::UNAUTHORIZED, "ts {ts}");
        }
        for ts in [unix_now() - 20, unix_now() + 20] {
            let (status, _, _) = send(Method::GET, "/x", signed("GET", "/x", ts)).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "ts {ts}");
        }
    }

    #[tokio::test]
    async fn method_path_and_query_must_match() {
        let now = unix_now();
        // Signed for GET, sent as POST.
        let (status, _, _) = send(Method::POST, "/x", signed("GET", "/x", now)).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        // Signed for /x, sent to /y.
        let (status, _, _) = send(Method::GET, "/y", signed("GET", "/x", now)).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        // Signed with one query, sent with another (or none).
        let (status, _, _) = send(Method::GET, "/x?a=2", signed("GET", "/x?a=1", now)).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        let (status, _, _) = send(Method::GET, "/x", signed("GET", "/x?a=1", now)).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn duplicate_header_is_refused() {
        let mut headers = signed("GET", "/x", unix_now());
        let value = headers[SIGNATURE_HEADER].clone();
        headers.append(SIGNATURE_HEADER, value);
        let (status, _, _) = send(Method::GET, "/x", headers).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn oversized_caller_is_refused() {
        let mut caller = caller_json("GET", "/x", unix_now());
        caller["person"] = "p".repeat(9 * 1024).into();
        let headers = signed_headers(&test_key(), &caller);
        let (status, _, _) = send(Method::GET, "/x", headers).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn bad_kind_and_version_are_refused() {
        for (field, value) in [("kind", json!("robot")), ("v", json!(2))] {
            let mut caller = caller_json("GET", "/x", unix_now());
            caller[field] = value;
            let headers = signed_headers(&test_key(), &caller);
            let (status, _, _) = send(Method::GET, "/x", headers).await;
            assert_eq!(status, StatusCode::UNAUTHORIZED, "{field}");
        }
    }
}
