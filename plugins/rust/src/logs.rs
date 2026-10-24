use std::sync::Arc;

use reqwest::Method;

use crate::client::Inner;
use crate::error::{Error, Result};
use crate::types::{LogQuery, LogSearchResult};

const LEVELS: [&str; 6] = ["unknown", "debug", "info", "warn", "error", "fatal"];

fn check_level(level: &str) -> Result<&str> {
    if LEVELS.contains(&level) {
        Ok(level)
    } else {
        Err(Error::config(format!(
            "Unknown log level \"{level}\". Use one of: {}.",
            LEVELS.join(", ")
        )))
    }
}

/// Search the logs. Get one with [`Homelab::logs`](crate::Homelab::logs).
#[derive(Clone)]
pub struct Logs {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for Logs {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Logs").finish_non_exhaustive()
    }
}

impl Logs {
    pub(crate) fn new(inner: Arc<Inner>) -> Self {
        Self { inner }
    }

    /// `GET /api/logs`: searches the logs, newest lines first. An app key sees its own app and
    /// environment only.
    pub async fn search(&self, query: LogQuery) -> Result<LogSearchResult> {
        let mut params: Vec<(&str, String)> = Vec::new();
        if let Some(q) = query.q.as_deref().filter(|q| !q.trim().is_empty()) {
            params.push(("q", q.to_string()));
        }
        for (name, value) in [
            ("app", &query.app),
            ("env", &query.env),
            ("service", &query.service),
        ] {
            if let Some(v) = value.as_deref().filter(|v| !v.is_empty()) {
                params.push((name, v.to_string()));
            }
        }
        if !query.level.is_empty() {
            let levels = query
                .level
                .iter()
                .map(|l| check_level(l))
                .collect::<Result<Vec<_>>>()?;
            params.push(("level", levels.join(",")));
        }
        if let Some(l) = query.min_level.as_deref() {
            params.push(("min_level", check_level(l)?.to_string()));
        }
        if let Some(v) = &query.since {
            params.push(("since", v.clone()));
        }
        if let Some(v) = &query.until {
            params.push(("until", v.clone()));
        }
        if let Some(v) = query.before {
            params.push(("before", v.to_string()));
        }
        if let Some(v) = query.after {
            params.push(("after", v.to_string()));
        }
        if let Some(n) = query.limit {
            if !(1..=500).contains(&n) {
                return Err(Error::config(
                    "The limit must be a whole number from 1 to 500.",
                ));
            }
            params.push(("limit", n.to_string()));
        }
        let mut req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "logs"])?)
            .timeout(self.inner.timeout);
        if !params.is_empty() {
            req = req.query(&params);
        }
        Ok(self.inner.send(req).await?.json().await?)
    }
}
