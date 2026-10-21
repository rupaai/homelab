//! Checking who is calling.
//!
//! homelabd sends two headers with every request:
//!
//! * `X-Homelab-Caller`: base64url (no padding) of a JSON object that says who
//!   the caller is and which request it was signed for.
//! * `X-Homelab-Signature`: base64url (no padding) of an ed25519 signature over
//!   `"homelab-services/v1\n"` followed by the exact text of the caller header.
//!
//! `verify` is a plain function so it is easy to test.

use axum::extract::FromRequestParts;
use axum::http::request::Parts;
use axum::http::{HeaderMap, StatusCode};
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use ed25519_dalek::{Signature, VerifyingKey};
use serde::Deserialize;

pub const CALLER_HEADER: &str = "x-homelab-caller";
pub const SIGNATURE_HEADER: &str = "x-homelab-signature";

/// Put in front of the caller header text before signing.
/// It ties the signature to this program and version of the contract.
pub const SIGNING_PREFIX: &[u8] = b"homelab-services/v1\n";

/// The longest caller header we accept, in bytes.
pub const MAX_CALLER_LEN: usize = 8 * 1024;

/// How far the caller's clock may differ from ours, in seconds.
pub const MAX_CLOCK_SKEW: i64 = 30;

/// What kind of caller made the request.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    Person,
    Admin,
    App,
    /// homelabd itself, acting for Homelab (not for a person, key or app). Only homelabd
    /// can sign one: the signing key exists only inside the running homelabd, and homelabd
    /// builds a system caller only for its own calls, never from a request it forwards.
    /// Homelab's own rules (who may see or change secrets) are applied before it calls.
    System,
}

/// The signed description of who is calling and for which request.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Caller {
    /// Contract version. Only 1 is accepted.
    pub v: u32,
    pub kind: Kind,
    /// Person id, or empty.
    #[serde(default)]
    pub person: String,
    /// API key id, or empty.
    #[serde(default)]
    pub key: String,
    /// App id, or empty.
    #[serde(default)]
    pub app: String,
    /// "dev", "prod" or empty.
    #[serde(default)]
    pub env: String,
    #[serde(default)]
    pub scopes: Vec<String>,
    /// The HTTP method this was signed for.
    pub method: String,
    /// The path and query this was signed for, e.g. "/api/files/x?y=1".
    pub path: String,
    /// When it was signed, in unix seconds.
    pub ts: i64,
}

/// Reads a header that must appear exactly once and be plain ASCII.
fn single_header<'a>(headers: &'a HeaderMap, name: &str) -> Result<&'a str, String> {
    let mut values = headers.get_all(name).iter();
    let first = values
        .next()
        .ok_or_else(|| format!("The {name} header is missing."))?;
    if values.next().is_some() {
        return Err(format!("The {name} header was sent more than once."));
    }
    let text = first
        .to_str()
        .map_err(|_| format!("The {name} header is not plain text."))?;
    Ok(text)
}

/// Checks that a request comes from homelabd.
///
/// `now` is the current time in unix seconds. On success it returns the
/// caller; on failure it returns a plain English reason that is safe to show.
pub fn verify(
    headers: &HeaderMap,
    method: &str,
    path_and_query: &str,
    now: i64,
    key: &VerifyingKey,
) -> Result<Caller, String> {
    // 1. Both headers present once, and ASCII. Refuse huge values early.
    let caller_text = single_header(headers, CALLER_HEADER)?;
    let signature_text = single_header(headers, SIGNATURE_HEADER)?;
    if caller_text.len() > MAX_CALLER_LEN {
        return Err("The caller header is too large.".to_string());
    }

    // 2. The signature must be valid for the exact header text.
    let signature_bytes = URL_SAFE_NO_PAD
        .decode(signature_text)
        .map_err(|_| "The signature is not valid base64.".to_string())?;
    let signature = Signature::from_slice(&signature_bytes)
        .map_err(|_| "The signature has the wrong length.".to_string())?;
    let mut signed = Vec::with_capacity(SIGNING_PREFIX.len() + caller_text.len());
    signed.extend_from_slice(SIGNING_PREFIX);
    signed.extend_from_slice(caller_text.as_bytes());
    key.verify_strict(&signed, &signature)
        .map_err(|_| "The signature does not match.".to_string())?;

    // 3. Now that we trust the bytes, read them.
    let json = URL_SAFE_NO_PAD
        .decode(caller_text)
        .map_err(|_| "The caller header is not valid base64.".to_string())?;
    let caller: Caller = serde_json::from_slice(&json)
        .map_err(|_| "The caller header is not in the expected format.".to_string())?;
    if caller.v != 1 {
        return Err("The caller header uses an unknown version.".to_string());
    }

    // 4. It must be fresh (in either direction, to allow for clock drift).
    if caller.ts.abs_diff(now) > MAX_CLOCK_SKEW as u64 {
        return Err("The caller header is too old or too new.".to_string());
    }

    // 5. It must have been signed for this very request.
    if caller.method != method {
        return Err("The caller header was signed for a different method.".to_string());
    }
    if caller.path != path_and_query {
        return Err("The caller header was signed for a different path.".to_string());
    }

    Ok(caller)
}

/// Lets a handler take `Caller` as an argument. The auth middleware has
/// already put it in the request extensions.
impl<S: Send + Sync> FromRequestParts<S> for Caller {
    type Rejection = (StatusCode, &'static str);

    async fn from_request_parts(parts: &mut Parts, _state: &S) -> Result<Self, Self::Rejection> {
        parts
            .extensions
            .get::<Caller>()
            .cloned()
            .ok_or((StatusCode::UNAUTHORIZED, "No caller on this request."))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::*;
    use axum::http::HeaderValue;

    const NOW: i64 = 1_800_000_000;

    fn check(headers: &HeaderMap) -> Result<Caller, String> {
        verify(
            headers,
            "GET",
            "/api/files/x?y=1",
            NOW,
            &test_key().verifying_key(),
        )
    }

    fn good() -> serde_json::Value {
        caller_json("GET", "/api/files/x?y=1", NOW)
    }

    #[test]
    fn accepts_a_good_caller() {
        let caller = check(&signed_headers(&test_key(), &good())).unwrap();
        assert_eq!(caller.kind, Kind::App);
        assert_eq!(caller.app, "notes");
        assert_eq!(caller.scopes, vec!["files:read".to_string()]);
    }

    #[test]
    fn rejects_missing_headers() {
        assert!(check(&HeaderMap::new()).is_err());
        let mut h = signed_headers(&test_key(), &good());
        h.remove(SIGNATURE_HEADER);
        assert!(check(&h).is_err());
    }

    #[test]
    fn rejects_duplicate_headers() {
        let mut h = signed_headers(&test_key(), &good());
        let v = h.get(CALLER_HEADER).unwrap().clone();
        h.append(CALLER_HEADER, v);
        assert!(check(&h).unwrap_err().contains("more than once"));

        let mut h = signed_headers(&test_key(), &good());
        let v = h.get(SIGNATURE_HEADER).unwrap().clone();
        h.append(SIGNATURE_HEADER, v);
        assert!(check(&h).unwrap_err().contains("more than once"));
    }

    #[test]
    fn rejects_non_ascii_header() {
        let mut h = signed_headers(&test_key(), &good());
        h.insert(CALLER_HEADER, HeaderValue::from_bytes(b"caf\xe9").unwrap());
        assert!(check(&h).is_err());
    }

    #[test]
    fn rejects_forged_signature() {
        let other = other_key();
        assert!(check(&signed_headers(&other, &good())).is_err());
    }

    #[test]
    fn rejects_tampered_caller() {
        // Signature from one caller, header from another.
        let mut h = signed_headers(&test_key(), &good());
        let mut admin = good();
        admin["kind"] = "admin".into();
        let other = signed_headers(&test_key(), &admin);
        h.insert(CALLER_HEADER, other.get(CALLER_HEADER).unwrap().clone());
        assert!(check(&h).unwrap_err().contains("signature"));
    }

    #[test]
    fn rejects_bad_signature_encoding() {
        let mut h = signed_headers(&test_key(), &good());
        h.insert(SIGNATURE_HEADER, HeaderValue::from_static("!!!"));
        assert!(check(&h).is_err());
        h.insert(SIGNATURE_HEADER, HeaderValue::from_static("AAAA"));
        assert!(check(&h).is_err());
    }

    #[test]
    fn checks_the_clock_window() {
        let key = test_key();
        let at = |ts: i64| {
            check(&signed_headers(
                &key,
                &caller_json("GET", "/api/files/x?y=1", ts),
            ))
        };
        assert!(at(NOW).is_ok());
        assert!(at(NOW - 30).is_ok());
        assert!(at(NOW + 30).is_ok());
        assert!(at(NOW - 31).is_err());
        assert!(at(NOW + 31).is_err());
    }

    #[test]
    fn rejects_other_method_path_and_query() {
        let key = test_key();
        let h = signed_headers(&key, &good());
        let v = key.verifying_key();
        assert!(verify(&h, "POST", "/api/files/x?y=1", NOW, &v).is_err());
        assert!(verify(&h, "GET", "/api/files/z?y=1", NOW, &v).is_err());
        assert!(verify(&h, "GET", "/api/files/x?y=2", NOW, &v).is_err());
        assert!(verify(&h, "GET", "/api/files/x", NOW, &v).is_err());
    }

    #[test]
    fn rejects_oversized_caller() {
        let mut j = good();
        j["person"] = "p".repeat(MAX_CALLER_LEN).into();
        assert!(check(&signed_headers(&test_key(), &j))
            .unwrap_err()
            .contains("too large"));
    }

    #[test]
    fn rejects_bad_kind_version_and_unknown_fields() {
        let key = test_key();
        let mut j = good();
        j["kind"] = "robot".into();
        assert!(check(&signed_headers(&key, &j)).is_err());

        let mut j = good();
        j["v"] = 2.into();
        assert!(check(&signed_headers(&key, &j))
            .unwrap_err()
            .contains("version"));

        let mut j = good();
        j["extra"] = true.into();
        assert!(check(&signed_headers(&key, &j)).is_err());
    }

    #[test]
    fn rejects_caller_that_is_not_json() {
        let key = test_key();
        let text = URL_SAFE_NO_PAD.encode(b"not json");
        let h = headers_for_text(&key, &text);
        assert!(check(&h).is_err());
    }
}

#[cfg(test)]
mod extreme_ts {
    use super::*;
    use crate::testutil::*;

    #[test]
    fn rejects_extreme_timestamps() {
        for ts in [i64::MIN, i64::MAX] {
            let h = signed_headers(&test_key(), &caller_json("GET", "/x", ts));
            let err =
                verify(&h, "GET", "/x", 1_800_000_000, &test_key().verifying_key()).unwrap_err();
            assert!(err.contains("too old or too new"), "{ts}: {err}");
        }
    }
}
