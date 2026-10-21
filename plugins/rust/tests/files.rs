mod common;

use common::*;
use futures_util::TryStreamExt;
use homelab::*;
use serde_json::json;

const TOKEN: &str = "hlk_secret_token_value_0123456789abcdefghij";

const FILE: &str = r#"{"id":"01J9Z3X5K8Q2W4E6R8T0Y1U2I3","folder":"posters","name":"a.jpg",
"content_type":"image/jpeg","size":3,"sha256":"ab","created_at":1,"created_by":"app:x/dev",
"tags":["dune"],"info":{"k":1},"meta":{"width":10,"gps":{"lat":1.5,"lon":2.5},"future":true},"new_field":7}"#;

fn client(f: &Fake) -> Homelab {
    Homelab::new(format!("{}/", f.url), TOKEN).unwrap()
}

#[tokio::test]
async fn folders_lists_and_sends_bearer() {
    let f = fake(vec![json(
        200,
        r#"{"folders":[{"name":"posters","disk":"main","app":"movies","env":"prod","files":2,"bytes":9,"created_at":5,"x":1}]}"#,
    )])
    .await;
    let out = client(&f).files().folders().await.unwrap();
    assert_eq!(out.len(), 1);
    assert_eq!(out[0].name, "posters");
    assert_eq!(out[0].bytes, 9);
    assert_eq!(out[0].extra["x"], 1);
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("GET", "/api/files/folders")
    );
    assert_eq!(
        r.header("authorization"),
        Some(format!("Bearer {TOKEN}").as_str())
    );
}

#[tokio::test]
async fn create_folder_sends_json() {
    let f = fake(vec![json(201, r#"{"name":"posters","disk":"main"}"#)]).await;
    let out = client(&f)
        .files()
        .create_folder("posters", FolderOptions::default().disk("main"))
        .await
        .unwrap();
    assert_eq!(out.disk, "main");
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("POST", "/api/files/folders")
    );
    assert_eq!(r.header("content-type"), Some("application/json"));
    assert_eq!(r.json(), json!({"name":"posters","disk":"main"}));
}

#[tokio::test]
async fn create_folder_without_disk_omits_it() {
    let f = fake(vec![json(201, r#"{"name":"p"}"#)]).await;
    client(&f)
        .files()
        .create_folder("p", FolderOptions::default())
        .await
        .unwrap();
    assert_eq!(f.last().json(), json!({"name":"p"}));
}

#[tokio::test]
async fn ensure_folder_creates() {
    let f = fake(vec![json(201, r#"{"name":"p"}"#)]).await;
    let out = client(&f)
        .files()
        .ensure_folder("p", FolderOptions::default())
        .await
        .unwrap();
    assert_eq!(out.name, "p");
    assert_eq!(f.all().len(), 1);
}

#[tokio::test]
async fn ensure_folder_on_409_returns_existing() {
    let f = fake(vec![
        json(
            409,
            r#"{"error":"There is already a folder with that name."}"#,
        ),
        json(
            200,
            r#"{"folders":[{"name":"other"},{"name":"p","disk":"main","files":4}]}"#,
        ),
    ])
    .await;
    let out = client(&f)
        .files()
        .ensure_folder("p", FolderOptions::default())
        .await
        .unwrap();
    assert_eq!(out.files, 4);
    let reqs = f.all();
    assert_eq!(reqs[0].method, "POST");
    assert_eq!(
        (reqs[1].method.as_str(), reqs[1].path.as_str()),
        ("GET", "/api/files/folders")
    );
}

#[tokio::test]
async fn ensure_folder_409_but_not_visible_keeps_the_conflict() {
    let f = fake(vec![
        json(409, r#"{"error":"taken"}"#),
        json(200, r#"{"folders":[]}"#),
    ])
    .await;
    let err = client(&f)
        .files()
        .ensure_folder("p", FolderOptions::default())
        .await
        .unwrap_err();
    assert_eq!(err.status(), 409);
}

#[tokio::test]
async fn ensure_folder_other_errors_pass_through() {
    let f = fake(vec![json(403, r#"{"error":"no"}"#)]).await;
    let err = client(&f)
        .files()
        .ensure_folder("p", FolderOptions::default())
        .await
        .unwrap_err();
    assert_eq!(err.status(), 403);
    assert_eq!(f.all().len(), 1);
}

#[tokio::test]
async fn delete_folder() {
    let f = fake(vec![Canned {
        status: 204,
        headers: vec![],
        body: vec![],
    }])
    .await;
    client(&f).files().delete_folder("my folder").await.unwrap();
    let r = f.last();
    assert_eq!(r.method, "DELETE");
    assert_eq!(r.path, "/api/files/folders/my%20folder");
}

#[tokio::test]
async fn upload_sends_raw_body_and_query() {
    let f = fake(vec![json(201, FILE)]).await;
    let opts = UploadOptions::new("a b.jpg")
        .tags(["dune", "poster"])
        .sha256("ab".repeat(32));
    let up = client(&f)
        .files()
        .upload("posters", b"\x00\x01\xffraw".to_vec(), opts)
        .await
        .unwrap();
    assert!(!up.duplicate);
    assert_eq!(up.file.id, "01J9Z3X5K8Q2W4E6R8T0Y1U2I3");
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("POST", "/api/files/posters")
    );
    assert_eq!(
        r.query,
        format!("name=a+b.jpg&tags=dune%2Cposter&sha256={}", "ab".repeat(32))
    );
    assert_eq!(r.body, b"\x00\x01\xffraw");
    assert_eq!(r.header("content-type"), Some("application/octet-stream"));
}

#[tokio::test]
async fn upload_without_options_has_no_query() {
    let f = fake(vec![json(201, FILE)]).await;
    client(&f)
        .files()
        .upload("p", "hi", UploadOptions::default())
        .await
        .unwrap();
    assert_eq!(f.last().query, "");
    assert_eq!(f.last().body, b"hi");
}

#[tokio::test]
async fn upload_duplicate_header() {
    let mut c = json(200, FILE);
    c.headers.push(("X-Homelab-Duplicate", "true"));
    let f = fake(vec![c]).await;
    let up = client(&f)
        .files()
        .upload("p", "x", UploadOptions::default())
        .await
        .unwrap();
    assert!(up.duplicate);
}

#[tokio::test]
async fn upload_file_streams_from_disk() {
    let dir = std::env::temp_dir().join(format!("homelab-sdk-test-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("big.bin");
    let data: Vec<u8> = (0..300_000u32).map(|i| (i % 251) as u8).collect();
    std::fs::write(&path, &data).unwrap();
    let f = fake(vec![json(201, FILE)]).await;
    let up = client(&f)
        .files()
        .upload_file("posters", &path, UploadOptions::default().tags(["t"]))
        .await
        .unwrap();
    assert!(!up.duplicate);
    let r = f.last();
    assert_eq!(r.query, "name=big.bin&tags=t");
    assert_eq!(r.body, data);
    let err = client(&f)
        .files()
        .upload_file("posters", dir.join("missing"), UploadOptions::default())
        .await
        .unwrap_err();
    assert!(matches!(err, Error::Config(_)));
    std::fs::remove_dir_all(dir).unwrap();
}

#[tokio::test]
async fn list_sends_every_filter_and_decodes() {
    let f = fake(vec![json(
        200,
        &format!(r#"{{"files":[{FILE}],"next":"01NEXT"}}"#),
    )])
    .await;
    let opts = ListOptions::default()
        .tag("a b")
        .content_type("image/")
        .taken_after("2024-05-01")
        .taken_before("2024-05-01T10:00:00+02:00")
        .camera("canon eos")
        .after("01AFTER")
        .limit(5);
    let page = client(&f).files().list("posters", opts).await.unwrap();
    assert_eq!(page.next.as_deref(), Some("01NEXT"));
    assert_eq!(page.files[0].meta.gps.as_ref().unwrap().lat, 1.5);
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("GET", "/api/files/posters")
    );
    assert_eq!(
        r.query,
        "tag=a+b&type=image%2F&taken_after=2024-05-01&taken_before=2024-05-01T10%3A00%3A00%2B02%3A00&camera=canon+eos&after=01AFTER&limit=5"
    );
}

#[tokio::test]
async fn list_last_page_has_null_next() {
    let f = fake(vec![json(200, r#"{"files":[],"next":null}"#)]).await;
    let page = client(&f)
        .files()
        .list("p", ListOptions::default())
        .await
        .unwrap();
    assert!(page.files.is_empty() && page.next.is_none());
    assert_eq!(f.last().query, "");
}

#[tokio::test]
async fn all_follows_next_as_after() {
    let file = |id: &str| format!(r#"{{"id":"{id}"}}"#);
    let f = fake(vec![
        json(
            200,
            &format!(r#"{{"files":[{},{}],"next":"B"}}"#, file("A"), file("B")),
        ),
        json(200, &format!(r#"{{"files":[{}],"next":"C"}}"#, file("C"))),
        json(200, &format!(r#"{{"files":[{}],"next":null}}"#, file("D"))),
    ])
    .await;
    let ids: Vec<String> = client(&f)
        .files()
        .all("p", ListOptions::default().tag("t").limit(2))
        .map_ok(|x| x.id)
        .try_collect()
        .await
        .unwrap();
    assert_eq!(ids, ["A", "B", "C", "D"]);
    let reqs = f.all();
    assert_eq!(reqs.len(), 3);
    assert_eq!(reqs[0].query, "tag=t&limit=2");
    assert_eq!(reqs[1].query, "tag=t&after=B&limit=2");
    assert_eq!(reqs[2].query, "tag=t&after=C&limit=2");
}

#[tokio::test]
async fn all_surfaces_errors_and_stops() {
    let f = fake(vec![json(404, r#"{"error":"No such folder."}"#)]).await;
    let mut s = Box::pin(client(&f).files().all("nope", ListOptions::default()));
    let first = futures_util::StreamExt::next(&mut s).await.unwrap();
    assert_eq!(first.unwrap_err().status(), 404);
}

#[tokio::test]
async fn info_decodes_and_keeps_unknown_fields() {
    let f = fake(vec![json(200, FILE)]).await;
    let file = client(&f)
        .files()
        .info("posters", "01J9Z3X5K8Q2W4E6R8T0Y1U2I3")
        .await
        .unwrap();
    assert_eq!(file.tags, ["dune"]);
    assert_eq!(file.info["k"], 1);
    assert_eq!(file.meta.width, Some(10));
    assert_eq!(file.meta.extra["future"], true);
    assert_eq!(file.extra["new_field"], 7);
    let r = f.last();
    assert_eq!(r.method, "GET");
    assert_eq!(r.path, "/api/files/posters/01J9Z3X5K8Q2W4E6R8T0Y1U2I3/info");
}

#[tokio::test]
async fn decoding_tolerates_missing_fields() {
    let f = fake(vec![json(200, r#"{"id":"X"}"#)]).await;
    let file = client(&f).files().info("p", "X").await.unwrap();
    assert_eq!(file.id, "X");
    assert!(file.tags.is_empty() && file.meta == Meta::default());
}

#[tokio::test]
async fn update_sends_only_given_fields() {
    let f = fake(vec![json(200, FILE), json(200, FILE)]).await;
    let c = client(&f).files();
    c.update("p", "ID", UpdateOptions::default().tags(["x", "y"]))
        .await
        .unwrap();
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("PATCH", "/api/files/p/ID/info")
    );
    assert_eq!(r.json(), json!({"tags":["x","y"]}));
    let info = json!({"a":1}).as_object().unwrap().clone();
    c.update("p", "ID", UpdateOptions::default().info(info))
        .await
        .unwrap();
    assert_eq!(f.last().json(), json!({"info":{"a":1}}));
}

#[tokio::test]
async fn update_can_clear_tags() {
    let f = fake(vec![json(200, FILE)]).await;
    client(&f)
        .files()
        .update(
            "p",
            "ID",
            UpdateOptions::default().tags(Vec::<String>::new()),
        )
        .await
        .unwrap();
    assert_eq!(f.last().json(), json!({"tags":[]}));
}

#[tokio::test]
async fn download_returns_bytes_and_sends_range() {
    let f = fake(vec![
        Canned {
            status: 200,
            headers: vec![],
            body: b"hello".to_vec(),
        },
        Canned {
            status: 206,
            headers: vec![],
            body: b"ell".to_vec(),
        },
    ])
    .await;
    let c = client(&f).files();
    assert_eq!(
        &c.download("p", "ID", DownloadOptions::default())
            .await
            .unwrap()[..],
        b"hello"
    );
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("GET", "/api/files/p/ID")
    );
    assert_eq!(r.header("range"), None);
    let got = c
        .download("p", "ID", DownloadOptions::default().range("bytes=1-3"))
        .await
        .unwrap();
    assert_eq!(&got[..], b"ell");
    assert_eq!(f.last().header("range"), Some("bytes=1-3"));
}

#[tokio::test]
async fn download_stream_yields_chunks() {
    let f = fake(vec![Canned {
        status: 200,
        headers: vec![],
        body: vec![7u8; 100_000],
    }])
    .await;
    let chunks: Vec<Bytes> = client(&f)
        .files()
        .download_stream("p", "ID", DownloadOptions::default())
        .await
        .unwrap()
        .try_collect()
        .await
        .unwrap();
    assert_eq!(chunks.iter().map(|c| c.len()).sum::<usize>(), 100_000);
}

#[tokio::test]
async fn download_error_is_mapped() {
    let f = fake(vec![json(416, r#"{"error":"Bad range."}"#)]).await;
    let err = client(&f)
        .files()
        .download("p", "ID", DownloadOptions::default())
        .await
        .unwrap_err();
    assert_eq!(err.status(), 416);
}

#[tokio::test]
async fn delete_file() {
    let f = fake(vec![Canned {
        status: 204,
        headers: vec![],
        body: vec![],
    }])
    .await;
    client(&f).files().delete("p", "ID").await.unwrap();
    let r = f.last();
    assert_eq!(
        (r.method.as_str(), r.path.as_str()),
        ("DELETE", "/api/files/p/ID")
    );
}

#[tokio::test]
async fn folder_and_id_are_path_encoded() {
    let f = fake(vec![json(200, FILE)]).await;
    client(&f)
        .files()
        .info("a/b?c#d e%", "x\u{e9}+y")
        .await
        .unwrap();
    assert_eq!(
        f.last().path,
        "/api/files/a%2Fb%3Fc%23d%20e%25/x%C3%A9%2By/info"
    );
}

#[tokio::test]
async fn dot_segments_are_refused() {
    let f = fake(vec![]).await;
    let err = client(&f).files().info("..", "x").await.unwrap_err();
    assert!(matches!(err, Error::Config(_)));
    assert!(client(&f).files().delete("p", ".").await.is_err());
    assert!(f.all().is_empty());
}

#[tokio::test]
async fn base_url_with_prefix_and_trailing_slash() {
    let f = fake(vec![json(200, r#"{"folders":[]}"#)]).await;
    let c = Homelab::new(format!("{}/prefix//", f.url), TOKEN).unwrap();
    c.files().folders().await.unwrap();
    assert_eq!(f.last().path, "/prefix/api/files/folders");
}

#[tokio::test]
async fn api_error_uses_server_message() {
    let f = fake(vec![json(404, r#"{"error":"There is no such folder."}"#)]).await;
    let err = client(&f)
        .files()
        .list("nope", ListOptions::default())
        .await
        .unwrap_err();
    match &err {
        Error::Api { status, message } => {
            assert_eq!(*status, 404);
            assert_eq!(message, "There is no such folder.");
        }
        other => panic!("{other:?}"),
    }
    assert!(err.to_string().contains("There is no such folder."));
}

#[tokio::test]
async fn api_error_falls_back_when_body_is_not_json() {
    let f = fake(vec![Canned {
        status: 413,
        headers: vec![],
        body: b"too big".to_vec(),
    }])
    .await;
    let err = client(&f)
        .files()
        .upload("p", "x", UploadOptions::default())
        .await
        .unwrap_err();
    assert_eq!(err.status(), 413);
    assert!(err.to_string().contains("HTTP 413"));
}

#[tokio::test]
async fn network_error_has_status_zero_and_no_token() {
    // Nothing listens on port 1.
    let c = Homelab::new("http://127.0.0.1:1", TOKEN).unwrap();
    let err = c.files().folders().await.unwrap_err();
    assert!(matches!(err, Error::Network(_)));
    assert_eq!(err.status(), 0);
    assert!(!format!("{err} {err:?}").contains(TOKEN));
}

#[tokio::test]
async fn bad_json_in_success_is_a_network_error() {
    let f = fake(vec![json(200, "not json")]).await;
    let err = client(&f).files().folders().await.unwrap_err();
    assert!(matches!(err, Error::Network(_)));
}

#[test]
fn debug_does_not_show_token() {
    let c = Homelab::new("http://example.test/", TOKEN).unwrap();
    let shown = format!("{c:?} {:#?} {:?}", c, c.files());
    assert!(!shown.contains(TOKEN));
    assert!(!shown.contains("hlk_"));
    assert!(shown.contains("example.test"));
}

#[test]
fn new_validates() {
    assert!(matches!(Homelab::new("", TOKEN), Err(Error::Config(_))));
    assert!(matches!(
        Homelab::new("localhost:7085", TOKEN),
        Err(Error::Config(_))
    ));
    assert!(matches!(
        Homelab::new("http://x", " "),
        Err(Error::Config(_))
    ));
}
