mod common;

use common::*;
use homelab::*;

const TOKEN: &str = "hlk_secret_token_value_0123456789abcdefghij";

fn client(f: &Fake) -> Homelab {
    Homelab::new(format!("{}/", f.url), TOKEN).unwrap()
}

const ANSWER: &str = r###"{"lines":[{"id":42,"time":"2026-10-23T22:00:00.123Z","level":"error","msg":"boom","app":"a","env":"prod","service":"app","stream":"stderr","extra":{"code":7},"x":1}],"counts":{"debug":0,"info":3,"warn":1,"error":2,"fatal":0,"unknown":0},"next":"41"}"###;

#[tokio::test]
async fn search_gets_and_parses_the_answer() {
    let f = fake(vec![json(200, ANSWER)]).await;
    let out = client(&f).logs().search(LogQuery::default()).await.unwrap();
    assert_eq!(out.lines.len(), 1);
    let l = &out.lines[0];
    assert_eq!(
        (l.id, l.level.as_str(), l.msg.as_str()),
        (42, "error", "boom")
    );
    assert_eq!(l.stream, "stderr");
    assert_eq!(l.extra["code"], 7);
    assert_eq!(l.raw["x"], 1);
    assert_eq!(out.counts["error"], 2);
    assert_eq!(out.next.as_deref(), Some("41"));
    let r = f.last();
    assert_eq!((r.method.as_str(), r.path.as_str()), ("GET", "/api/logs"));
    assert_eq!(r.query, "");
    assert_eq!(
        r.header("authorization"),
        Some(format!("Bearer {TOKEN}").as_str())
    );
}

#[tokio::test]
async fn search_sends_every_option() {
    let f = fake(vec![json(200, r#"{"lines":[],"counts":{},"next":null}"#)]).await;
    let out = client(&f)
        .logs()
        .search(LogQuery {
            q: Some("disk full".into()),
            level: vec!["error".into(), "fatal".into()],
            min_level: Some("warn".into()),
            app: Some("a".into()),
            env: Some("prod".into()),
            service: Some("app".into()),
            since: Some("2026-10-23T10:00:00Z".into()),
            until: Some("1730000000000".into()),
            before: Some(41),
            after: Some(7),
            limit: Some(50),
        })
        .await
        .unwrap();
    assert!(out.lines.is_empty());
    assert_eq!(out.next, None);
    let q = &f.last().query;
    for part in [
        "q=disk%20full",
        "level=error%2Cfatal",
        "min_level=warn",
        "app=a",
        "env=prod",
        "service=app",
        "since=2026-10-23T10%3A00%3A00Z",
        "until=1730000000000",
        "before=41",
        "after=7",
        "limit=50",
    ] {
        let alt = part.replace("%20", "+");
        assert!(
            q.contains(part) || q.contains(&alt),
            "{part} missing from {q}"
        );
    }
}

#[tokio::test]
async fn an_answer_with_nothing_in_it_is_empty() {
    let f = fake(vec![json(200, "{}")]).await;
    let out = client(&f)
        .logs()
        .search(LogQuery {
            q: Some("  ".into()),
            ..Default::default()
        })
        .await
        .unwrap();
    assert!(out.lines.is_empty());
    assert_eq!(f.last().query, "");
}

#[tokio::test]
async fn bad_options_are_never_sent() {
    let f = fake(vec![]).await;
    let hl = client(&f);
    for q in [
        LogQuery {
            limit: Some(0),
            ..Default::default()
        },
        LogQuery {
            limit: Some(501),
            ..Default::default()
        },
        LogQuery {
            level: vec!["loud".into()],
            ..Default::default()
        },
        LogQuery {
            min_level: Some("loud".into()),
            ..Default::default()
        },
    ] {
        let err = hl.logs().search(q).await.unwrap_err();
        assert!(matches!(err, Error::Config(_)));
    }
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn errors_carry_the_servers_message_and_not_the_token() {
    let f = fake(vec![json(
        403,
        r#"{"error":"This key can't do that. It needs the logs:read scope."}"#,
    )])
    .await;
    let err = client(&f)
        .logs()
        .search(LogQuery::default())
        .await
        .unwrap_err();
    match &err {
        Error::Api { status, message } => {
            assert_eq!(*status, 403);
            assert!(message.contains("logs:read"));
        }
        other => panic!("{other:?}"),
    }
    assert!(!format!("{err:?}{err}").contains(TOKEN));
}
