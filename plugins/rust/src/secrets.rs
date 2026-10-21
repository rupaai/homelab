use std::path::PathBuf;
use std::sync::Arc;

use reqwest::Method;
use serde::Deserialize;
use serde_json::json;

use crate::client::Inner;
use crate::error::{Error, Result};
use crate::types::*;

/// Where Homelab puts config secrets as files inside an app.
pub const DEFAULT_SECRETS_DIR: &str = "/run/secrets";

/// The same rule as the server: 1 to 200 of letters, digits and `_ . : -`
/// (never `.` or `..`).
fn check_name(name: &str) -> Result<()> {
    let ok = !name.is_empty()
        && name.len() <= 200
        && name != "."
        && name != ".."
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b':' | b'-'));
    if ok {
        Ok(())
    } else {
        Err(Error::config(
            "A secret name is 1 to 200 letters, digits and _ . : -",
        ))
    }
}

/// Only names that are safe file names are looked up on disk.
fn is_file_name(name: &str) -> bool {
    let mut bytes = name.bytes();
    matches!(bytes.next(), Some(b) if b.is_ascii_alphabetic() || b == b'_')
        && bytes.all(|b| b.is_ascii_alphanumeric() || b == b'_')
}

/// The Secrets service. Get one with [`Homelab::secrets`](crate::Homelab::secrets).
/// Values are never logged or put in errors.
#[derive(Clone)]
pub struct Secrets {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for Secrets {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Secrets")
            .field("dir", &self.inner.secrets_dir)
            .finish_non_exhaustive()
    }
}

#[derive(Deserialize)]
struct Found {
    #[serde(default)]
    value: String,
}

#[derive(Deserialize)]
struct SecretList {
    #[serde(default)]
    secrets: Vec<SecretInfo>,
}

#[derive(Deserialize)]
struct VersionList {
    #[serde(default)]
    versions: Vec<SecretVersion>,
}

impl Secrets {
    pub(crate) fn new(inner: Arc<Inner>) -> Self {
        Self { inner }
    }

    /// The value of a secret.
    ///
    /// Without `version`, a file `<secrets dir>/<name>` is used when it exists (the
    /// contents are returned exactly as written). Otherwise, or with `fresh`, it asks
    /// Homelab: `GET /api/secrets/{name}[?version=n]`. A missing secret is
    /// `Error::Api { status: 404, .. }`; see [`get_opt`](Self::get_opt).
    pub async fn get(&self, name: &str, opts: GetSecretOptions) -> Result<String> {
        check_name(name)?;
        if opts.version == Some(0) {
            return Err(Error::config("The version is a number from 1."));
        }
        if opts.version.is_none() && !opts.fresh {
            if let Some(v) = self.read_file(name).await? {
                return Ok(v);
            }
        }
        let mut req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "secrets", name])?)
            .timeout(self.inner.timeout);
        if let Some(v) = opts.version {
            req = req.query(&[("version", v)]);
        }
        let found: Found = self.inner.send(req).await?.json().await?;
        Ok(found.value)
    }

    /// Like [`get`](Self::get), but `Ok(None)` when there is no such secret (404).
    /// Other errors are still errors.
    pub async fn get_opt(&self, name: &str, opts: GetSecretOptions) -> Result<Option<String>> {
        match self.get(name, opts).await {
            Ok(v) => Ok(Some(v)),
            Err(Error::Api { status: 404, .. }) => Ok(None),
            Err(e) => Err(e),
        }
    }

    /// `PUT /api/secrets/{name}`. Saves the value as the next version (needs the
    /// `secrets:write` scope).
    pub async fn set(&self, name: &str, value: &str) -> Result<SecretSaved> {
        check_name(name)?;
        let req = self
            .inner
            .request(Method::PUT, self.inner.url(&["api", "secrets", name])?)
            .timeout(self.inner.timeout)
            .json(&json!({ "value": value }));
        match self.inner.send(req).await {
            Ok(resp) => Ok(resp.json().await?),
            // The server never echoes a value, but nothing we sent may come back in an error.
            Err(Error::Api { status, message }) if !value.is_empty() && message.contains(value) => {
                Err(Error::Api {
                    status,
                    message: message.replace(value, "[redacted]"),
                })
            }
            Err(e) => Err(e),
        }
    }

    /// `DELETE /api/secrets/{name}`. Deleting a secret that is not there is fine.
    pub async fn delete(&self, name: &str) -> Result<()> {
        check_name(name)?;
        let req = self
            .inner
            .request(Method::DELETE, self.inner.url(&["api", "secrets", name])?)
            .timeout(self.inner.timeout);
        self.inner.send(req).await?;
        Ok(())
    }

    /// `GET /api/secrets`: names, kinds and versions. No values.
    pub async fn list(&self) -> Result<Vec<SecretInfo>> {
        let req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "secrets"])?)
            .timeout(self.inner.timeout);
        let list: SecretList = self.inner.send(req).await?.json().await?;
        Ok(list.secrets)
    }

    /// `GET /api/secrets/{name}/versions`: the kept versions, newest first. No values.
    pub async fn versions(&self, name: &str) -> Result<Vec<SecretVersion>> {
        check_name(name)?;
        let req = self
            .inner
            .request(
                Method::GET,
                self.inner.url(&["api", "secrets", name, "versions"])?,
            )
            .timeout(self.inner.timeout);
        let list: VersionList = self.inner.send(req).await?.json().await?;
        Ok(list.versions)
    }

    /// The file's contents, or `None` when there is no such file.
    async fn read_file(&self, name: &str) -> Result<Option<String>> {
        if !is_file_name(name) {
            return Ok(None);
        }
        let path: PathBuf = self.inner.secrets_dir.join(name);
        match tokio::fs::read(&path).await {
            Ok(bytes) => String::from_utf8(bytes).map(Some).map_err(|_| {
                Error::config(format!("The secret file for {name:?} is not valid UTF-8"))
            }),
            Err(e) if is_missing(&e) => Ok(None),
            Err(e) => Err(Error::config(format!(
                "Could not read the secret file for {name:?} ({:?})",
                e.kind()
            ))),
        }
    }
}

fn is_missing(e: &std::io::Error) -> bool {
    use std::io::ErrorKind::*;
    // `NotADirectory` and `IsADirectory` are not stable kinds on 1.75, so check the code too.
    matches!(e.kind(), NotFound) || matches!(e.raw_os_error(), Some(20) | Some(21))
}
