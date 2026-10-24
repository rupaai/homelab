use std::fmt;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use reqwest::{RequestBuilder, Response};

use crate::db::Db;
use crate::error::{Error, Result};
use crate::files::Files;
use crate::logs::Logs;
use crate::releases::Releases;
use crate::secrets::{Secrets, DEFAULT_SECRETS_DIR};

/// Env var holding the API address.
pub const ENV_URL: &str = "HOMELAB_API_URL";
/// Env var holding the API key.
pub const ENV_TOKEN: &str = "HOMELAB_TOKEN";
/// Default timeout for calls without a streamed body.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(60);

pub(crate) struct Inner {
    pub(crate) http: reqwest::Client,
    base: String,
    token: String,
    pub(crate) timeout: Duration,
    pub(crate) secrets_dir: PathBuf,
}

/// The Homelab client. Cheap to clone; share it freely.
#[derive(Clone)]
pub struct Homelab {
    pub(crate) inner: Arc<Inner>,
}

impl fmt::Debug for Homelab {
    // Written by hand so the token can never end up in logs.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Homelab")
            .field("url", &self.inner.base)
            .field("timeout", &self.inner.timeout)
            .finish_non_exhaustive()
    }
}

impl Homelab {
    /// Reads `HOMELAB_API_URL` and `HOMELAB_TOKEN`.
    pub fn from_env() -> Result<Self> {
        Self::from_vars(|k| std::env::var(k).ok())
    }

    pub(crate) fn from_vars(get: impl Fn(&str) -> Option<String>) -> Result<Self> {
        let url = get(ENV_URL).filter(|v| !v.trim().is_empty());
        let token = get(ENV_TOKEN).filter(|v| !v.trim().is_empty());
        match (url, token) {
            (Some(u), Some(t)) => Self::new(u, t),
            (None, _) => Err(Error::config(format!(
                "Set {ENV_URL} or pass url (Homelab::new)"
            ))),
            (_, None) => Err(Error::config(format!(
                "Set {ENV_TOKEN} or pass token (Homelab::new)"
            ))),
        }
    }

    /// Builds a client for `url` (a trailing slash is fine) and `token`.
    pub fn new(url: impl Into<String>, token: impl Into<String>) -> Result<Self> {
        let url = url.into();
        let token = token.into();
        let base = url.trim().trim_end_matches('/').to_string();
        if base.is_empty() {
            return Err(Error::config(format!(
                "Set {ENV_URL} or pass url (Homelab::new)"
            )));
        }
        if !(base.starts_with("http://") || base.starts_with("https://")) {
            return Err(Error::config(
                "url must start with http:// or https://".to_string(),
            ));
        }
        let token = token.trim().to_string();
        if token.is_empty() {
            return Err(Error::config(format!(
                "Set {ENV_TOKEN} or pass token (Homelab::new)"
            )));
        }
        let http = reqwest::Client::builder()
            .user_agent(concat!("homelab-sdk-rust/", env!("CARGO_PKG_VERSION")))
            // Never follow redirects: the token must not travel to another place.
            .redirect(reqwest::redirect::Policy::none())
            .build()?;
        Ok(Self {
            inner: Arc::new(Inner {
                http,
                base,
                token,
                timeout: DEFAULT_TIMEOUT,
                secrets_dir: PathBuf::from(DEFAULT_SECRETS_DIR),
            }),
        })
    }

    /// A copy of this client with another timeout for normal calls
    /// (uploads and downloads never time out).
    pub fn with_timeout(&self, timeout: Duration) -> Self {
        Self {
            inner: Arc::new(Inner {
                http: self.inner.http.clone(),
                base: self.inner.base.clone(),
                token: self.inner.token.clone(),
                timeout,
                secrets_dir: self.inner.secrets_dir.clone(),
            }),
        }
    }

    /// A copy of this client that looks for secret files in `dir` instead of
    /// `/run/secrets` (used by `secrets().get`).
    pub fn with_secrets_dir(&self, dir: impl Into<PathBuf>) -> Self {
        Self {
            inner: Arc::new(Inner {
                http: self.inner.http.clone(),
                base: self.inner.base.clone(),
                token: self.inner.token.clone(),
                timeout: self.inner.timeout,
                secrets_dir: dir.into(),
            }),
        }
    }

    /// The Files service.
    pub fn files(&self) -> Files {
        Files::new(self.inner.clone())
    }
    /// The Secrets service.
    pub fn secrets(&self) -> Secrets {
        Secrets::new(self.inner.clone())
    }
    /// The Databases service (read-only).
    pub fn db(&self) -> Db {
        Db::new(self.inner.clone())
    }
    /// The app's own versions.
    pub fn releases(&self) -> Releases {
        Releases::new(self.inner.clone())
    }
    /// Search the logs.
    pub fn logs(&self) -> Logs {
        Logs::new(self.inner.clone())
    }
    /// This app's version (`HOMELAB_VERSION`), or `None` when Homelab has not given it one.
    pub fn version(&self) -> Option<String> {
        crate::releases::version()
    }
    // Room for `data()` later: each will be a small handle like
    // `Files` that shares `Inner`.
}

impl Inner {
    /// `base` + `/api/files/...` with each segment percent-encoded.
    pub(crate) fn url(&self, segments: &[&str]) -> Result<String> {
        let mut out = self.base.clone();
        for s in segments {
            // URL parsers resolve `.` and `..` (even as %2E) and would hit another path.
            if matches!(*s, "." | "..") {
                return Err(Error::config(format!("invalid name {s:?}")));
            }
            out.push('/');
            out.push_str(&encode_segment(s));
        }
        Ok(out)
    }

    pub(crate) fn request(&self, method: reqwest::Method, url: String) -> RequestBuilder {
        self.http.request(method, url).bearer_auth(&self.token)
    }

    /// Sends and turns non-2xx answers into `Error::Api`.
    pub(crate) async fn send(&self, req: RequestBuilder) -> Result<Response> {
        let resp = req.send().await?;
        let status = resp.status();
        if status.is_success() {
            return Ok(resp);
        }
        let body = resp.bytes().await.unwrap_or_default();
        Err(Error::Api {
            status: status.as_u16(),
            message: error_message(status, &body),
        })
    }
}

fn error_message(status: reqwest::StatusCode, body: &[u8]) -> String {
    if let Ok(v) = serde_json::from_slice::<serde_json::Value>(body) {
        if let Some(s) = v.get("error").and_then(|e| e.as_str()) {
            if !s.is_empty() {
                return s.to_string();
            }
        }
    }
    match status.canonical_reason() {
        Some(r) => format!("HTTP {} {}", status.as_u16(), r),
        None => format!("HTTP {}", status.as_u16()),
    }
}

/// Percent-encodes everything but unreserved characters.
pub(crate) fn encode_segment(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'~' => out.push(b as char),
            b'.' => out.push('.'),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}
