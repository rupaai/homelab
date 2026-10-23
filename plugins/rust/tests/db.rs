mod common;

use common::*;
use homelab::*;
use serde_json::json;

const TOKEN: &str = "hlk_secret_token_value_0123456789abcdefghij";

fn client(f: &Fake) -> Homelab {
    Homelab::new(format!("{}/", f.url), TOKEN).unwrap()
}

const INFO: &str = r#"{"app":"shop","env":"prod","engine":"postgres","status":"up","size_bytes":10,"connections":2,"tables":3,"x":1}"#;

#[tokio::test]
async fn list_gets_databases() {
    let f = fake(vec![json(200, &format!(r#"{{"databases":[{INFO}]}}"#))]).await;
    let out = client(&f).db().list().await.unwrap();
    assert_eq!(out.len(), 1);
    assert_eq!(out[0].app, "shop");
    assert_eq!(out[0].engine, "postgres");
    assert_eq!(out[0].tables, 3);
    assert_eq!(out[0].extra["x"], 1);
    let r = f.last();
    assert_eq!((r.method.as_str(), r.path.as_str()), ("GET", "/api/db"));
    assert_eq!(
        r.header("authorization"),
        Some(format!("Bearer {TOKEN}").as_str())
    );
}

#[tokio::test]
async fn info_gets_one() {
    let f = fake(vec![json(200, INFO)]).await;
    let d = client(&f)
        .db()
        .info(Some("shop"), Some("prod"))
        .await
        .unwrap();
    assert_eq!(d.size_bytes, 10);
    assert_eq!(f.last().path, "/api/db/shop/prod");
}

#[tokio::test]
async fn tables_lists_tables() {
    let f = fake(vec![json(
        200,
        r#"{"tables":[{"schema":"public","name":"orders","rows":7}]}"#,
    )])
    .await;
    let t = client(&f)
        .db()
        .tables(Some("shop"), Some("prod"))
        .await
        .unwrap();
    assert_eq!(t[0].name, "orders");
    assert_eq!(t[0].rows, 7);
    assert_eq!(f.last().path, "/api/db/shop/prod/tables");
}

#[tokio::test]
async fn rows_sends_options_and_encodes_the_table() {
    let f = fake(vec![json(
        200,
        r#"{"columns":["id"],"rows":[[1],[2]],"truncated":true}"#,
    )])
    .await;
    let out = client(&f)
        .db()
        .rows(
            Some("shop"),
            Some("prod"),
            "my table",
            RowsOptions::default().schema("app").limit(20).offset(40),
        )
        .await
        .unwrap();
    assert_eq!(out.columns, vec!["id"]);
    assert_eq!(out.rows, vec![vec![json!(1)], vec![json!(2)]]);
    assert!(out.truncated);
    let r = f.last();
    assert_eq!(r.path, "/api/db/shop/prod/tables/my%20table/rows");
    assert_eq!(r.query, "schema=app&limit=20&offset=40");
}

#[tokio::test]
async fn rows_without_options_has_no_query() {
    let f = fake(vec![json(
        200,
        r#"{"columns":[],"rows":[],"truncated":false}"#,
    )])
    .await;
    client(&f)
        .db()
        .rows(Some("shop"), Some("prod"), "orders", RowsOptions::default())
        .await
        .unwrap();
    assert_eq!(f.last().query, "");
}

#[tokio::test]
async fn rows_bad_limit_is_refused_before_sending() {
    let f = fake(vec![]).await;
    for l in [0, 201] {
        let err = client(&f)
            .db()
            .rows(
                Some("shop"),
                Some("prod"),
                "t",
                RowsOptions::default().limit(l),
            )
            .await
            .unwrap_err();
        assert_eq!(err.status(), 0);
    }
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn query_sends_only_sql() {
    let f = fake(vec![json(
        200,
        r#"{"columns":["n"],"rows":[[1]],"row_count":1,"truncated":false,"command":"SELECT","ms":3}"#,
    )])
    .await;
    let out = client(&f)
        .db()
        .query(Some("shop"), Some("prod"), "select 1 as n")
        .await
        .unwrap();
    assert_eq!(out.row_count, 1);
    assert_eq!(out.command, "SELECT");
    assert_eq!(out.ms, 3);
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("POST", "/api/db/shop/prod/query")
    );
    assert_eq!(r.header("content-type"), Some("application/json"));
    assert_eq!(r.json(), json!({ "sql": "select 1 as n" }));
}

#[tokio::test]
async fn dot_segments_are_refused() {
    let f = fake(vec![]).await;
    let db = client(&f).db();
    assert_eq!(
        db.info(Some("."), Some("prod")).await.unwrap_err().status(),
        0
    );
    assert_eq!(
        db.tables(Some("shop"), Some(".."))
            .await
            .unwrap_err()
            .status(),
        0
    );
    let e = db
        .rows(Some("shop"), Some("prod"), "..", RowsOptions::default())
        .await
        .unwrap_err();
    assert_eq!(e.status(), 0);
    let e = db
        .query(Some(".."), Some("prod"), "select 1")
        .await
        .unwrap_err();
    assert_eq!(e.status(), 0);
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn errors_map_to_api_errors() {
    let f = fake(vec![
        json(404, r#"{"error":"There's no such database."}"#),
        json(
            403,
            r#"{"error":"This key can't do that. It needs the db:read scope."}"#,
        ),
    ])
    .await;
    let db = client(&f).db();
    match db.info(Some("x"), Some("dev")).await.unwrap_err() {
        Error::Api { status, message } => {
            assert_eq!(status, 404);
            assert_eq!(message, "There's no such database.");
        }
        e => panic!("{e:?}"),
    }
    assert_eq!(db.list().await.unwrap_err().status(), 403);
}

// The environment is shared by the whole test binary, so everything that touches
// HOMELAB_APP / HOMELAB_ENV lives in this one test.
#[tokio::test]
async fn app_and_env_default_to_the_environment() {
    let f = fake(vec![json(200, r#"{"tables":[]}"#), json(200, INFO)]).await;
    let db = client(&f).db();

    std::env::remove_var("HOMELAB_APP");
    std::env::remove_var("HOMELAB_ENV");
    let e = db.tables(None, None).await.unwrap_err();
    assert!(e.to_string().contains("Set HOMELAB_APP or pass app"), "{e}");
    std::env::set_var("HOMELAB_APP", "blog");
    let e = db.tables(None, None).await.unwrap_err();
    assert!(e.to_string().contains("Set HOMELAB_ENV or pass env"), "{e}");
    assert!(f.all().is_empty());

    std::env::set_var("HOMELAB_ENV", "dev");
    db.tables(None, None).await.unwrap();
    assert_eq!(f.last().path, "/api/db/blog/dev/tables");

    // Explicit values win.
    db.info(Some("shop"), Some("prod")).await.unwrap();
    assert_eq!(f.last().path, "/api/db/shop/prod");

    std::env::remove_var("HOMELAB_APP");
    std::env::remove_var("HOMELAB_ENV");
}
