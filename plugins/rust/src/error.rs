use std::fmt;

/// Everything that can go wrong. One type for the whole crate.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The server answered with an error status. `message` is the server's
    /// `{"error": "..."}` text, or a plain fallback such as `HTTP 502 Bad Gateway`.
    #[error("homelab: {message} (HTTP {status})")]
    Api { status: u16, message: String },

    /// The request never got a usable answer (connection failed, timed out,
    /// or the answer could not be read).
    #[error("homelab: network error: {0}")]
    Network(#[from] reqwest::Error),

    /// Bad configuration, found when building the client.
    #[error("homelab: {0}")]
    Config(String),
}

impl Error {
    /// The HTTP status, or 0 for network and configuration errors.
    pub fn status(&self) -> u16 {
        match self {
            Error::Api { status, .. } => *status,
            _ => 0,
        }
    }

    pub(crate) fn config(message: impl fmt::Display) -> Self {
        Error::Config(message.to_string())
    }
}

/// `Result` with this crate's [`Error`].
pub type Result<T> = std::result::Result<T, Error>;
