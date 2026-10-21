//! Settings read from environment variables.

use std::net::SocketAddr;
use std::path::{Path, PathBuf};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ed25519_dalek::VerifyingKey;
use serde::Deserialize;

use crate::disk::valid_name;

/// Everything the server needs to start.
#[derive(Debug)]
pub struct Config {
    /// Where to listen. Always a loopback address.
    pub listen: SocketAddr,
    /// Folder for this program's own state.
    pub data_dir: PathBuf,
    /// homelabd's public key, used to check caller signatures.
    pub key: VerifyingKey,
    /// The disks files can be stored on. The first is the default.
    pub storage: Vec<Disk>,
    /// Biggest upload in bytes.
    pub max_upload: u64,
    /// The master key file for secrets. Without it the secrets service is off.
    pub secrets_key: Option<PathBuf>,
    /// The old secrets database made by homelabd, imported once on first start.
    pub secrets_import: Option<PathBuf>,
}

/// One storage disk.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Disk {
    pub name: String,
    pub path: PathBuf,
}

/// The upload limit when HOMELAB_SERVICES_MAX_UPLOAD is not set: 10 GiB.
const DEFAULT_MAX_UPLOAD: u64 = 10 * 1024 * 1024 * 1024;

impl Config {
    /// Reads the settings from the environment.
    /// Errors are one plain line, ready to print.
    pub fn from_env() -> Result<Config, String> {
        Config::parse(
            &read_var("HOMELAB_SERVICES_LISTEN")?,
            &read_var("HOMELAB_SERVICES_DATA")?,
            &read_var("HOMELAB_SERVICES_KEY")?,
            &read_var("HOMELAB_SERVICES_STORAGE")?,
            std::env::var("HOMELAB_SERVICES_MAX_UPLOAD").ok().as_deref(),
            std::env::var("HOMELAB_SERVICES_SECRETS_KEY")
                .ok()
                .as_deref(),
            std::env::var("HOMELAB_SERVICES_SECRETS_IMPORT")
                .ok()
                .as_deref(),
        )
    }

    /// Checks the raw values and builds a Config.
    /// This does not touch the disk; see `prepare_data_dir` and `prepare_storage`.
    pub fn parse(
        listen: &str,
        data_dir: &str,
        key: &str,
        storage: &str,
        max_upload: Option<&str>,
        secrets_key: Option<&str>,
        secrets_import: Option<&str>,
    ) -> Result<Config, String> {
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

        let storage = parse_storage(storage)?;
        let max_upload = parse_max_upload(max_upload)?;
        let secrets_key = parse_path("HOMELAB_SERVICES_SECRETS_KEY", secrets_key)?;
        let secrets_import = parse_path("HOMELAB_SERVICES_SECRETS_IMPORT", secrets_import)?;
        Ok(Config {
            listen,
            data_dir: PathBuf::from(data_dir),
            key: parse_key(key)?,
            storage,
            max_upload,
            secrets_key,
            secrets_import,
        })
    }
}

/// Reads an optional file path setting. Unset or empty means none. It must be absolute.
/// The path is where a file is, never the secret itself.
fn parse_path(name: &str, text: Option<&str>) -> Result<Option<PathBuf>, String> {
    match text.map(str::trim) {
        None | Some("") => Ok(None),
        Some(t) if Path::new(t).is_absolute() => Ok(Some(PathBuf::from(t))),
        Some(_) => Err(format!("{name} must be an absolute path")),
    }
}

/// Reads the disks: a JSON array like `[{"name":"main","path":"/mnt/storage"}]`.
fn parse_storage(text: &str) -> Result<Vec<Disk>, String> {
    let bad = |why: &str| {
        format!(
            "HOMELAB_SERVICES_STORAGE must be a JSON list like \
             [{{\"name\":\"main\",\"path\":\"/mnt/storage\"}}]: {why}"
        )
    };
    let disks: Vec<Disk> = serde_json::from_str(text).map_err(|e| bad(&e.to_string()))?;
    if disks.is_empty() {
        return Err(bad("it needs at least one disk"));
    }
    for (i, d) in disks.iter().enumerate() {
        if !valid_name(&d.name, 32) {
            return Err(bad(&format!(
                "disk name \"{}\" must be 1-32 lowercase letters, digits, _ or -",
                d.name
            )));
        }
        if disks[..i].iter().any(|o| o.name == d.name) {
            return Err(bad(&format!("disk name \"{}\" is used twice", d.name)));
        }
        if !d.path.is_absolute() {
            return Err(bad(&format!("the path of \"{}\" must be absolute", d.name)));
        }
    }
    Ok(disks)
}

/// Reads the upload limit in bytes. Unset means the default.
fn parse_max_upload(text: Option<&str>) -> Result<u64, String> {
    match text.map(str::trim) {
        None | Some("") => Ok(DEFAULT_MAX_UPLOAD),
        Some(t) => match t.parse::<u64>() {
            Ok(n) if n > 0 => Ok(n),
            _ => Err("HOMELAB_SERVICES_MAX_UPLOAD must be a number of bytes above 0".to_string()),
        },
    }
}

/// Checks that every disk path is a real folder (not a symlink).
/// A missing one is created (private) if its parent folder exists.
pub fn prepare_storage(disks: &[Disk]) -> Result<(), String> {
    for d in disks {
        let path = &d.path;
        match std::fs::symlink_metadata(path) {
            Ok(m) if m.is_dir() => {}
            Ok(_) => {
                return Err(format!(
                    "disk \"{}\": {} is not a folder (links are not allowed)",
                    d.name,
                    path.display()
                ))
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                if !path.parent().is_some_and(|p| p.is_dir()) {
                    return Err(format!(
                        "disk \"{}\": {} is missing and so is its parent folder",
                        d.name,
                        path.display()
                    ));
                }
                crate::disk::ensure_dir(path).map_err(|e| {
                    format!("disk \"{}\": cannot create {}: {e}", d.name, path.display())
                })?;
            }
            Err(e) => return Err(format!("disk \"{}\": {}: {e}", d.name, path.display())),
        }
    }
    Ok(())
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

    const DISKS: &str = r#"[{"name":"main","path":"/mnt/storage"}]"#;

    fn ok(listen: &str, key: &str) -> Result<Config, String> {
        Config::parse(listen, "/tmp/x", key, DISKS, None, None, None)
    }

    fn storage(text: &str) -> Result<Vec<Disk>, String> {
        parse_storage(text)
    }

    #[test]
    fn accepts_loopback_v4_and_v6() {
        assert!(ok("127.0.0.1:8080", &test_key_text()).is_ok());
        assert!(ok("127.5.5.5:1", &test_key_text()).is_ok());
        assert!(ok("[::1]:8080", &test_key_text()).is_ok());
    }

    #[test]
    fn refuses_non_loopback_listen() {
        for addr in [
            "0.0.0.0:8080",
            "192.168.1.5:8080",
            "[::]:8080",
            "8.8.8.8:80",
        ] {
            let err = ok(addr, &test_key_text()).unwrap_err();
            assert!(err.contains("loopback"), "{addr}: {err}");
        }
    }

    #[test]
    fn refuses_unparsable_listen() {
        assert!(ok("localhost:8080", &test_key_text()).is_err());
        assert!(ok("127.0.0.1", &test_key_text()).is_err());
    }

    #[test]
    fn refuses_bad_keys() {
        let short = URL_SAFE_NO_PAD.encode([1u8; 31]);
        for key in ["", "not base64 !!", "AAAA", short.as_str()] {
            let err = ok("127.0.0.1:8080", key).unwrap_err();
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

    #[test]
    fn reads_storage() {
        let disks =
            storage(r#"[{"name":"main","path":"/mnt/a"},{"name":"b_2","path":"/b"}]"#).unwrap();
        assert_eq!(disks.len(), 2);
        assert_eq!(disks[0].name, "main");
        assert_eq!(disks[1].path, PathBuf::from("/b"));
    }

    #[test]
    fn refuses_bad_storage() {
        for text in [
            "",
            "not json",
            "[]",
            r#"{"name":"a","path":"/a"}"#,
            r#"[{"name":"Main","path":"/a"}]"#,
            r#"[{"name":"","path":"/a"}]"#,
            r#"[{"name":"a","path":"relative/path"}]"#,
            r#"[{"name":"a","path":"/a"},{"name":"a","path":"/b"}]"#,
            r#"[{"name":"a"}]"#,
            r#"[{"name":"a","path":"/a","extra":1}]"#,
        ] {
            let err = storage(text).unwrap_err();
            assert!(err.contains("HOMELAB_SERVICES_STORAGE"), "{text}: {err}");
        }
    }

    #[test]
    fn secrets_paths_are_optional_and_absolute() {
        let parse = |key: Option<&str>, import: Option<&str>| {
            Config::parse(
                "127.0.0.1:8080",
                "/tmp/x",
                &test_key_text(),
                DISKS,
                None,
                key,
                import,
            )
        };
        let c = parse(None, None).unwrap();
        assert!(c.secrets_key.is_none() && c.secrets_import.is_none());
        let c = parse(Some(" /h/secrets/master.key "), Some("")).unwrap();
        assert_eq!(c.secrets_key, Some(PathBuf::from("/h/secrets/master.key")));
        assert!(c.secrets_import.is_none());
        let err = parse(Some("master.key"), None).unwrap_err();
        assert!(err.contains("HOMELAB_SERVICES_SECRETS_KEY"), "{err}");
        let err = parse(None, Some("relative/secrets.db")).unwrap_err();
        assert!(err.contains("HOMELAB_SERVICES_SECRETS_IMPORT"), "{err}");
    }

    #[test]
    fn max_upload_default_and_errors() {
        assert_eq!(parse_max_upload(None).unwrap(), 10 * 1024 * 1024 * 1024);
        assert_eq!(parse_max_upload(Some(" 500 ")).unwrap(), 500);
        for bad in ["0", "-1", "ten", "1.5"] {
            assert!(parse_max_upload(Some(bad)).is_err(), "{bad}");
        }
    }

    #[test]
    fn storage_folders_are_checked_and_created() {
        let base = tempfile::tempdir().unwrap();
        let disk = |name: &str, path: PathBuf| Disk {
            name: name.to_string(),
            path,
        };
        // A missing folder whose parent exists is created.
        let new = base.path().join("new");
        prepare_storage(&[disk("a", new.clone())]).unwrap();
        assert!(new.is_dir());
        // Existing is fine.
        prepare_storage(&[disk("a", new.clone())]).unwrap();
        // Missing parent is an error.
        assert!(prepare_storage(&[disk("a", base.path().join("x/y"))]).is_err());
        // A file is an error.
        let file = base.path().join("file");
        std::fs::write(&file, "x").unwrap();
        assert!(prepare_storage(&[disk("a", file)]).is_err());
        // A symlink to a folder is an error.
        let link = base.path().join("link");
        std::os::unix::fs::symlink(&new, &link).unwrap();
        assert!(prepare_storage(&[disk("a", link)]).is_err());
    }
}
