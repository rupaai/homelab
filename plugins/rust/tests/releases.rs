mod common;

use common::*;
use homelab::*;

const TOKEN: &str = "hlk_secret_token_value_0123456789abcdefghij";

fn client(f: &Fake) -> Homelab {
    Homelab::new(format!("{}/", f.url), TOKEN).unwrap()
}

const ONE: &str = r###"{"tag":"v0.4.0","prerelease":false,"commit":"9f3c2ab0","date":"2026-10-01T10:00:00Z","summary":"2 new, 1 fix","notes":"## New\n\n- a\n","x":1}"###;

#[tokio::test]
async fn list_gets_the_apps_releases() {
    let f = fake(vec![json(
        200,
        &format!(
            r#"{{"releases":[{ONE},{{"tag":"v0.4.0-dev.2","prerelease":true,"commit":"c","date":"d","summary":"","notes":""}}]}}"#
        ),
    )])
    .await;
    let out = client(&f).releases().list(None).await.unwrap();
    assert_eq!(out.len(), 2);
    assert_eq!(out[0].tag, "v0.4.0");
    assert!(!out[0].prerelease);
    assert_eq!(out[0].summary, "2 new, 1 fix");
    assert_eq!(out[0].extra["x"], 1);
    assert!(out[1].prerelease);
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("GET", "/api/releases")
    );
    assert_eq!(r.query, "");
    assert_eq!(
        r.header("authorization"),
        Some(format!("Bearer {TOKEN}").as_str())
    );
}

#[tokio::test]
async fn list_sends_a_limit() {
    let f = fake(vec![json(200, r#"{"releases":[]}"#)]).await;
    assert!(client(&f)
        .releases()
        .list(Some(5))
        .await
        .unwrap()
        .is_empty());
    assert_eq!(f.last().query, "limit=5");
}

#[tokio::test]
async fn a_zero_limit_is_never_sent() {
    let f = fake(vec![]).await;
    let err = client(&f).releases().list(Some(0)).await.unwrap_err();
    assert!(matches!(err, Error::Config(_)));
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn errors_carry_the_servers_message_and_not_the_token() {
    let f = fake(vec![json(
        403,
        r#"{"error":"This key can't do that. It needs the releases:read scope."}"#,
    )])
    .await;
    let err = client(&f).releases().list(None).await.unwrap_err();
    match &err {
        Error::Api { status, message } => {
            assert_eq!(*status, 403);
            assert!(message.contains("releases:read"));
        }
        other => panic!("{other:?}"),
    }
    assert!(!format!("{err:?}{err}").contains(TOKEN));
}
