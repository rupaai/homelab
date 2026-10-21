//! homelab-services: a small HTTP server started by homelabd.
//!
//! homelabd is the only caller. It signs every request it forwards, and this
//! server refuses anything that is not signed (except /healthz).

pub mod app;
pub mod caller;
pub mod config;
pub mod db;
pub mod disk;
pub mod files;

#[cfg(test)]
mod testutil;

/// The version of this build. Set `HOMELAB_VERSION` at compile time to change it.
pub const VERSION: &str = match option_env!("HOMELAB_VERSION") {
    Some(v) => v,
    None => "dev",
};
