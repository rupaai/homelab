//! homelab-services: a small HTTP server started by homelabd.
//!
//! homelabd is the only caller. It signs every request it forwards, and this
//! server refuses anything that is not signed (except /healthz).

pub mod api;
pub mod app;
pub mod caller;
pub mod config;
pub mod db;
pub mod dbs;
pub mod dbs_api;
pub mod dbs_sql;
pub mod disk;
pub mod files;
pub mod logs;
pub mod meta;
pub mod releases;
pub mod releases_api;
pub mod releases_github;
pub mod secrets;
pub mod secrets_api;

#[cfg(test)]
mod releases_tests;
#[cfg(test)]
mod testutil;

/// The version of this build. Set `HOMELAB_VERSION` at compile time to change it.
pub const VERSION: &str = match option_env!("HOMELAB_VERSION") {
    Some(v) => v,
    None => "dev",
};
