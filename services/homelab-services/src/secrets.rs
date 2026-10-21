//! The encrypted secrets store.
//!
//! One SQLite file (`secrets.db` in the data folder). Every value is sealed with
//! AES-256-GCM under the master key, with its own random 96-bit nonce.
//!
//! The sealed value is tied to the place it lives in. The additional data (AAD) is
//!
//! ```text
//! "homelab-secret-v2" 0x00 app 0x00 env 0x00 name 0x00 kind 0x00 version
//! ```
//!
//! where `0x00` is a zero byte, `kind` is `config` or `app`, and `version` is the version
//! number in decimal. A sealed value copied onto another secret, another kind or another
//! version does not open. None of the parts can contain a zero byte, so the parts can't
//! slide into each other.
//!
//! The last five versions of each secret are kept. The current value is the newest kept
//! version, so nothing is sealed twice.
//!
//! The first time it starts, it imports the old store made by homelabd (Go) with the same
//! master key. See `import_go`.

use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Mutex;

use aes_gcm::aead::{Aead, Generate, Nonce, Payload};
use aes_gcm::{Aes256Gcm, Key, KeyInit};
use base64::engine::general_purpose::STANDARD;
use base64::Engine;
use rusqlite::{params, Connection, OpenFlags, OptionalExtension};
use zeroize::{Zeroize, Zeroizing};

use crate::disk::valid_name;

/// How many versions of each secret are kept.
pub const KEPT: i64 = 5;
/// The longest config secret value, in bytes.
pub const MAX_CONFIG_VALUE: usize = 8192;
/// The longest app secret value, in bytes.
pub const MAX_APP_VALUE: usize = 64 * 1024;
/// How many app secrets one app environment may have.
pub const MAX_APP_SECRETS: i64 = 10_000;
/// Names starting with this are Homelab's own (for example the app's API key).
pub const RESERVED_PREFIX: &str = "HOMELAB_";

const AAD_PREFIX: &str = "homelab-secret-v2";
/// The AAD that homelabd (Go) used for a current value: `aad()` in secretstore.go.
const GO_AAD_PREFIX: &str = "homelab-secret-v1";
/// The AAD that homelabd (Go) used for a kept version: `versionAAD()` in secretstore.go.
const GO_VERSION_AAD_PREFIX: &str = "homelab-secret-version-v1";

/// Who made a secret.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    /// Set by the owner, given to the app at deploy.
    Config,
    /// Made by the app while it runs.
    App,
}

impl Kind {
    pub fn as_str(self) -> &'static str {
        match self {
            Kind::Config => "config",
            Kind::App => "app",
        }
    }

    pub fn parse(text: &str) -> Option<Kind> {
        match text {
            "config" => Some(Kind::Config),
            "app" => Some(Kind::App),
            _ => None,
        }
    }
}

/// What went wrong. Messages are plain and never contain a value.
#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    /// The request is not valid (name, value, place).
    Invalid(String),
    /// There is no such secret or version.
    Missing(String),
    /// The name is taken by the other kind, or a limit is reached.
    Conflict(String),
    /// The caller may not change this secret.
    Forbidden(String),
    /// A sealed value did not open. Something changed it, or the key is wrong.
    Seal,
    /// The database failed. Logged, not shown.
    Db(String),
}

impl From<rusqlite::Error> for Error {
    fn from(e: rusqlite::Error) -> Error {
        Error::Db(e.to_string())
    }
}

/// One secret, without its value.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Info {
    pub name: String,
    pub kind: Kind,
    pub version: i64,
    /// Unix time in milliseconds.
    pub updated_at: i64,
    pub updated_by: String,
}

/// One kept version, without its value.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct VersionInfo {
    pub version: i64,
    /// Unix time in milliseconds.
    pub set_at: i64,
    pub set_by: String,
}

/// One value, opened.
pub struct Opened {
    pub kind: Kind,
    pub version: i64,
    pub value: Zeroizing<String>,
}

/// What an import did.
#[derive(Debug, PartialEq, Eq)]
pub struct Imported {
    pub secrets: usize,
    pub versions: usize,
}

/// The store. Calls are blocking: run them on a blocking thread.
pub struct Secrets {
    conn: Mutex<Connection>,
    sealer: Sealer,
    /// What the first start imported, for the log.
    pub imported: Option<Imported>,
}

/// Seals and opens values under the master key. The key is wiped when this is dropped.
struct Sealer {
    cipher: Aes256Gcm,
}

// ---- names and places ----

/// Config secret names are environment variable names.
pub fn valid_config_name(name: &str) -> bool {
    let b = name.as_bytes();
    !b.is_empty()
        && (b[0].is_ascii_alphabetic() || b[0] == b'_')
        && b.iter().all(|c| c.is_ascii_alphanumeric() || *c == b'_')
}

/// App secret names: 1 to 200 of letters, digits and `_ . : -`. A name that is only dots
/// is refused, because it can't be written safely in an address.
pub fn valid_app_name(name: &str) -> bool {
    let b = name.as_bytes();
    !b.is_empty()
        && b.len() <= 200
        && name != "."
        && name != ".."
        && b.iter()
            .all(|c| c.is_ascii_alphanumeric() || matches!(*c, b'_' | b'.' | b':' | b'-'))
}

pub fn is_reserved(name: &str) -> bool {
    name.starts_with(RESERVED_PREFIX)
}

/// Checks where a secret lives. App "" with env "" is the platform.
fn check_place(app: &str, env: &str) -> Result<(), Error> {
    if app.is_empty() {
        if !env.is_empty() {
            return Err(Error::Invalid(
                "Platform secrets have no environment.".into(),
            ));
        }
        return Ok(());
    }
    if !valid_name(app, 63) {
        return Err(Error::Invalid("That isn't a valid app name.".into()));
    }
    if env != "dev" && env != "prod" {
        return Err(Error::Invalid("The environment is dev or prod.".into()));
    }
    Ok(())
}

fn check_name_and_value(app: &str, kind: Kind, name: &str, value: &str) -> Result<(), Error> {
    match kind {
        Kind::Config => {
            if !valid_config_name(name) {
                return Err(Error::Invalid(
                    "A config secret name uses letters, digits and _, and doesn't start with a digit.".into(),
                ));
            }
            if value.is_empty() || value.len() > MAX_CONFIG_VALUE {
                return Err(Error::Invalid(format!(
                    "A value must be 1 to {MAX_CONFIG_VALUE} bytes."
                )));
            }
            if value.contains(['\n', '\r', '\0']) {
                return Err(Error::Invalid("A value can't contain line breaks.".into()));
            }
        }
        Kind::App => {
            if app.is_empty() {
                return Err(Error::Invalid(
                    "Platform secrets are config secrets.".into(),
                ));
            }
            if !valid_app_name(name) {
                return Err(Error::Invalid(
                    "A secret name is 1 to 200 letters, digits and _ . : -".into(),
                ));
            }
            if value.is_empty() || value.len() > MAX_APP_VALUE {
                return Err(Error::Invalid(format!(
                    "A value must be 1 to {MAX_APP_VALUE} bytes."
                )));
            }
        }
    }
    Ok(())
}

// ---- the master key ----

/// Reads the master key: the file homelabd keeps in `secrets/master.key`, base64 of
/// 32 bytes. The file must be private (no group or other access).
pub fn load_master_key(path: &Path) -> Result<Zeroizing<Vec<u8>>, String> {
    let meta = std::fs::metadata(path).map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            format!(
                "the master key {} is missing. Put the key file back (from your copy)",
                path.display()
            )
        } else {
            format!("cannot read the master key {}: {e}", path.display())
        }
    })?;
    if !meta.is_file() {
        return Err(format!("the master key {} isn't a file", path.display()));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if meta.permissions().mode() & 0o077 != 0 {
            return Err(format!(
                "the master key {} can be read by other users. Run: chmod 600 {}",
                path.display(),
                path.display()
            ));
        }
    }
    if meta.len() > 4096 {
        return Err(format!(
            "the master key {} isn't a valid key (32 bytes, base64)",
            path.display()
        ));
    }
    let text = Zeroizing::new(
        std::fs::read(path)
            .map_err(|e| format!("cannot read the master key {}: {e}", path.display()))?,
    );
    let bad = || {
        format!(
            "the master key {} isn't a valid key (32 bytes, base64)",
            path.display()
        )
    };
    let text = std::str::from_utf8(&text).map_err(|_| bad())?;
    let key = Zeroizing::new(STANDARD.decode(text.trim()).map_err(|_| bad())?);
    if key.len() != 32 {
        return Err(bad());
    }
    Ok(key)
}

// ---- sealing ----

fn aad(app: &str, env: &str, name: &str, kind: Kind, version: i64) -> Vec<u8> {
    format!(
        "{AAD_PREFIX}\0{app}\0{env}\0{name}\0{}\0{version}",
        kind.as_str()
    )
    .into_bytes()
}

/// The AAD homelabd (Go) used for a current value.
fn go_aad(app: &str, env: &str, name: &str) -> Vec<u8> {
    format!("{GO_AAD_PREFIX}\0{app}\0{env}\0{name}").into_bytes()
}

/// The AAD homelabd (Go) used for a kept version.
fn go_version_aad(app: &str, env: &str, name: &str, version: i64) -> Vec<u8> {
    format!("{GO_VERSION_AAD_PREFIX}\0{app}\0{env}\0{name}\0{version}").into_bytes()
}

impl Sealer {
    fn seal(&self, ad: &[u8], value: &str) -> Result<(Vec<u8>, Vec<u8>), Error> {
        let nonce = Nonce::<Aes256Gcm>::generate();
        let sealed = self
            .cipher
            .encrypt(
                &nonce,
                Payload {
                    msg: value.as_bytes(),
                    aad: ad,
                },
            )
            .map_err(|_| Error::Seal)?;
        Ok((nonce.to_vec(), sealed))
    }

    fn open_with(
        &self,
        ad: &[u8],
        nonce: &[u8],
        sealed: &[u8],
    ) -> Result<Zeroizing<String>, Error> {
        if nonce.len() != 12 {
            return Err(Error::Seal);
        }
        let plain = self
            .cipher
            .decrypt(
                &Nonce::<Aes256Gcm>::try_from(nonce).map_err(|_| Error::Seal)?,
                Payload {
                    msg: sealed,
                    aad: ad,
                },
            )
            .map_err(|_| Error::Seal)?;
        let mut plain = Zeroizing::new(plain);
        let text = String::from_utf8(std::mem::take(&mut *plain)).map_err(|e| {
            let mut bytes = e.into_bytes();
            bytes.zeroize();
            Error::Seal
        })?;
        Ok(Zeroizing::new(text))
    }
}

impl Secrets {
    /// Opens (and if needed makes or upgrades) the store.
    ///
    /// `key_path` is the master key file. `import_path` is the old Go store, imported once.
    pub fn open(
        db_path: &Path,
        key_path: &Path,
        import_path: Option<&Path>,
    ) -> Result<Secrets, String> {
        let key = load_master_key(key_path)?;
        let sealer = Sealer {
            cipher: Aes256Gcm::new(
                &Key::<Aes256Gcm>::try_from(&key[..])
                    .map_err(|_| "the master key is not 32 bytes".to_string())?,
            ),
        };
        drop(key);
        let mut conn = Connection::open(db_path).map_err(|e| {
            format!(
                "cannot open the secrets database {}: {e}",
                db_path.display()
            )
        })?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(db_path, std::fs::Permissions::from_mode(0o600));
        }
        conn.execute_batch(
            "PRAGMA journal_mode = WAL; PRAGMA synchronous = FULL; PRAGMA busy_timeout = 5000;",
        )
        .and_then(|_| migrate(&conn))
        .map_err(|e| format!("cannot set up the secrets database: {e}"))?;
        let done: Option<String> = conn
            .query_row("SELECT value FROM meta WHERE key = 'imported'", [], |r| {
                r.get(0)
            })
            .optional()
            .map_err(|e| e.to_string())?;
        let imported = if done.is_none() {
            Some(import_go(&sealer, &mut conn, import_path)?)
        } else {
            None
        };
        let store = Secrets {
            conn: Mutex::new(conn),
            sealer,
            imported,
        };
        store.check_key()?;
        Ok(store)
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, Connection> {
        self.conn.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn seal(&self, ad: &[u8], value: &str) -> Result<(Vec<u8>, Vec<u8>), Error> {
        self.sealer.seal(ad, value)
    }

    fn open_with(
        &self,
        ad: &[u8],
        nonce: &[u8],
        sealed: &[u8],
    ) -> Result<Zeroizing<String>, Error> {
        self.sealer.open_with(ad, nonce, sealed)
    }

    /// Proves the master key opens what is stored, so a wrong key fails at start and not
    /// at a deploy.
    fn check_key(&self) -> Result<(), String> {
        let conn = self.lock();
        type KeyRow = (String, String, String, String, i64, Vec<u8>, Vec<u8>);
        let row: Option<KeyRow> = conn
            .query_row(
                "SELECT s.app, s.env, s.name, s.kind, s.version, v.nonce, v.sealed
                 FROM secrets s JOIN secret_versions v
                   ON v.app = s.app AND v.env = s.env AND v.name = s.name AND v.version = s.version
                 LIMIT 1",
                [],
                |r| {
                    Ok((
                        r.get(0)?,
                        r.get(1)?,
                        r.get(2)?,
                        r.get(3)?,
                        r.get(4)?,
                        r.get(5)?,
                        r.get(6)?,
                    ))
                },
            )
            .optional()
            .map_err(|e| e.to_string())?;
        if let Some((app, env, name, kind, version, nonce, sealed)) = row {
            let kind = Kind::parse(&kind).ok_or("the secrets database is damaged")?;
            if self
                .open_with(&aad(&app, &env, &name, kind, version), &nonce, &sealed)
                .is_err()
            {
                return Err("the master key doesn't open the secrets already stored. \
                            Put back the key that was used with them"
                    .to_string());
            }
        }
        Ok(())
    }

    // ---- reading ----

    /// Describes the secrets of app/env, by name. `kind` limits it to one kind.
    pub fn list(&self, app: &str, env: &str, kind: Option<Kind>) -> Result<Vec<Info>, Error> {
        check_place(app, env)?;
        let conn = self.lock();
        let mut stmt = conn.prepare(
            "SELECT name, kind, version, updated_at, updated_by FROM secrets
             WHERE app = ? AND env = ? ORDER BY name",
        )?;
        let rows = stmt.query_map(params![app, env], info_from)?;
        let mut out = Vec::new();
        for row in rows {
            let info = row?;
            if kind.is_none_or(|k| k == info.kind) {
                out.push(info);
            }
        }
        Ok(out)
    }

    /// Describes one secret.
    pub fn info(&self, app: &str, env: &str, name: &str) -> Result<Option<Info>, Error> {
        check_place(app, env)?;
        let conn = self.lock();
        Ok(conn
            .query_row(
                "SELECT name, kind, version, updated_at, updated_by FROM secrets
                 WHERE app = ? AND env = ? AND name = ?",
                params![app, env, name],
                info_from,
            )
            .optional()?)
    }

    /// Opens one value: the current one, or a kept version. `None` when there is none.
    pub fn get(
        &self,
        app: &str,
        env: &str,
        name: &str,
        version: Option<i64>,
    ) -> Result<Option<Opened>, Error> {
        check_place(app, env)?;
        let conn = self.lock();
        let Some((kind, current)) = secret_row(&conn, app, env, name)? else {
            return Ok(None);
        };
        let version = version.unwrap_or(current);
        let row: Option<(Vec<u8>, Vec<u8>)> = conn
            .query_row(
                "SELECT nonce, sealed FROM secret_versions
                 WHERE app = ? AND env = ? AND name = ? AND version = ?",
                params![app, env, name, version],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .optional()?;
        let Some((nonce, sealed)) = row else {
            return Ok(None);
        };
        let value = self.open_with(&aad(app, env, name, kind, version), &nonce, &sealed)?;
        Ok(Some(Opened {
            kind,
            version,
            value,
        }))
    }

    /// The kept versions of a secret, newest first, without values.
    pub fn versions(&self, app: &str, env: &str, name: &str) -> Result<Vec<VersionInfo>, Error> {
        check_place(app, env)?;
        let conn = self.lock();
        let mut stmt = conn.prepare(
            "SELECT version, set_at, set_by FROM secret_versions
             WHERE app = ? AND env = ? AND name = ? ORDER BY version DESC",
        )?;
        let rows = stmt.query_map(params![app, env, name], |r| {
            Ok(VersionInfo {
                version: r.get(0)?,
                set_at: r.get(1)?,
                set_by: r.get(2)?,
            })
        })?;
        Ok(rows.collect::<Result<Vec<_>, _>>()?)
    }

    /// Which of `names` have no config value for app/env.
    pub fn missing(&self, app: &str, env: &str, names: &[String]) -> Result<Vec<String>, Error> {
        let have: Vec<String> = self
            .list(app, env, Some(Kind::Config))?
            .into_iter()
            .map(|i| i.name)
            .collect();
        Ok(names
            .iter()
            .filter(|n| !have.contains(n))
            .cloned()
            .collect())
    }

    /// The config values of `names` that are set for app/env, for a deploy. App secrets
    /// are never given out this way.
    pub fn values_for_deploy(
        &self,
        app: &str,
        env: &str,
        names: &[String],
    ) -> Result<BTreeMap<String, Zeroizing<String>>, Error> {
        let mut out = BTreeMap::new();
        for name in names {
            if let Some(o) = self.get(app, env, name, None)? {
                if o.kind == Kind::Config {
                    out.insert(name.clone(), o.value);
                }
            }
        }
        Ok(out)
    }

    // ---- writing ----

    /// Stores a new value as the next version.
    ///
    /// A name can be one kind only: setting an app secret's name as config is a conflict,
    /// and setting a config secret's name as an app secret is refused.
    #[allow(clippy::too_many_arguments)]
    pub fn put(
        &self,
        app: &str,
        env: &str,
        name: &str,
        kind: Kind,
        value: &str,
        by: &str,
        now_ms: i64,
    ) -> Result<i64, Error> {
        check_place(app, env)?;
        check_name_and_value(app, kind, name, value)?;
        let mut conn = self.lock();
        let tx = conn.transaction()?;
        let version = self.put_in(&tx, app, env, name, kind, value, by, now_ms)?;
        tx.commit()?;
        Ok(version)
    }

    #[allow(clippy::too_many_arguments)]
    fn put_in(
        &self,
        tx: &rusqlite::Transaction,
        app: &str,
        env: &str,
        name: &str,
        kind: Kind,
        value: &str,
        by: &str,
        now_ms: i64,
    ) -> Result<i64, Error> {
        let existing = secret_row(tx, app, env, name)?;
        let version = match existing {
            Some((k, _)) if k != Kind::Config && kind == Kind::Config => {
                return Err(Error::Conflict(
                    "The app already saved a secret with that name. Delete that one first.".into(),
                ))
            }
            Some((k, _)) if k != kind => {
                return Err(Error::Forbidden(
                    "That name is a config secret, set by the owner. An app can't change it."
                        .into(),
                ))
            }
            Some((_, v)) => v + 1,
            None => {
                if kind == Kind::App {
                    let n: i64 = tx.query_row(
                        "SELECT COUNT(*) FROM secrets WHERE app = ? AND env = ? AND kind = 'app'",
                        params![app, env],
                        |r| r.get(0),
                    )?;
                    if n >= MAX_APP_SECRETS {
                        return Err(Error::Conflict(format!(
                            "An app can save up to {MAX_APP_SECRETS} secrets."
                        )));
                    }
                }
                1
            }
        };
        let (nonce, sealed) = self.seal(&aad(app, env, name, kind, version), value)?;
        tx.execute(
            "INSERT INTO secret_versions (app, env, name, version, nonce, sealed, set_at, set_by)
             VALUES (?,?,?,?,?,?,?,?)",
            params![app, env, name, version, nonce, sealed, now_ms, by],
        )?;
        tx.execute(
            "INSERT INTO secrets (app, env, name, kind, version, updated_at, updated_by)
             VALUES (?,?,?,?,?,?,?)
             ON CONFLICT (app, env, name) DO UPDATE SET
               version = excluded.version, updated_at = excluded.updated_at,
               updated_by = excluded.updated_by",
            params![app, env, name, kind.as_str(), version, now_ms, by],
        )?;
        tx.execute(
            "DELETE FROM secret_versions WHERE app = ? AND env = ? AND name = ? AND version <= ?",
            params![app, env, name, version - KEPT],
        )?;
        Ok(version)
    }

    /// Puts a kept version's value back as a new version.
    pub fn restore(
        &self,
        app: &str,
        env: &str,
        name: &str,
        version: i64,
        by: &str,
        now_ms: i64,
    ) -> Result<i64, Error> {
        check_place(app, env)?;
        let Some(old) = self.get(app, env, name, Some(version))? else {
            return Err(Error::Missing(format!(
                "There's no version {version} of {name}."
            )));
        };
        // The value is checked again against the rules of its kind on the way back in.
        self.put(app, env, name, old.kind, &old.value, by, now_ms)
    }

    /// Removes a secret and its versions. With `only` set, only if it is that kind:
    /// for an app (`App`) a config secret is refused; for `Config` an app secret of that
    /// name is left alone. Returns whether anything was removed.
    pub fn delete(
        &self,
        app: &str,
        env: &str,
        name: &str,
        only: Option<Kind>,
    ) -> Result<bool, Error> {
        check_place(app, env)?;
        let mut conn = self.lock();
        let tx = conn.transaction()?;
        let Some((kind, _)) = secret_row(&tx, app, env, name)? else {
            return Ok(false);
        };
        match only {
            Some(k) if k != kind => {
                // An app can't remove what the owner set. Asking for a config secret by
                // name finds no config secret when the name is an app's: nothing to remove.
                return if k == Kind::App {
                    Err(Error::Forbidden(
                        "That secret was set by the owner. An app can't delete it.".into(),
                    ))
                } else {
                    Ok(false)
                };
            }
            _ => {}
        }
        tx.execute(
            "DELETE FROM secret_versions WHERE app = ? AND env = ? AND name = ?",
            params![app, env, name],
        )?;
        tx.execute(
            "DELETE FROM secrets WHERE app = ? AND env = ? AND name = ?",
            params![app, env, name],
        )?;
        tx.commit()?;
        Ok(true)
    }

    /// Removes every secret of an app, in every environment and of both kinds.
    pub fn delete_app(&self, app: &str) -> Result<usize, Error> {
        if app.is_empty() || !valid_name(app, 63) {
            return Err(Error::Invalid("That isn't a valid app name.".into()));
        }
        let mut conn = self.lock();
        let tx = conn.transaction()?;
        tx.execute("DELETE FROM secret_versions WHERE app = ?", [app])?;
        let n = tx.execute("DELETE FROM secrets WHERE app = ?", [app])?;
        tx.commit()?;
        Ok(n)
    }
}

// ---- the one-time import ----

/// Imports the old store made by homelabd (Go), once.
///
/// The Go file is opened read-only and never changed or removed. Every current value
/// and kept version is decrypted with Go's AAD, sealed again here, and written, with the
/// "imported" mark, in one transaction: either everything is imported or nothing is,
/// and a failed import is tried again at the next start. With no old file the mark is
/// still set, so an old file that shows up later is not imported by surprise.
fn import_go(
    sealer: &Sealer,
    conn: &mut Connection,
    import_path: Option<&Path>,
) -> Result<Imported, String> {
    let mut report = Imported {
        secrets: 0,
        versions: 0,
    };
    let old = match import_path {
        Some(p) if p.is_file() => Some(
            Connection::open_with_flags(p, OpenFlags::SQLITE_OPEN_READ_ONLY).map_err(|e| {
                format!("cannot read the old secrets database {}: {e}", p.display())
            })?,
        ),
        _ => None,
    };
    let tx = conn.transaction().map_err(|e| e.to_string())?;
    if let Some(old) = &old {
        let path = import_path
            .map(|p| p.display().to_string())
            .unwrap_or_default();
        let fail = |what: &str| format!("importing the old secrets from {path}: {what}");
        let wrong_key =
            || fail("the master key doesn't open them. Put back the key that was used with them");
        if has_table(old, "secrets").map_err(|e| fail(&e.to_string()))? {
            let with_version =
                has_column(old, "secrets", "version").map_err(|e| fail(&e.to_string()))?;
            let has_versions =
                has_table(old, "secret_versions").map_err(|e| fail(&e.to_string()))?;
            let sql = if with_version {
                "SELECT app, env, name, nonce, sealed, updated_at, updated_by, version FROM secrets ORDER BY app, env, name"
            } else {
                "SELECT app, env, name, nonce, sealed, updated_at, updated_by, 1 FROM secrets ORDER BY app, env, name"
            };
            let mut stmt = old.prepare(sql).map_err(|e| fail(&e.to_string()))?;
            type Row = (String, String, String, Vec<u8>, Vec<u8>, i64, String, i64);
            let rows: Vec<Row> = stmt
                .query_map([], |r| {
                    Ok((
                        r.get(0)?,
                        r.get(1)?,
                        r.get(2)?,
                        r.get(3)?,
                        r.get(4)?,
                        r.get(5)?,
                        r.get(6)?,
                        r.get(7)?,
                    ))
                })
                .and_then(|it| it.collect())
                .map_err(|e| fail(&e.to_string()))?;
            for (app, env, name, nonce, sealed, at, by, version) in rows {
                let current = sealer
                    .open_with(&go_aad(&app, &env, &name), &nonce, &sealed)
                    .map_err(|_| wrong_key())?;
                // The kept versions, each opened with its own AAD.
                let mut kept: BTreeMap<i64, (i64, String, Zeroizing<String>)> = BTreeMap::new();
                if has_versions {
                    let mut vs = old
                        .prepare(
                            "SELECT version, nonce, sealed, set_at, set_by FROM secret_versions
                             WHERE app = ? AND env = ? AND name = ?",
                        )
                        .map_err(|e| fail(&e.to_string()))?;
                    type VRow = (i64, Vec<u8>, Vec<u8>, i64, String);
                    let vrows: Vec<VRow> = vs
                        .query_map(params![app, env, name], |r| {
                            Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
                        })
                        .and_then(|it| it.collect())
                        .map_err(|e| fail(&e.to_string()))?;
                    for (v, n, s, set_at, set_by) in vrows {
                        let value = sealer
                            .open_with(&go_version_aad(&app, &env, &name, v), &n, &s)
                            .map_err(|_| wrong_key())?;
                        kept.insert(v, (set_at, set_by, value));
                    }
                }
                // The current value is always the newest kept version.
                kept.entry(version)
                    .or_insert_with(|| (at, by.clone(), current.clone()));
                while kept.len() as i64 > KEPT {
                    kept.pop_first();
                }
                for (v, (set_at, set_by, value)) in &kept {
                    let (n, s) = sealer
                        .seal(&aad(&app, &env, &name, Kind::Config, *v), value)
                        .map_err(|_| fail("sealing failed"))?;
                    tx.execute(
                        "INSERT INTO secret_versions (app, env, name, version, nonce, sealed, set_at, set_by)
                         VALUES (?,?,?,?,?,?,?,?)",
                        params![app, env, name, v, n, s, set_at, set_by],
                    )
                    .map_err(|e| fail(&e.to_string()))?;
                    report.versions += 1;
                }
                tx.execute(
                    "INSERT INTO secrets (app, env, name, kind, version, updated_at, updated_by)
                     VALUES (?,?,?,?,?,?,?)",
                    params![app, env, name, "config", version, at, by],
                )
                .map_err(|e| fail(&e.to_string()))?;
                report.secrets += 1;
            }
        }
    }
    let note = if old.is_some() {
        format!("{} secrets, {} versions", report.secrets, report.versions)
    } else {
        "nothing to import".to_string()
    };
    tx.execute(
        "INSERT INTO meta (key, value) VALUES ('imported', ?)",
        [note],
    )
    .map_err(|e| e.to_string())?;
    tx.commit().map_err(|e| e.to_string())?;
    Ok(report)
}

fn info_from(r: &rusqlite::Row) -> rusqlite::Result<Info> {
    let kind: String = r.get(1)?;
    Ok(Info {
        name: r.get(0)?,
        kind: Kind::parse(&kind).unwrap_or(Kind::Config),
        version: r.get(2)?,
        updated_at: r.get(3)?,
        updated_by: r.get(4)?,
    })
}

/// The kind and current version of a secret, if it exists.
fn secret_row(
    conn: &Connection,
    app: &str,
    env: &str,
    name: &str,
) -> Result<Option<(Kind, i64)>, Error> {
    let row: Option<(String, i64)> = conn
        .query_row(
            "SELECT kind, version FROM secrets WHERE app = ? AND env = ? AND name = ?",
            params![app, env, name],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .optional()?;
    match row {
        None => Ok(None),
        Some((k, v)) => Kind::parse(&k)
            .map(|k| Some((k, v)))
            .ok_or_else(|| Error::Db("the secrets database is damaged".into())),
    }
}

fn has_table(conn: &Connection, table: &str) -> rusqlite::Result<bool> {
    conn.query_row(
        "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
        [table],
        |r| r.get::<_, i64>(0),
    )
    .map(|n| n > 0)
}

fn has_column(conn: &Connection, table: &str, column: &str) -> rusqlite::Result<bool> {
    let mut stmt = conn.prepare(&format!("PRAGMA table_info({table})"))?;
    let names = stmt.query_map([], |r| r.get::<_, String>(1))?;
    for n in names {
        if n? == column {
            return Ok(true);
        }
    }
    Ok(false)
}

/// Brings the schema up to date, using `user_version`.
fn migrate(conn: &Connection) -> rusqlite::Result<()> {
    let version: i64 = conn.query_row("PRAGMA user_version", [], |r| r.get(0))?;
    if version < 1 {
        conn.execute_batch(
            "BEGIN;
             CREATE TABLE secrets(
               app TEXT NOT NULL, env TEXT NOT NULL, name TEXT NOT NULL,
               kind TEXT NOT NULL CHECK (kind IN ('config', 'app')),
               version INTEGER NOT NULL,
               updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL,
               PRIMARY KEY (app, env, name));
             CREATE TABLE secret_versions(
               app TEXT NOT NULL, env TEXT NOT NULL, name TEXT NOT NULL, version INTEGER NOT NULL,
               nonce BLOB NOT NULL, sealed BLOB NOT NULL,
               set_at INTEGER NOT NULL, set_by TEXT NOT NULL,
               PRIMARY KEY (app, env, name, version));
             CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
             PRAGMA user_version = 1;
             COMMIT;",
        )?;
    }
    Ok(())
}

#[cfg(test)]
mod tests;
