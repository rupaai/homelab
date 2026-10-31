//! Tests for files.
use super::*;

use crate::testutil::*;
use http_body_util::BodyExt;
use sha2::{Digest, Sha256};
use tower::ServiceExt;

const PNG: &[u8] = b"\x89PNG\r\n\x1a\n0123456789";

async fn call(
    env: &TestEnv,
    who: &Value,
    method: &str,
    target: &str,
    body: &[u8],
    extra: &[(&str, &str)],
) -> (StatusCode, HeaderMap, Vec<u8>) {
    let req = request(who, method, target, body.to_vec(), extra);
    let res = env.router.clone().oneshot(req).await.unwrap();
    let (parts, body) = res.into_parts();
    (
        parts.status,
        parts.headers,
        body.collect().await.unwrap().to_bytes().to_vec(),
    )
}

/// Like `call`, with the body parsed as JSON.
async fn json_call(
    env: &TestEnv,
    who: &Value,
    method: &str,
    target: &str,
    body: Value,
) -> (StatusCode, Value) {
    let bytes = if body.is_null() {
        Vec::new()
    } else {
        body.to_string().into_bytes()
    };
    let (status, _, out) = call(env, who, method, target, &bytes, &[]).await;
    (status, serde_json::from_slice(&out).unwrap_or(Value::Null))
}

async fn make_folder(env: &TestEnv, who: &Value, name: &str, app: &str, env_name: &str) {
    let body = json!({ "name": name, "app": app, "env": env_name });
    let (status, _) = json_call(env, who, "POST", "/api/files/folders", body).await;
    assert_eq!(status, StatusCode::CREATED);
}

async fn put(env: &TestEnv, who: &Value, query: &str, body: &[u8]) -> (StatusCode, Value) {
    let target = format!("/api/files/photos{query}");
    let (status, _, out) = call(
        env,
        who,
        "POST",
        &target,
        body,
        &[("content-type", "text/plain")],
    )
    .await;
    (status, serde_json::from_slice(&out).unwrap_or(Value::Null))
}

/// Everything inside `dir`, as paths.
fn tree(dir: &std::path::Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
        out.push(entry.path());
        if entry.path().is_dir() {
            out.extend(tree(&entry.path()));
        }
    }
    out
}

fn files_on_disk(env: &TestEnv) -> usize {
    tree(&env.disk).iter().filter(|p| p.is_file()).count()
}

// ---- pure functions ----

fn caller(kind: Kind, scopes: &[&str], app: &str, env: &str) -> Caller {
    Caller {
        v: 1,
        kind,
        person: "p".into(),
        key: "k".into(),
        app: app.into(),
        env: env.into(),
        scopes: scopes.iter().map(|s| s.to_string()).collect(),
        method: "GET".into(),
        path: "/".into(),
        ts: 0,
    }
}

#[test]
fn access_rules() {
    let person = caller(Kind::Person, &[], "", "");
    assert!(allowed(&person, true, "", ""));
    assert!(allowed(&person, true, "notes", "dev"));

    let none = caller(Kind::Admin, &[], "", "");
    let read = caller(Kind::Admin, &["files:read"], "", "");
    let write = caller(Kind::Admin, &["files:write"], "", "");
    assert!(!allowed(&none, false, "", ""));
    assert!(!allowed(&none, true, "", ""));
    assert!(allowed(&read, false, "notes", "dev"));
    assert!(!allowed(&read, true, "notes", "dev"));
    assert!(allowed(&write, false, "", ""));
    assert!(allowed(&write, true, "notes", "prod"));

    let app = caller(Kind::App, &[], "notes", "dev");
    for write in [false, true] {
        assert!(allowed(&app, write, "notes", "dev"));
        assert!(!allowed(&app, write, "notes", "prod"));
        assert!(!allowed(&app, write, "other", "dev"));
        assert!(!allowed(&app, write, "", ""));
    }
    // An app caller without an app never matches admin-owned folders.
    let odd = caller(Kind::App, &[], "", "");
    assert!(!allowed(&odd, false, "", ""));
}

#[test]
fn ranges() {
    use Wanted::*;
    assert_eq!(parse_range("bytes=0-4", 10), Part(0, 4));
    assert_eq!(parse_range("bytes=5-", 10), Part(5, 9));
    assert_eq!(parse_range("bytes=-3", 10), Part(7, 9));
    assert_eq!(parse_range("bytes=-30", 10), Part(0, 9));
    assert_eq!(parse_range("bytes=8-100", 10), Part(8, 9));
    assert_eq!(parse_range("bytes=10-", 10), Unsatisfiable);
    assert_eq!(parse_range("bytes=10-12", 10), Unsatisfiable);
    assert_eq!(parse_range("bytes=-0", 10), Unsatisfiable);
    assert_eq!(parse_range("bytes=0-0", 0), Unsatisfiable);
    assert_eq!(parse_range("bytes=0-1,4-5", 10), Whole);
    assert_eq!(parse_range("bytes=5-2", 10), Whole);
    assert_eq!(parse_range("bytes=x-2", 10), Whole);
    assert_eq!(parse_range("bytes=-", 10), Whole);
    assert_eq!(parse_range("lines=1-2", 10), Whole);
}

#[test]
fn tags() {
    assert_eq!(
        clean_tag("Holiday 2024_a-b").as_deref(),
        Some("holiday 2024_a-b")
    );
    assert_eq!(clean_tag(""), None);
    assert_eq!(clean_tag("a/b"), None);
    assert_eq!(clean_tag("é"), None);
    assert!(clean_tag(&"a".repeat(40)).is_some());
    assert!(clean_tag(&"a".repeat(41)).is_none());
    let twenty: Vec<String> = (0..20).map(|i| format!("t{i}")).collect();
    assert!(clean_tags(&twenty).is_ok());
    let mut twenty_one = twenty;
    twenty_one.push("x".into());
    assert!(clean_tags(&twenty_one).is_err());
}

// ---- folders ----

#[tokio::test]
async fn creating_folders() {
    let env = test_env(1 << 20);
    let url = "/api/files/folders";

    // A person makes an admin folder.
    let (status, body) = json_call(&env, &person(), "POST", url, json!({"name": "photos"})).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(body["name"], "photos");
    assert_eq!(body["disk"], "main");
    assert_eq!(body["app"], "");
    assert_eq!(body["files"], 0);

    // Again: 409.
    let (status, _) = json_call(&env, &person(), "POST", url, json!({"name": "photos"})).await;
    assert_eq!(status, StatusCode::CONFLICT);

    // Admin keys need files:write.
    for scopes in [&[][..], &["files:read"][..]] {
        let (status, body) =
            json_call(&env, &admin(scopes), "POST", url, json!({"name": "a1"})).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        assert!(body["error"].is_string());
    }
    let writer = admin(&["files:write"]);
    let body = json!({"name": "a1", "app": "notes", "env": "prod"});
    let (status, body) = json_call(&env, &writer, "POST", url, body).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(
        (body["app"].as_str(), body["env"].as_str()),
        (Some("notes"), Some("prod"))
    );

    // An app owns what it makes, even if it names no app.
    let notes = app_key("notes", "dev");
    let (status, body) = json_call(&env, &notes, "POST", url, json!({"name": "n1"})).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(
        (body["app"].as_str(), body["env"].as_str()),
        (Some("notes"), Some("dev"))
    );
    // It may name itself.
    let body = json!({"name": "n2", "app": "notes", "env": "dev"});
    assert_eq!(
        json_call(&env, &notes, "POST", url, body).await.0,
        StatusCode::CREATED
    );
    // It may not name another app or env.
    for (a, e) in [("other", "dev"), ("notes", "prod")] {
        let body = json!({"name": "n3", "app": a, "env": e});
        assert_eq!(
            json_call(&env, &notes, "POST", url, body).await.0,
            StatusCode::FORBIDDEN
        );
    }

    // Bad requests.
    for body in [
        json!({"name": "Bad"}),
        json!({"name": ".."}),
        json!({"name": "folders"}),
        json!({"name": "x", "disk": "nope"}),
        json!({"name": "x", "app": "notes"}),
        json!({"name": "x", "app": "notes", "env": "staging"}),
        json!({"name": "x", "app": "../x", "env": "dev"}),
        json!({"nope": 1}),
    ] {
        let (status, body) = json_call(&env, &person(), "POST", url, body.clone()).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    }
}

#[tokio::test]
async fn folder_list_depends_on_the_caller() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "pics", "", "").await;
    make_folder(&env, &person(), "notes-dev", "notes", "dev").await;
    make_folder(&env, &person(), "other-prod", "other", "prod").await;

    let names = |who: Value| {
        let env = &env;
        async move {
            let (status, body) =
                json_call(env, &who, "GET", "/api/files/folders", Value::Null).await;
            assert_eq!(status, StatusCode::OK);
            body["folders"]
                .as_array()
                .unwrap()
                .iter()
                .map(|f| f["name"].as_str().unwrap().to_string())
                .collect::<Vec<_>>()
        }
    };
    assert_eq!(names(person()).await, ["notes-dev", "other-prod", "pics"]);
    assert_eq!(names(admin(&["files:read"])).await.len(), 3);
    assert_eq!(names(admin(&[])).await.len(), 0);
    assert_eq!(names(app_key("notes", "dev")).await, ["notes-dev"]);
    assert_eq!(names(app_key("notes", "prod")).await.len(), 0);
}

// ---- files ----

#[tokio::test]
async fn upload_list_info_download() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;

    // The client says text/plain; the bytes say PNG.
    let (status, info) = put(&env, &person(), "?name=My%20Cat.png&tags=Pets,cute", PNG).await;
    assert_eq!(status, StatusCode::CREATED, "{info}");
    assert_eq!(info["name"], "My_Cat.png");
    assert_eq!(info["content_type"], "image/png");
    assert_eq!(info["size"], PNG.len());
    assert_eq!(info["sha256"], hex::encode(Sha256::digest(PNG)));
    assert_eq!(info["tags"], json!(["pets", "cute"]));
    assert_eq!(info["created_by"], "person:p1");
    let id = info["id"].as_str().unwrap().to_string();

    // It is on disk where the layout says, under the disk.
    let stored: Vec<_> = tree(&env.disk)
        .into_iter()
        .filter(|p| p.is_file())
        .collect();
    assert_eq!(stored.len(), 1);
    let rel = stored[0]
        .strip_prefix(&env.disk)
        .unwrap()
        .to_string_lossy()
        .to_string();
    assert!(rel.starts_with("homelab-files/photos/"), "{rel}");
    assert!(rel.ends_with(&format!("{id}-My_Cat.png")), "{rel}");
    // The temp folder is empty.
    assert_eq!(tree(&env.disk.join("homelab-files/.tmp")).len(), 0);

    // A second file, text, to test the filters.
    let (status, second) = put(&env, &person(), "?name=a.txt&tags=docs", b"hello").await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(second["content_type"], "application/octet-stream");

    let list = |query: &str| {
        let target = format!("/api/files/photos{query}");
        let env = &env;
        async move {
            json_call(env, &person(), "GET", &target, Value::Null)
                .await
                .1
        }
    };
    let all = list("").await;
    let ids: Vec<_> = all["files"]
        .as_array()
        .unwrap()
        .iter()
        .map(|f| f["id"].clone())
        .collect();
    assert_eq!(ids, [second["id"].clone(), json!(id)]); // newest first
    assert_eq!(all["next"], Value::Null);
    assert_eq!(
        list("?type=image/").await["files"]
            .as_array()
            .unwrap()
            .len(),
        1
    );
    assert_eq!(list("?tag=docs").await["files"][0]["name"], "a.txt");
    assert_eq!(list("?tag=doc").await["files"].as_array().unwrap().len(), 0);
    let page = list("?limit=1").await;
    assert_eq!(page["files"].as_array().unwrap().len(), 1);
    assert_eq!(page["next"], second["id"]);
    let rest = list(&format!(
        "?limit=1&after={}",
        page["next"].as_str().unwrap()
    ))
    .await;
    assert_eq!(rest["files"][0]["id"], json!(id));
    assert_eq!(rest["next"], Value::Null);

    // Info.
    let (status, got) = json_call(
        &env,
        &person(),
        "GET",
        &format!("/api/files/photos/{id}/info"),
        Value::Null,
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(got, info);

    // Download.
    let (status, headers, bytes) = call(
        &env,
        &person(),
        "GET",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(bytes, PNG);
    assert_eq!(headers["content-type"], "image/png");
    assert_eq!(headers["content-length"], PNG.len().to_string().as_str());
    assert_eq!(headers["accept-ranges"], "bytes");
    assert_eq!(
        headers["content-disposition"],
        "attachment; filename=\"My_Cat.png\""
    );
    assert_eq!(headers["x-content-type-options"], "nosniff");
    assert_eq!(headers["cache-control"], "no-store");
    assert_eq!(
        headers["etag"],
        format!("\"{}\"", hex::encode(Sha256::digest(PNG))).as_str()
    );

    // Folder totals.
    let (_, body) = json_call(&env, &person(), "GET", "/api/files/folders", Value::Null).await;
    assert_eq!(body["folders"][0]["files"], 2);
    assert_eq!(body["folders"][0]["bytes"], PNG.len() + 5);
}

#[tokio::test]
async fn ranges_on_download() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "?name=n.bin", b"0123456789").await;
    let target = format!("/api/files/photos/{}", info["id"].as_str().unwrap());
    let get = |range: &'static str| {
        let (env, target) = (&env, &target);
        async move { call(env, &person(), "GET", target, &[], &[("range", range)]).await }
    };

    let (status, headers, bytes) = get("bytes=2-4").await;
    assert_eq!(status, StatusCode::PARTIAL_CONTENT);
    assert_eq!(bytes, b"234");
    assert_eq!(headers["content-range"], "bytes 2-4/10");
    assert_eq!(headers["content-length"], "3");
    assert_eq!(get("bytes=-3").await.2, b"789");
    assert_eq!(get("bytes=7-").await.2, b"789");

    let (status, headers, _) = get("bytes=10-").await;
    assert_eq!(status, StatusCode::RANGE_NOT_SATISFIABLE);
    assert_eq!(headers["content-range"], "bytes */10");

    // Several ranges: the whole file.
    let (status, _, bytes) = get("bytes=0-1,3-4").await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(bytes, b"0123456789");
}

#[tokio::test]
async fn too_big_uploads_are_refused() {
    let env = test_env(100);
    make_folder(&env, &person(), "photos", "", "").await;
    let (status, body) = put(&env, &person(), "", &[7u8; 101]).await;
    assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
    assert_eq!(body["error"], "The file is bigger than the 1 MB limit.");
    assert_eq!(files_on_disk(&env), 0);
    // Exactly the limit is fine.
    assert_eq!(
        put(&env, &person(), "", &[7u8; 100]).await.0,
        StatusCode::CREATED
    );
}

#[tokio::test]
async fn wrong_sha256_is_refused() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let wrong = "0".repeat(64);
    let (status, _) = put(&env, &person(), &format!("?sha256={wrong}"), b"abc").await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(files_on_disk(&env), 0);
    let (status, _) = put(&env, &person(), "?sha256=xyz", b"abc").await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    // The right one (in capitals) works.
    let right = hex::encode(Sha256::digest(b"abc")).to_uppercase();
    let (status, _) = put(&env, &person(), &format!("?sha256={right}"), b"abc").await;
    assert_eq!(status, StatusCode::CREATED);
    // Nothing was recorded for the refused ones.
    let (_, body) = json_call(&env, &person(), "GET", "/api/files/photos", Value::Null).await;
    assert_eq!(body["files"].as_array().unwrap().len(), 1);
}

#[tokio::test]
async fn bad_tags_are_refused() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (status, _) = put(&env, &person(), "?tags=ok,bad/tag", b"x").await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(files_on_disk(&env), 0);
}

#[tokio::test]
async fn patching_tags_and_info() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "?tags=a", b"x").await;
    let target = format!("/api/files/photos/{}/info", info["id"].as_str().unwrap());

    let (status, body) = json_call(
        &env,
        &person(),
        "PATCH",
        &target,
        json!({"tags": ["B", "c d"]}),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(body["tags"], json!(["b", "c d"]));
    assert_eq!(body["info"], json!({}));

    // Only info: tags stay.
    let (_, body) = json_call(&env, &person(), "PATCH", &target, json!({"info": {"w": 1}})).await;
    assert_eq!(body["tags"], json!(["b", "c d"]));
    assert_eq!(body["info"], json!({"w": 1}));

    for bad in [
        json!({"tags": ["no/slash"]}),
        json!({"tags": (0..21).map(|i| format!("t{i}")).collect::<Vec<_>>()}),
        json!({"info": [1]}),
        json!({"info": {"big": "x".repeat(5000)}}),
        json!({"name": "new"}),
    ] {
        let (status, _) = json_call(&env, &person(), "PATCH", &target, bad).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }
    // A body over 64 KiB is refused.
    let huge = vec![b' '; 70 * 1024];
    let (status, _, _) = call(&env, &person(), "PATCH", &target, &huge, &[]).await;
    assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
    // Unchanged by the bad ones.
    let (_, body) = json_call(&env, &person(), "GET", &target, Value::Null).await;
    assert_eq!(body["info"], json!({"w": 1}));
}

#[tokio::test]
async fn deleted_files_are_hidden_then_purged() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "", b"bye").await;
    let id = info["id"].as_str().unwrap();
    let file = format!("/api/files/photos/{id}");

    let (status, _, _) = call(&env, &person(), "DELETE", &file, &[], &[]).await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    for (method, target) in [
        ("GET", file.clone()),
        ("GET", format!("{file}/info")),
        ("DELETE", file.clone()),
    ] {
        let (status, _, _) = call(&env, &person(), method, &target, &[], &[]).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "{method} {target}");
    }
    let (_, list) = json_call(&env, &person(), "GET", "/api/files/photos", Value::Null).await;
    assert_eq!(list["files"].as_array().unwrap().len(), 0);
    let (_, folders) = json_call(&env, &person(), "GET", "/api/files/folders", Value::Null).await;
    assert_eq!(folders["folders"][0]["files"], 0);
    assert_eq!(folders["folders"][0]["bytes"], 0);

    // Still on disk for now; a purge 29 days later keeps it, 31 days later removes it.
    let day = 24 * 60 * 60;
    assert_eq!(files_on_disk(&env), 1);
    assert_eq!(env.files.purge(unix_now() + 29 * day).await.unwrap(), 0);
    assert_eq!(files_on_disk(&env), 1);
    assert_eq!(env.files.purge(unix_now() + 31 * day).await.unwrap(), 1);
    assert_eq!(files_on_disk(&env), 0);
    assert_eq!(env.files.purge(unix_now() + 31 * day).await.unwrap(), 0);
}

// ---- access and safety ----

#[tokio::test]
async fn other_callers_see_no_folder() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "notes", "dev").await;
    let owner = app_key("notes", "dev");
    let (_, info) = put(&env, &owner, "?name=a.txt", b"mine").await;
    assert_eq!(info["created_by"], "app:notes/dev");
    let id = info["id"].as_str().unwrap();

    let others = [
        app_key("other", "dev"),
        app_key("notes", "prod"),
        admin(&[]),
    ];
    for who in &others {
        for (method, target) in [
            ("GET", "/api/files/photos".to_string()),
            ("POST", "/api/files/photos".to_string()),
            ("GET", format!("/api/files/photos/{id}")),
            ("GET", format!("/api/files/photos/{id}/info")),
            ("PATCH", format!("/api/files/photos/{id}/info")),
            ("DELETE", format!("/api/files/photos/{id}")),
            ("GET", "/api/files/missing".to_string()),
        ] {
            let (status, _, body) = call(&env, who, method, &target, b"{}", &[]).await;
            assert_eq!(status, StatusCode::NOT_FOUND, "{method} {target}");
            let body: Value = serde_json::from_slice(&body).unwrap();
            assert!(body["error"]
                .as_str()
                .unwrap()
                .starts_with("There's no folder"));
        }
    }
    // Nothing was uploaded or deleted by them.
    assert_eq!(files_on_disk(&env), 1);
    let (status, _, bytes) = call(
        &env,
        &owner,
        "GET",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    assert_eq!((status, bytes.as_slice()), (StatusCode::OK, &b"mine"[..]));
}

#[tokio::test]
async fn read_only_admin_can_read_not_write() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "", b"x").await;
    let id = info["id"].as_str().unwrap();
    let reader = admin(&["files:read"]);
    let (status, _, _) = call(
        &env,
        &reader,
        "GET",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(put(&env, &reader, "", b"y").await.0, StatusCode::FORBIDDEN);
    let (status, _, _) = call(
        &env,
        &reader,
        "DELETE",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    let writer = admin(&["files:write"]);
    let (status, info) = put(&env, &writer, "", b"z").await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(info["created_by"], "key:k1");
}

#[tokio::test]
async fn path_tricks_find_nothing() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "", b"x").await;
    let id = info["id"].as_str().unwrap();
    let before = tree(env.dir.path()).len();

    for target in [
        "/api/files/..",
        "/api/files/%2e%2e",
        "/api/files/%2e%2e/%2e%2e",
        "/api/files/..%2Fdisk",
        "/api/files/photos%2F..%2F..",
        "/api/files/Photos",
        "/api/files/photos/..",
        "/api/files/photos/%2e%2e",
        "/api/files/photos/..%2F..%2Fetc%2Fpasswd",
        "/api/files/photos/not-an-id",
        "/api/files/photos/not-an-id/info",
        &format!("/api/files/photos/{id}/extra/more"),
        &format!("/api/files/photos/{id}%2F.."),
    ] {
        for method in ["GET", "POST", "DELETE"] {
            let (status, _, _) = call(&env, &person(), method, target, b"x", &[]).await;
            assert!(
                status == StatusCode::NOT_FOUND || status == StatusCode::BAD_REQUEST,
                "{method} {target}: {status}"
            );
        }
    }
    // Bad folder names when creating.
    for name in ["..", "../x", "a/b", "%2e%2e"] {
        let (status, _) = json_call(
            &env,
            &person(),
            "POST",
            "/api/files/folders",
            json!({"name": name}),
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{name}");
    }
    // A hostile file name stays a plain name inside the folder.
    let (status, info) = put(&env, &person(), "?name=..%2F..%2F..%2Fevil.txt", b"y").await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(info["name"], "_.._.._evil.txt");

    // Nothing new outside the disk's own folders.
    let outside: Vec<_> = tree(env.dir.path())
        .into_iter()
        .filter(|p| !p.starts_with(&env.disk) && !p.starts_with(env.dir.path().join("data")))
        .collect();
    assert!(outside.is_empty(), "{outside:?}");
    assert!(tree(env.dir.path()).len() > before); // only the second upload
}

#[tokio::test]
async fn symlinked_storage_folders_are_refused() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let elsewhere = tempfile::tempdir().unwrap();
    std::os::unix::fs::symlink(elsewhere.path(), env.disk.join("homelab-files")).unwrap();

    let (status, body) = put(&env, &person(), "", b"x").await;
    assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
    assert!(body["error"].is_string());
    assert_eq!(tree(elsewhere.path()).len(), 0);
}

#[tokio::test]
async fn symlinked_files_are_not_served() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "", b"x").await;
    let id = info["id"].as_str().unwrap();
    let stored = tree(&env.disk).into_iter().find(|p| p.is_file()).unwrap();
    let secret = env.dir.path().join("secret");
    std::fs::write(&secret, "secret").unwrap();
    std::fs::remove_file(&stored).unwrap();
    std::os::unix::fs::symlink(&secret, &stored).unwrap();

    let (status, _, bytes) = call(
        &env,
        &person(),
        "GET",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
    assert!(!String::from_utf8_lossy(&bytes).contains("secret"));
}

#[tokio::test]
async fn wrong_methods_are_404() {
    let env = test_env(1 << 20);
    for (method, target) in [
        ("PUT", "/api/files/folders"),
        ("DELETE", "/api/files/photos"),
        ("POST", "/api/files/photos/01ARZ3NDEKTSV4RRFFQ69G5FAV"),
        ("POST", "/api/files/photos/01ARZ3NDEKTSV4RRFFQ69G5FAV/info"),
    ] {
        let (status, _, _) = call(&env, &person(), method, target, &[], &[]).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "{method} {target}");
    }
}

// ---- deleting folders ----

async fn folder_names(env: &TestEnv) -> Vec<String> {
    let (_, list) = json_call(env, &person(), "GET", "/api/files/folders", Value::Null).await;
    list["folders"]
        .as_array()
        .unwrap()
        .iter()
        .map(|f| f["name"].as_str().unwrap().to_string())
        .collect()
}

async fn delete_folder_call(env: &TestEnv, who: &Value, name: &str) -> (StatusCode, Value) {
    json_call(
        env,
        who,
        "DELETE",
        &format!("/api/files/folders/{name}"),
        Value::Null,
    )
    .await
}

#[tokio::test]
async fn an_empty_folder_is_deleted() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (status, _) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    assert!(folder_names(&env).await.is_empty());
    let (status, body) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(body["error"], "There's no folder photos.");
}

#[tokio::test]
async fn a_folder_with_files_is_not_deleted() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    put(&env, &person(), "", b"keep me").await;
    let (status, body) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::CONFLICT);
    assert_eq!(
        body["error"],
        "The folder still has files. Delete them first."
    );
    assert_eq!(folder_names(&env).await, ["photos"]);
    assert_eq!(files_on_disk(&env), 1);
}

#[tokio::test]
async fn trashed_files_go_with_the_folder() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    make_folder(&env, &person(), "other", "", "").await;
    let (_, info) = put(&env, &person(), "", b"bye").await;
    let id = info["id"].as_str().unwrap();
    let (status, _, _) = call(
        &env,
        &person(),
        "DELETE",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    assert_eq!(files_on_disk(&env), 1);

    let (status, _) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    assert_eq!(files_on_disk(&env), 0);
    assert!(!env.disk.join("homelab-files/photos").exists());
    assert!(env.disk.join("homelab-files").is_dir());
    assert_eq!(folder_names(&env).await, ["other"]);
    let rows = env
        .files
        .db
        .run(|c| c.query_row("SELECT COUNT(*) FROM files", [], |r| r.get::<_, i64>(0)))
        .await
        .unwrap();
    assert_eq!(rows, 0);
    // The purge has nothing left to do.
    let later = unix_now() + 31 * 24 * 60 * 60;
    assert_eq!(env.files.purge(later).await.unwrap(), 0);
}

#[tokio::test]
async fn a_missing_disk_file_does_not_block_deleting() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "", b"gone").await;
    let id = info["id"].as_str().unwrap();
    call(
        &env,
        &person(),
        "DELETE",
        &format!("/api/files/photos/{id}"),
        &[],
        &[],
    )
    .await;
    for p in tree(&env.disk) {
        if p.is_file() {
            std::fs::remove_file(p).unwrap();
        }
    }
    let (status, _) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    assert!(folder_names(&env).await.is_empty());
}

#[tokio::test]
async fn leftover_things_in_a_deleted_folder_stay() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let stray = env.disk.join("homelab-files/photos/2020/01");
    std::fs::create_dir_all(&stray).unwrap();
    std::fs::write(stray.join("stray.txt"), b"not ours").unwrap();
    let (status, _) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    assert!(stray.join("stray.txt").is_file());
}

#[tokio::test]
async fn folder_delete_access() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "mine", "blog", "prod").await;
    make_folder(&env, &person(), "theirs", "shop", "prod").await;
    make_folder(&env, &person(), "adm", "", "").await;
    let blog = app_key("blog", "prod");
    let (status, _) = delete_folder_call(&env, &blog, "theirs").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    let (status, _) = delete_folder_call(&env, &blog, "adm").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    let (status, _) = delete_folder_call(&env, &blog, "mine").await;
    assert_eq!(status, StatusCode::NO_CONTENT);

    let reader = admin(&["files:read"]);
    let (status, _) = delete_folder_call(&env, &reader, "adm").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert_eq!(folder_names(&env).await, ["adm", "theirs"]);
    let writer = admin(&["files:write"]);
    let (status, _) = delete_folder_call(&env, &writer, "adm").await;
    assert_eq!(status, StatusCode::NO_CONTENT);
}

#[tokio::test]
async fn a_deleted_folder_name_can_be_used_again() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (status, _) = delete_folder_call(&env, &person(), "photos").await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    make_folder(&env, &person(), "photos", "", "").await;
    assert_eq!(
        put(&env, &person(), "", b"again").await.0,
        StatusCode::CREATED
    );
}

#[tokio::test]
async fn folders_route_beats_the_file_routes() {
    let env = test_env(1 << 20);
    // DELETE goes to the folder delete (its message names a folder, the
    // file delete would say "no file").
    let (status, body) = delete_folder_call(&env, &person(), "x").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert_eq!(body["error"], "There's no folder x.");
    for method in ["GET", "POST", "PUT", "PATCH"] {
        let (status, _, _) = call(&env, &person(), method, "/api/files/folders/x", &[], &[]).await;
        assert!(
            status == StatusCode::NOT_FOUND || status == StatusCode::METHOD_NOT_ALLOWED,
            "{method}: {status}"
        );
    }
}

// ---- metadata ----

use crate::meta::fixtures;

async fn list(env: &TestEnv, query: &str) -> (StatusCode, Value) {
    let target = format!("/api/files/photos{query}");
    json_call(env, &person(), "GET", &target, Value::Null).await
}

fn names(listing: &Value) -> Vec<String> {
    let mut v: Vec<String> = listing["files"]
        .as_array()
        .unwrap()
        .iter()
        .map(|f| f["name"].as_str().unwrap().to_string())
        .collect();
    v.sort();
    v
}

#[tokio::test]
async fn upload_returns_meta() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;

    let (status, info) = put(&env, &person(), "?name=a.jpg", &fixtures::jpeg_with_exif()).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(info["content_type"], "image/jpeg");
    assert_eq!(info["meta"]["width"], 640);
    assert_eq!(info["meta"]["camera_make"], "ACME");
    assert_eq!(info["meta"]["taken_at"], "2023-07-09T20:36:33+08:00");
    assert!(info["meta"]["gps"]["lat"].is_f64());

    // The same object comes back from the info and the list.
    let target = format!("/api/files/photos/{}/info", info["id"].as_str().unwrap());
    let (_, again) = json_call(&env, &person(), "GET", &target, Value::Null).await;
    assert_eq!(again["meta"], info["meta"]);
    let (_, listing) = list(&env, "").await;
    assert_eq!(listing["files"][0]["meta"], info["meta"]);

    let (status, png) = put(&env, &person(), "?name=b.png", &fixtures::png()).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(png["meta"], json!({ "width": 3, "height": 2 }));

    // Nothing found: an empty object, not null or missing.
    let (status, text) = put(&env, &person(), "?name=c.txt", b"just words").await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(text["meta"], json!({}));
}

#[tokio::test]
async fn the_client_cannot_choose_the_type_or_meta() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    // A PNG that calls itself a JPEG (by name and Content-Type), and the
    // other way round.
    let (_, info) = put(&env, &person(), "?name=x.jpg", &fixtures::png()).await;
    assert_eq!(info["content_type"], "image/png");
    assert_eq!(info["meta"]["width"], 3);
    let target = "/api/files/photos?name=y.png";
    let (status, _, out) = call(
        &env,
        &person(),
        "POST",
        target,
        &fixtures::jpeg_with_exif(),
        &[("content-type", "image/png")],
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);
    let out: Value = serde_json::from_slice(&out).unwrap();
    assert_eq!(out["content_type"], "image/jpeg");
    assert_eq!(out["meta"]["camera_make"], "ACME");
}

#[tokio::test]
async fn garbage_claiming_to_be_a_jpeg_still_uploads() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (status, info) = put(&env, &person(), "?name=bad.jpg", &fixtures::fake_jpeg()).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(info["content_type"], "image/jpeg");
    assert_eq!(info["meta"], json!({}));
    // Truncated real files too.
    let whole = fixtures::jpeg_with_exif();
    for (i, len) in [10, 40, 100, whole.len() - 3].into_iter().enumerate() {
        let (status, _) = put(&env, &person(), &format!("?name=t{i}.jpg"), &whole[..len]).await;
        assert_eq!(status, StatusCode::CREATED, "{len}");
    }
}

#[tokio::test]
async fn listing_by_date_and_camera() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let old = fixtures::jpeg_with_exif();
    let mut other = old.clone();
    for (from, to) in [
        (&b"2023:07:09"[..], &b"2021:01:02"[..]),
        (b"ACME\0", b"Zeta\0"),
        (b"Pocket Cam 3", b"Big Camera 9"),
        (b"+08:00", b"\0\0\0\0\0\0"),
    ] {
        let at = other.windows(from.len()).position(|w| w == from).unwrap();
        other[at..at + from.len()].copy_from_slice(to);
    }
    put(&env, &person(), "?name=acme.jpg", &old).await;
    put(&env, &person(), "?name=zeta.jpg", &other).await;
    put(&env, &person(), "?name=plain.png", &fixtures::png()).await;
    put(&env, &person(), "?name=note.txt", b"hi").await;

    let q = |s: &str| {
        let (env, s) = (&env, s.to_string());
        async move { list(env, &s).await }
    };
    // 2023-07-09 20:36 at +08:00 is 12:36 UTC.
    assert_eq!(names(&q("").await.1).len(), 4);
    assert_eq!(names(&q("?taken_after=2022-01-01").await.1), ["acme.jpg"]);
    assert_eq!(names(&q("?taken_before=2022-01-01").await.1), ["zeta.jpg"]);
    assert_eq!(
        names(&q("?taken_after=2021-01-02&taken_before=2024-01-01").await.1),
        ["acme.jpg", "zeta.jpg"]
    );
    // The first is inclusive, the second is not.
    assert_eq!(
        names(&q("?taken_after=2021-01-02T20:36:33Z").await.1),
        ["acme.jpg", "zeta.jpg"]
    );
    assert_eq!(
        names(&q("?taken_before=2021-01-02T20:36:33Z").await.1),
        Vec::<String>::new()
    );
    assert_eq!(
        names(
            &q("?taken_before=2023-07-09T12:36:34Z&taken_after=2023-07-09T12:36:33Z")
                .await
                .1
        ),
        ["acme.jpg"]
    );
    assert_eq!(
        names(
            &q("?taken_before=2023-07-09T12:36:33Z&taken_after=2023-01-01")
                .await
                .1
        ),
        Vec::<String>::new()
    );
    // A time zone offset is taken into account.
    assert_eq!(
        names(&q("?taken_after=2023-07-09T21:00:00%2B08:00").await.1),
        Vec::<String>::new()
    );
    assert_eq!(
        names(&q("?taken_after=2023-07-09T20:36:33%2B08:00").await.1),
        ["acme.jpg"]
    );

    assert_eq!(names(&q("?camera=acme").await.1), ["acme.jpg"]);
    assert_eq!(names(&q("?camera=POCKET").await.1), ["acme.jpg"]);
    assert_eq!(names(&q("?camera=acme%20pocket").await.1), ["acme.jpg"]);
    assert_eq!(names(&q("?camera=zet").await.1), ["zeta.jpg"]);
    assert_eq!(names(&q("?camera=camera").await.1), ["zeta.jpg"]);
    assert_eq!(names(&q("?camera=nikon").await.1), Vec::<String>::new());
    // % and _ are plain characters, and quotes do no harm.
    for odd in ["%25", "_", "'%20OR%201%3D1%20--", "%22"] {
        assert_eq!(
            names(&q(&format!("?camera={odd}")).await.1),
            Vec::<String>::new(),
            "{odd}"
        );
    }
    assert_eq!(
        names(&q("?camera=acme&taken_after=2022-01-01").await.1),
        ["acme.jpg"]
    );
    assert_eq!(
        names(&q("?camera=acme&taken_before=2022-01-01").await.1),
        Vec::<String>::new()
    );

    // Paging still works with the filters.
    let (_, page) = q("?taken_after=2000-01-01&limit=1").await;
    assert_eq!(page["files"].as_array().unwrap().len(), 1);
    assert!(page["next"].is_string());
    let after = page["next"].as_str().unwrap();
    let (_, page2) = q(&format!("?taken_after=2000-01-01&limit=1&after={after}")).await;
    assert_eq!(page2["files"].as_array().unwrap().len(), 1);
    assert!(page2["next"].is_null());
    assert_ne!(page["files"][0]["id"], page2["files"][0]["id"]);

    for bad in [
        "?taken_after=yesterday",
        "?taken_before=2023-13-01",
        "?taken_after=",
        "?taken_after=2023-07-09T10:00:00",
        "?camera=",
        &format!("?camera={}", "a".repeat(101)),
    ] {
        let (status, body) = q(bad).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{bad}");
        assert!(body["error"].is_string());
    }
    let (status, _) = q(&format!("?camera={}", "a".repeat(100))).await;
    assert_eq!(status, StatusCode::OK);
}

#[tokio::test]
async fn duplicates_are_stored_once_per_folder() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    make_folder(&env, &person(), "other", "", "").await;
    let jpeg = fixtures::jpeg_with_exif();

    let (status, first) = put(&env, &person(), "?name=a.jpg&tags=one", &jpeg).await;
    assert_eq!(status, StatusCode::CREATED);
    let on_disk = files_on_disk(&env);

    let (status, headers, out) = call(
        &env,
        &person(),
        "POST",
        "/api/files/photos?name=copy.jpg&tags=two",
        &jpeg,
        &[],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(headers["x-homelab-duplicate"], "true");
    let second: Value = serde_json::from_slice(&out).unwrap();
    // The existing file, untouched.
    assert_eq!(second, first);
    assert_eq!(second["name"], "a.jpg");
    assert_eq!(second["tags"], json!(["one"]));
    assert_eq!(files_on_disk(&env), on_disk);
    assert!(tree(&env.disk.join("homelab-files").join(".tmp")).is_empty());
    assert_eq!(list(&env, "").await.1["files"].as_array().unwrap().len(), 1);

    // A new upload has no such header.
    let (status, headers, _) =
        call(&env, &person(), "POST", "/api/files/photos", b"new", &[]).await;
    assert_eq!(status, StatusCode::CREATED);
    assert!(headers.get("x-homelab-duplicate").is_none());

    // Another folder keeps its own copy.
    let (status, headers, _) = call(&env, &person(), "POST", "/api/files/other", &jpeg, &[]).await;
    assert_eq!(status, StatusCode::CREATED);
    assert!(headers.get("x-homelab-duplicate").is_none());

    // A trashed file does not count.
    let target = format!("/api/files/photos/{}", first["id"].as_str().unwrap());
    let (status, _, _) = call(&env, &person(), "DELETE", &target, b"", &[]).await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    let (status, again) = put(&env, &person(), "?name=back.jpg", &jpeg).await;
    assert_eq!(status, StatusCode::CREATED);
    assert_ne!(again["id"], first["id"]);

    // A wrong sha256 is still refused, even for a duplicate.
    let (status, _) = put(
        &env,
        &person(),
        &format!("?sha256={}", "0".repeat(64)),
        &jpeg,
    )
    .await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn patch_cannot_change_meta() {
    let env = test_env(1 << 20);
    make_folder(&env, &person(), "photos", "", "").await;
    let (_, info) = put(&env, &person(), "?name=a.jpg", &fixtures::jpeg_with_exif()).await;
    let target = format!("/api/files/photos/{}/info", info["id"].as_str().unwrap());

    for body in [
        json!({ "meta": { "width": 1 } }),
        json!({ "meta": {}, "tags": ["x"] }),
        json!({ "taken_at": "2000-01-01T00:00:00" }),
        json!({ "info": { "meta": 1 }, "meta": null }),
    ] {
        let (status, out) = json_call(&env, &person(), "PATCH", &target, body).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(out["error"].is_string());
    }
    let (_, now) = json_call(&env, &person(), "GET", &target, Value::Null).await;
    assert_eq!(now["meta"], info["meta"]);
    assert_eq!(now["tags"], json!([]));

    // Tags and info still work, and meta stays as it was.
    let body = json!({ "tags": ["x"], "info": { "meta": "mine" } });
    let (status, out) = json_call(&env, &person(), "PATCH", &target, body).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(out["meta"], info["meta"]);
    assert_eq!(out["info"], json!({ "meta": "mine" }));
    let (_, listing) = list(&env, "?taken_after=2023-01-01").await;
    assert_eq!(listing["files"].as_array().unwrap().len(), 1);
}
