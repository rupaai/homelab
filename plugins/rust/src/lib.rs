//! Thin async client for Homelab's web API (Files, Secrets, Databases, Releases and Logs services).
//!
//! ```no_run
//! # async fn run() -> Result<(), homelab::Error> {
//! let homelab = homelab::Homelab::from_env()?;
//! let up = homelab
//!     .files()
//!     .upload("posters", b"bytes".as_slice(), homelab::UploadOptions::new("a.txt"))
//!     .await?;
//! println!("{} duplicate={}", up.file.id, up.duplicate);
//! # Ok(()) }
//! ```
//!
//! Secrets work the same way: `homelab.secrets().get("API_KEY", Default::default()).await?`.
//! Versions: `homelab::version()` is this app's `HOMELAB_VERSION` (`None` when missing), and
//! `homelab.releases().list(None).await?` lists the app's own releases.
//! Logs: `homelab.logs().search(homelab::LogQuery { q: Some("error".into()), ..Default::default() }).await?`.
//! Databases: `homelab.db().tables(None, None).await?` (read-only; `None` means `HOMELAB_APP` / `HOMELAB_ENV`).

mod client;
mod db;
mod error;
mod files;
mod logs;
mod releases;
mod secrets;
mod types;

pub use client::{Homelab, DEFAULT_TIMEOUT, ENV_TOKEN, ENV_URL};
pub use db::{Db, ENV_APP, ENV_ENV};
pub use error::{Error, Result};
pub use files::Files;
pub use logs::Logs;
pub use releases::{version, Releases, ENV_VERSION};
pub use secrets::{Secrets, DEFAULT_SECRETS_DIR};
pub use types::*;

pub use bytes::Bytes;
pub use futures_util::{Stream, StreamExt, TryStreamExt};
