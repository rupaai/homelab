//! Helpers shared by the tests: fixed keys and a signer that acts like homelabd.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{HeaderMap, HeaderValue, Request};
use axum::Router;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ed25519_dalek::{Signer, SigningKey};

use crate::config::Disk;
use crate::dbs::Dbs;
use crate::files::Files;
use crate::secrets::Secrets;

use crate::app::{app, AppState};
use crate::caller::{CALLER_HEADER, SIGNATURE_HEADER, SIGNING_PREFIX};

/// The "homelabd" key used by most tests.
pub fn test_key() -> SigningKey {
    SigningKey::from_bytes(&[7u8; 32])
}

/// A different key, for forgery tests.
pub fn other_key() -> SigningKey {
    SigningKey::from_bytes(&[9u8; 32])
}

/// The public half of `test_key`, as it appears in the environment.
pub fn test_key_text() -> String {
    URL_SAFE_NO_PAD.encode(test_key().verifying_key().to_bytes())
}

/// A valid caller object for the given request.
pub fn caller_json(method: &str, path: &str, ts: i64) -> serde_json::Value {
    serde_json::json!({
        "v": 1,
        "kind": "app",
        "person": "p1",
        "key": "",
        "app": "notes",
        "env": "dev",
        "scopes": ["files:read"],
        "method": method,
        "path": path,
        "ts": ts,
    })
}

/// Headers that carry `text` as the caller header, signed by `key`.
pub fn headers_for_text(key: &SigningKey, text: &str) -> HeaderMap {
    let mut message = SIGNING_PREFIX.to_vec();
    message.extend_from_slice(text.as_bytes());
    let signature = key.sign(&message);

    let mut headers = HeaderMap::new();
    headers.insert(CALLER_HEADER, HeaderValue::from_str(text).unwrap());
    headers.insert(
        SIGNATURE_HEADER,
        HeaderValue::from_str(&URL_SAFE_NO_PAD.encode(signature.to_bytes())).unwrap(),
    );
    headers
}

/// Headers for a caller object, signed by `key`.
pub fn signed_headers(key: &SigningKey, caller: &serde_json::Value) -> HeaderMap {
    let text = URL_SAFE_NO_PAD.encode(serde_json::to_vec(caller).unwrap());
    headers_for_text(key, &text)
}

/// A person (a signed-in Homelab admin).
pub fn person() -> serde_json::Value {
    serde_json::json!({ "v": 1, "kind": "person", "person": "p1" })
}

/// An admin API key with the given scopes.
pub fn admin(scopes: &[&str]) -> serde_json::Value {
    serde_json::json!({ "v": 1, "kind": "admin", "key": "k1", "scopes": scopes })
}

/// An app key.
pub fn app_key(app: &str, env: &str) -> serde_json::Value {
    serde_json::json!({ "v": 1, "kind": "app", "key": "k2", "app": app, "env": env })
}

/// homelabd itself.
pub fn system() -> serde_json::Value {
    serde_json::json!({ "v": 1, "kind": "system" })
}

/// An app key with scopes.
pub fn app_scoped(app: &str, env: &str, scopes: &[&str]) -> serde_json::Value {
    serde_json::json!({ "v": 1, "kind": "app", "key": "k2", "app": app, "env": env, "scopes": scopes })
}

/// Writes a master key file the way homelabd does (base64, then a new line, mode 0600).
pub fn write_master_key(path: &std::path::Path, key: &[u8]) {
    std::fs::write(
        path,
        format!(
            "{}\n",
            base64::engine::general_purpose::STANDARD.encode(key)
        ),
    )
    .unwrap();
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600)).unwrap();
}

/// The master key most tests use.
pub const TEST_MASTER_KEY: [u8; 32] = [3u8; 32];

/// A signed request from `who`, with extra headers.
pub fn request(
    who: &serde_json::Value,
    method: &str,
    target: &str,
    body: Vec<u8>,
    extra: &[(&str, &str)],
) -> Request<Body> {
    let mut caller = who.clone();
    caller["method"] = method.into();
    caller["path"] = target.into();
    caller["ts"] = crate::app::unix_now().into();
    let mut req = Request::builder().method(method).uri(target);
    for (name, value) in signed_headers(&test_key(), &caller).iter() {
        req = req.header(name, value);
    }
    for (name, value) in extra {
        req = req.header(*name, *value);
    }
    req.body(Body::from(body)).unwrap()
}

/// A router with its own temp data folder and one disk called "main".
pub struct TestEnv {
    pub dir: tempfile::TempDir,
    pub disk: std::path::PathBuf,
    pub files: Arc<Files>,
    pub secrets: Arc<Secrets>,
    pub dbs: Arc<Dbs>,
    pub router: Router,
}

pub fn test_env(max_upload: u64) -> TestEnv {
    let dir = tempfile::tempdir().unwrap();
    let data = dir.path().join("data");
    let disk = dir.path().join("disk");
    std::fs::create_dir(&data).unwrap();
    std::fs::create_dir(&disk).unwrap();
    let disks = vec![Disk {
        name: "main".to_string(),
        path: disk.clone(),
    }];
    let files = Arc::new(Files::open(&data, disks, max_upload).unwrap());
    let key = data.join("master.key");
    write_master_key(&key, &TEST_MASTER_KEY);
    let secrets = Arc::new(Secrets::open(&data.join("secrets.db"), &key, None).unwrap());
    let dbs = Arc::new(Dbs::open(&data.join("dbs.db")).unwrap());
    let router = app(AppState {
        key: Arc::new(test_key().verifying_key()),
        version: "test",
        files: files.clone(),
        secrets: Some(secrets.clone()),
        secrets_problem: String::new(),
        dbs: dbs.clone(),
    });
    TestEnv {
        dir,
        disk,
        files,
        secrets,
        dbs,
        router,
    }
}
