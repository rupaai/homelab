//! Pieces every API module shares: the error answer, the reply type, the refusal log, and the
//! app/env check.

use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};

use crate::app::error_response;
use crate::caller::Caller;
use crate::disk::valid_name;

/// An error answer: a status and a plain message.
pub struct ApiError(pub(crate) StatusCode, pub(crate) String);

pub(crate) fn err(status: StatusCode, message: impl Into<String>) -> ApiError {
    ApiError(status, message.into())
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        error_response(self.0, &self.1)
    }
}

/// What a handler returns.
pub(crate) type Reply = Result<Response, ApiError>;

/// A refused caller. `what` names the module in the log ("databases", "secrets", "logs").
/// Nothing but who asked is logged.
pub(crate) fn forbidden(what: &str, caller: &Caller, why: &str) -> ApiError {
    tracing::warn!(kind = ?caller.kind, app = %caller.app, env = %caller.env, "{what} request refused");
    err(StatusCode::FORBIDDEN, why)
}

/// An app name, `dev` or `prod`.
pub(crate) fn valid_place(app: &str, env: &str) -> bool {
    valid_name(app, 60) && matches!(env, "dev" | "prod")
}
