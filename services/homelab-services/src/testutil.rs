//! Helpers shared by the tests: fixed keys and a signer that acts like homelabd.

use axum::http::{HeaderMap, HeaderValue};
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ed25519_dalek::{Signer, SigningKey};

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
