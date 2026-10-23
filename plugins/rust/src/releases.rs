use std::sync::Arc;

use reqwest::Method;
use serde::Deserialize;

use crate::client::Inner;
use crate::error::{Error, Result};
use crate::types::Release;

/// Env var holding the app's version (set by Homelab when it could make one).
pub const ENV_VERSION: &str = "HOMELAB_VERSION";

/// This app's version (`HOMELAB_VERSION`), or `None` when Homelab has not given it one.
pub fn version() -> Option<String> {
    std::env::var(ENV_VERSION)
        .ok()
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
}

/// The app's own versions. Get one with [`Homelab::releases`](crate::Homelab::releases).
#[derive(Clone)]
pub struct Releases {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for Releases {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Releases").finish_non_exhaustive()
    }
}

#[derive(Deserialize)]
struct ReleaseList {
    #[serde(default)]
    releases: Vec<Release>,
}

impl Releases {
    pub(crate) fn new(inner: Arc<Inner>) -> Self {
        Self { inner }
    }

    /// `GET /api/releases`: the app's releases, newest first. Dev builds are pre-releases.
    /// `limit` is a whole number from 1; `None` gives everything Homelab sends.
    pub async fn list(&self, limit: Option<u32>) -> Result<Vec<Release>> {
        if limit == Some(0) {
            return Err(Error::config("The limit must be a whole number from 1."));
        }
        let mut req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "releases"])?)
            .timeout(self.inner.timeout);
        if let Some(n) = limit {
            req = req.query(&[("limit", n.to_string())]);
        }
        let list: ReleaseList = self.inner.send(req).await?.json().await?;
        Ok(list.releases)
    }
}
