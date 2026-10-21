mod common;

use common::*;
use homelab::*;
use serde_json::json;

const TOKEN: &str = "hlk_secret_token_value_0123456789abcdefghij";
const VALUE: &str = "ya29.super-secret-value";

fn client(f: &Fake, dir: &std::path::Path) -> Homelab {
    Homelab::new(format!("{}/", f.url), TOKEN)
        .unwrap()
        .with_secrets_dir(dir)
}

fn fresh() -> GetSecretOptions {
    GetSecretOptions::default().fresh()
}

fn tmp() -> tempdir::Dir {
    tempdir::Dir::new()
}

/// A tiny temp dir, removed on drop (no extra dev-dependency).
mod tempdir {
    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicU32, Ordering};

    static N: AtomicU32 = AtomicU32::new(0);

    pub struct Dir(PathBuf);

    impl Dir {
        pub fn new() -> Self {
            let p = std::env::temp_dir().join(format!(
                "homelab-secrets-{}-{}",
                std::process::id(),
                N.fetch_add(1, Ordering::SeqCst)
            ));
            std::fs::create_dir_all(&p).unwrap();
            Dir(p)
        }
        pub fn path(&self) -> &Path {
            &self.0
        }
        pub fn write(&self, name: &str, data: &[u8]) {
            std::fs::write(self.0.join(name), data).unwrap();
        }
    }

    impl Drop for Dir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
}

#[tokio::test]
async fn list_gets_names_without_values() {
    let d = tmp();
    let f = fake(vec![json(
        200,
        r#"{"secrets":[{"name":"a","kind":"app","version":2,"updated_at":5,"x":1}]}"#,
    )])
    .await;
    let out = client(&f, d.path()).secrets().list().await.unwrap();
    assert_eq!(out.len(), 1);
    assert_eq!((out[0].name.as_str(), out[0].kind.as_str()), ("a", "app"));
    assert_eq!((out[0].version, out[0].updated_at), (2, 5));
    assert_eq!(out[0].extra["x"], 1);
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("GET", "/api/secrets")
    );
    assert_eq!(
        r.header("authorization"),
        Some(format!("Bearer {TOKEN}").as_str())
    );
}

#[tokio::test]
async fn get_fresh_asks_homelab() {
    let d = tmp();
    d.write("tok", b"from-file");
    let f = fake(vec![json(
        200,
        &format!(r#"{{"name":"tok","value":"{VALUE}","version":3}}"#),
    )])
    .await;
    let v = client(&f, d.path())
        .secrets()
        .get("tok", fresh())
        .await
        .unwrap();
    assert_eq!(v, VALUE);
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str(), r.query.as_str()),
        ("GET", "/api/secrets/tok", "")
    );
}

#[tokio::test]
async fn get_version_asks_homelab_even_with_a_file() {
    let d = tmp();
    d.write("tok", b"from-file");
    let f = fake(vec![json(
        200,
        r#"{"name":"tok","value":"old","version":2}"#,
    )])
    .await;
    let v = client(&f, d.path())
        .secrets()
        .get("tok", GetSecretOptions::default().version(2))
        .await
        .unwrap();
    assert_eq!(v, "old");
    assert_eq!(f.last().path, "/api/secrets/tok");
    assert_eq!(f.last().query, "version=2");
}

#[tokio::test]
async fn set_puts_json() {
    let d = tmp();
    let f = fake(vec![json(200, r#"{"name":"tok","version":4}"#)]).await;
    let out = client(&f, d.path())
        .secrets()
        .set("tok", VALUE)
        .await
        .unwrap();
    assert_eq!((out.name.as_str(), out.version), ("tok", 4));
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("PUT", "/api/secrets/tok")
    );
    assert_eq!(r.header("content-type"), Some("application/json"));
    assert_eq!(r.json(), json!({ "value": VALUE }));
}

#[tokio::test]
async fn delete_sends_delete() {
    let d = tmp();
    let f = fake(vec![Canned {
        status: 204,
        headers: vec![],
        body: vec![],
    }])
    .await;
    client(&f, d.path()).secrets().delete("tok").await.unwrap();
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("DELETE", "/api/secrets/tok")
    );
}

#[tokio::test]
async fn versions_lists() {
    let d = tmp();
    let f = fake(vec![json(
        200,
        r#"{"versions":[{"version":2,"set_at":9},{"version":1,"set_at":3}]}"#,
    )])
    .await;
    let out = client(&f, d.path())
        .secrets()
        .versions("tok")
        .await
        .unwrap();
    assert_eq!(out.len(), 2);
    assert_eq!((out[0].version, out[0].set_at), (2, 9));
    assert_eq!(f.last().path, "/api/secrets/tok/versions");
}

#[tokio::test]
async fn path_parts_are_encoded() {
    let d = tmp();
    let f = fake(vec![json(200, r#"{"name":"x","value":"v","version":1}"#)]).await;
    client(&f, d.path())
        .secrets()
        .get("google_token:user.42-a", fresh())
        .await
        .unwrap();
    assert_eq!(f.last().path, "/api/secrets/google_token%3Auser.42-a");
}

#[tokio::test]
async fn file_first_without_calling_homelab() {
    let d = tmp();
    d.write("API_KEY", b"from-file");
    let f = fake(vec![]).await;
    let v = client(&f, d.path())
        .secrets()
        .get("API_KEY", GetSecretOptions::default())
        .await
        .unwrap();
    assert_eq!(v, "from-file");
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn falls_back_to_the_api_when_no_file() {
    let d = tmp();
    let f = fake(vec![json(
        200,
        r#"{"name":"API_KEY","value":"from-api","version":1}"#,
    )])
    .await;
    let v = client(&f, d.path())
        .secrets()
        .get("API_KEY", GetSecretOptions::default())
        .await
        .unwrap();
    assert_eq!(v, "from-api");
    assert_eq!(f.all().len(), 1);
}

#[tokio::test]
async fn names_that_are_not_identifiers_never_touch_disk() {
    let d = tmp();
    for n in ["a.b", "a:b", "1abc"] {
        d.write(n, b"from-file");
    }
    let f = fake(vec![
        json(200, r#"{"name":"x","value":"from-api","version":1}"#),
        json(200, r#"{"name":"x","value":"from-api","version":1}"#),
        json(200, r#"{"name":"x","value":"from-api","version":1}"#),
    ])
    .await;
    let s = client(&f, d.path()).secrets();
    for n in ["a.b", "a:b", "1abc"] {
        assert_eq!(
            s.get(n, GetSecretOptions::default()).await.unwrap(),
            "from-api"
        );
    }
    assert_eq!(f.all().len(), 3);
}

#[tokio::test]
async fn bad_names_and_versions_are_refused_before_anything() {
    let d = tmp();
    let f = fake(vec![]).await;
    let s = client(&f, d.path()).secrets();
    let long = "x".repeat(201);
    for bad in [
        "../etc/passwd",
        "a/b",
        ".",
        "..",
        "",
        "a b",
        long.as_str(),
        "a\\b",
        "ok\n",
        "é",
    ] {
        assert!(matches!(
            s.get(bad, GetSecretOptions::default()).await,
            Err(Error::Config(_))
        ));
        assert!(matches!(
            s.get_opt(bad, GetSecretOptions::default()).await,
            Err(Error::Config(_))
        ));
        assert!(matches!(s.set(bad, "v").await, Err(Error::Config(_))));
        assert!(matches!(s.delete(bad).await, Err(Error::Config(_))));
        assert!(matches!(s.versions(bad).await, Err(Error::Config(_))));
    }
    assert!(matches!(
        s.get("a", GetSecretOptions::default().version(0)).await,
        Err(Error::Config(_))
    ));
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn get_404_is_an_api_error_and_get_opt_is_none() {
    let d = tmp();
    let f = fake(vec![
        json(404, r#"{"error":"There's no such secret."}"#),
        json(404, r#"{"error":"There's no such secret."}"#),
        json(403, r#"{"error":"This key can't do that."}"#),
        json(200, r#"{"name":"a","value":"v","version":1}"#),
    ])
    .await;
    let s = client(&f, d.path()).secrets();
    match s.get("nope", fresh()).await {
        Err(Error::Api { status, message }) => {
            assert_eq!(status, 404);
            assert_eq!(message, "There's no such secret.");
        }
        other => panic!("{other:?}"),
    }
    assert_eq!(s.get_opt("nope", fresh()).await.unwrap(), None);
    assert_eq!(s.get_opt("nope", fresh()).await.unwrap_err().status(), 403);
    assert_eq!(s.get_opt("a", fresh()).await.unwrap(), Some("v".into()));
}

#[tokio::test]
async fn failed_set_never_has_the_value_in_the_error() {
    let d = tmp();
    let f = fake(vec![json(
        400,
        &format!(r#"{{"error":"bad value {VALUE}"}}"#),
    )])
    .await;
    let e = client(&f, d.path())
        .secrets()
        .set("a", VALUE)
        .await
        .unwrap_err();
    assert_eq!(e.status(), 400);
    for text in [e.to_string(), format!("{e:?}")] {
        assert!(!text.contains(VALUE), "{text}");
        assert!(!text.contains(TOKEN), "{text}");
    }
}

#[tokio::test]
async fn debug_output_has_no_values_or_token() {
    let d = tmp();
    let f = fake(vec![
        json(200, r#"{"name":"a","version":1}"#),
        json(
            200,
            r#"{"secrets":[{"name":"a","kind":"app","version":1,"updated_at":1}]}"#,
        ),
    ])
    .await;
    let h = client(&f, d.path());
    let saved = h.secrets().set("a", VALUE).await.unwrap();
    let list = h.secrets().list().await.unwrap();
    for text in [
        format!("{h:?}"),
        format!("{:?}", h.secrets()),
        format!("{saved:?}"),
        format!("{list:?}"),
    ] {
        assert!(!text.contains(VALUE), "{text}");
        assert!(!text.contains(TOKEN), "{text}");
    }
}

#[tokio::test]
async fn unreadable_file_error_has_no_contents() {
    let d = tmp();
    let mut bytes = vec![0xff, 0xfe];
    bytes.extend_from_slice(VALUE.as_bytes());
    d.write("BIN", &bytes);
    let f = fake(vec![]).await;
    let e = client(&f, d.path())
        .secrets()
        .get("BIN", GetSecretOptions::default())
        .await
        .unwrap_err();
    assert!(!e.to_string().contains(VALUE));
}

#[tokio::test]
async fn redirects_are_not_followed() {
    let d = tmp();
    let f = fake(vec![Canned {
        status: 302,
        headers: vec![("location", "http://127.0.0.1:1/steal")],
        body: vec![],
    }])
    .await;
    let e = client(&f, d.path())
        .secrets()
        .get("a", fresh())
        .await
        .unwrap_err();
    assert_eq!(e.status(), 302);
    assert_eq!(f.all().len(), 1);
}
