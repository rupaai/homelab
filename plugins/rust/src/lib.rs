//! Thin async client for Homelab's web API (Files, Secrets and Databases services).
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
//! Databases: `homelab.db().tables(None, None).await?` (read-only; `None` means `HOMELAB_APP` / `HOMELAB_ENV`).

mod client;
mod db;
mod error;
mod files;
mod secrets;
mod types;

pub use client::{Homelab, DEFAULT_TIMEOUT, ENV_TOKEN, ENV_URL};
pub use db::{Db, ENV_APP, ENV_ENV};
pub use error::{Error, Result};
pub use files::Files;
pub use secrets::{Secrets, DEFAULT_SECRETS_DIR};
pub use types::*;

pub use bytes::Bytes;
pub use futures_util::{Stream, StreamExt, TryStreamExt};
