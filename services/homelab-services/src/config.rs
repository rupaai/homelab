//! Settings read from environment variables.

use std::net::SocketAddr;
use std::path::{Path, PathBuf};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ed25519_dalek::VerifyingKey;

/// Everything the server needs to start.
#[derive(Debug)]
pub struct Config {
    /// Where to listen. Always a loopback address.
    pub listen: SocketAddr,
    /// Folder for this program's own state.
    pub data_dir: PathBuf,
    /// homelabd's public key, used to check caller signatures.
    pub key: VerifyingKey,
}

impl Config {
    /// Reads the settings from the environment.
    /// Errors are one plain line, ready to print.
    pub fn from_env() -> Result<Config, String> {
        Config::parse(
            &read_var("HOMELAB_SERVICES_LISTEN")?,
            &read_var("HOMELAB_SERVICES_DATA")?,
            &read_var("HOMELAB_SERVICES_KEY")?,
        )
    }

    /// Checks the three raw values and builds a Config.
    /// This does not touch the disk; see `prepare_data_dir`.
    pub fn parse(listen: &str, data_dir: &str, key: &str) -> Result<Config, String> {
        let listen: SocketAddr = listen.trim().parse().map_err(|_| {
            "HOMELAB_SERVICES_LISTEN must look like 127.0.0.1:8080 (an IP and a port)".to_string()
        })?;
        // Never listen anywhere but this machine.
        if !listen.ip().is_loopback() {
            return Err("HOMELAB_SERVICES_LISTEN must be a loopback address \
                        (127.x.x.x or ::1)"
                .to_string());
        }

        if data_dir.trim().is_empty() {
            return Err("HOMELAB_SERVICES_DATA must not be empty".to_string());
        }

        Ok(Config {
            listen,
            data_dir: PathBuf::from(data_dir),
            key: parse_key(key)?,
        })
    }
}

/// Reads one required environment variable.
fn read_var(name: &str) -> Result<String, String> {
    match std::env::var(name) {
        Ok(v) if !v.trim().is_empty() => Ok(v),
        Ok(_) => Err(format!("{name} is set but empty")),
        Err(std::env::VarError::NotPresent) => Err(format!("{name} is not set")),
        Err(std::env::VarError::NotUnicode(_)) => Err(format!("{name} is not valid text")),
    }
}

/// Decodes the public key: 32 bytes as base64url without padding.
fn parse_key(text: &str) -> Result<VerifyingKey, String> {
    let bad = || {
        "HOMELAB_SERVICES_KEY must be a 32-byte ed25519 public key \
         in base64url without padding"
            .to_string()
    };
    let bytes = URL_SAFE_NO_PAD.decode(text.trim()).map_err(|_| bad())?;
    let bytes: [u8; 32] = bytes.try_into().map_err(|_| bad())?;
    VerifyingKey::from_bytes(&bytes).map_err(|_| bad())
}

/// Creates the data folder (and parents) if it is missing.
/// New folders are private to the current user on unix.
pub fn prepare_data_dir(dir: &Path) -> Result<(), String> {
    let mut builder = std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(dir)
        .map_err(|e| format!("cannot create data folder {}: {e}", dir.display()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::test_key_text;

    #[test]
    fn accepts_loopback_v4_and_v6() {
        assert!(Config::parse("127.0.0.1:8080", "/tmp/x", &test_key_text()).is_ok());
        assert!(Config::parse("127.5.5.5:1", "/tmp/x", &test_key_text()).is_ok());
        assert!(Config::parse("[::1]:8080", "/tmp/x", &test_key_text()).is_ok());
    }

    #[test]
    fn refuses_non_loopback_listen() {
        for addr in [
            "0.0.0.0:8080",
            "192.168.1.5:8080",
            "[::]:8080",
            "8.8.8.8:80",
        ] {
            let err = Config::parse(addr, "/tmp/x", &test_key_text()).unwrap_err();
            assert!(err.contains("loopback"), "{addr}: {err}");
        }
    }

    #[test]
    fn refuses_unparsable_listen() {
        assert!(Config::parse("localhost:8080", "/tmp/x", &test_key_text()).is_err());
        assert!(Config::parse("127.0.0.1", "/tmp/x", &test_key_text()).is_err());
    }

    #[test]
    fn refuses_bad_keys() {
        let short = URL_SAFE_NO_PAD.encode([1u8; 31]);
        for key in ["", "not base64 !!", "AAAA", short.as_str()] {
            let err = Config::parse("127.0.0.1:8080", "/tmp/x", key).unwrap_err();
            assert!(err.contains("HOMELAB_SERVICES_KEY"), "{key}: {err}");
        }
    }

    #[test]
    fn creates_data_dir_with_parents() {
        let base = std::env::temp_dir().join(format!("hs-test-{}", std::process::id()));
        let dir = base.join("a/b");
        prepare_data_dir(&dir).unwrap();
        assert!(dir.is_dir());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&dir).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o700);
        }
        std::fs::remove_dir_all(&base).unwrap();
    }
}
