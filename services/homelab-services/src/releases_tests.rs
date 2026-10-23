//! Tests for planning and publishing against a fake GitHub on 127.0.0.1.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use axum::extract::{Path, Query, Request, State};
use axum::http::StatusCode;
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde_json::{json, Value};

use crate::releases::{parse_args, plan, publish, run, Channel, Kind, Options, Repo, Version};
use crate::releases_github::GitHub;

const TOKEN: &str = "ghp_fake_token_value";

/// What the fake GitHub knows and what it was asked to do.
#[derive(Default)]
struct Fake {
    /// (name, sha)
    tags: Vec<(String, String)>,
    /// Names the fake can turn into a sha.
    commits: HashMap<String, String>,
    /// "base...head" gives these commit messages.
    compare: HashMap<String, Vec<String>>,
    /// File name gives its text (at any commit).
    files: HashMap<String, String>,
    releases: Vec<Value>,
    /// The bodies of every POST /releases.
    posted: Vec<Value>,
}

type Shared = Arc<Mutex<Fake>>;

fn sha(n: u8) -> String {
    format!("{:040x}", n)
}

fn page(Query(q): Query<HashMap<String, String>>) -> (usize, usize) {
    let per: usize = q.get("per_page").and_then(|v| v.parse().ok()).unwrap_or(30);
    let page: usize = q.get("page").and_then(|v| v.parse().ok()).unwrap_or(1);
    ((page - 1) * per, per)
}

fn slice<T: Clone>(all: &[T], q: Query<HashMap<String, String>>) -> Vec<T> {
    let (from, per) = page(q);
    all.iter().skip(from).take(per).cloned().collect()
}

async fn require_token(req: Request, next: Next) -> Response {
    let ok = req
        .headers()
        .get("authorization")
        .and_then(|v| v.to_str().ok())
        == Some(&format!("Bearer {TOKEN}"));
    if !ok {
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({"message": "Bad credentials"})),
        )
            .into_response();
    }
    next.run(req).await
}

fn not_found() -> Response {
    (StatusCode::NOT_FOUND, Json(json!({"message": "Not Found"}))).into_response()
}

async fn commit(
    State(s): State<Shared>,
    Path((_, _, rev)): Path<(String, String, String)>,
) -> Response {
    match s.lock().unwrap().commits.get(&rev) {
        Some(sha) => Json(json!({"sha": sha})).into_response(),
        None => not_found(),
    }
}

async fn tags(State(s): State<Shared>, q: Query<HashMap<String, String>>) -> Response {
    let all: Vec<Value> = s
        .lock()
        .unwrap()
        .tags
        .iter()
        .map(|(n, c)| json!({"name": n, "commit": {"sha": c}}))
        .collect();
    Json(slice(&all, q)).into_response()
}

async fn compare(
    State(s): State<Shared>,
    Path((_, _, spec)): Path<(String, String, String)>,
    q: Query<HashMap<String, String>>,
) -> Response {
    let fake = s.lock().unwrap();
    let Some(all) = fake.compare.get(&spec) else {
        return not_found();
    };
    let commits: Vec<Value> = slice(all, q)
        .iter()
        .map(|m| json!({"commit": {"message": m}}))
        .collect();
    Json(json!({"total_commits": all.len(), "commits": commits})).into_response()
}

async fn contents(
    State(s): State<Shared>,
    Path((_, _, file)): Path<(String, String, String)>,
) -> Response {
    match s.lock().unwrap().files.get(&file) {
        Some(text) => text.clone().into_response(),
        None => not_found(),
    }
}

async fn list_releases(State(s): State<Shared>, q: Query<HashMap<String, String>>) -> Response {
    let all = s.lock().unwrap().releases.clone();
    Json(slice(&all, q)).into_response()
}

async fn release_by_tag(
    State(s): State<Shared>,
    Path((_, _, tag)): Path<(String, String, String)>,
) -> Response {
    let fake = s.lock().unwrap();
    match fake.releases.iter().find(|r| r["tag_name"] == tag.as_str()) {
        Some(r) => Json(r.clone()).into_response(),
        None => not_found(),
    }
}

async fn create_release(State(s): State<Shared>, Json(body): Json<Value>) -> Response {
    let mut fake = s.lock().unwrap();
    let tag = body["tag_name"].as_str().unwrap().to_string();
    let target = body["target_commitish"].as_str().unwrap().to_string();
    if !fake.tags.iter().any(|(n, _)| *n == tag) {
        fake.tags.push((tag.clone(), target));
    }
    let mut made = body.clone();
    made["html_url"] = json!(format!("https://example.test/releases/{tag}"));
    made["created_at"] = json!("2026-01-02T03:04:05Z");
    fake.releases.insert(0, made.clone());
    fake.posted.push(body);
    (StatusCode::CREATED, Json(made)).into_response()
}

async fn start(fake: Fake) -> (GitHub, Shared) {
    let shared: Shared = Arc::new(Mutex::new(fake));
    let app = Router::new()
        .route("/repos/{o}/{r}/commits/{rev}", get(commit))
        .route("/repos/{o}/{r}/tags", get(tags))
        .route("/repos/{o}/{r}/compare/{spec}", get(compare))
        .route("/repos/{o}/{r}/contents/{*file}", get(contents))
        .route(
            "/repos/{o}/{r}/releases",
            get(list_releases).post(create_release),
        )
        .route("/repos/{o}/{r}/releases/tags/{tag}", get(release_by_tag))
        .layer(middleware::from_fn(require_token))
        .with_state(shared.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    (GitHub::new(&base, TOKEN).unwrap(), shared)
}

fn repo() -> Repo {
    Repo::parse("rupaai/demo").unwrap()
}

fn v(text: &str) -> Version {
    Version::parse(text).unwrap()
}

/// A repo whose latest prod release is `tag` on sha(1); commit "main" is sha(2).
fn released(tag: &str, messages: &[&str]) -> Fake {
    let mut fake = Fake::default();
    fake.tags.push((tag.to_string(), sha(1)));
    fake.commits.insert("main".into(), sha(2));
    fake.commits.insert(sha(1), sha(1));
    fake.compare.insert(
        format!("{tag}...{}", sha(2)),
        messages.iter().map(|m| m.to_string()).collect(),
    );
    fake
}

fn unreleased() -> Fake {
    let mut fake = Fake::default();
    fake.commits.insert("main".into(), sha(2));
    fake
}

#[tokio::test]
async fn first_release_uses_the_pyproject_version() {
    let mut fake = unreleased();
    fake.files.insert(
        "pyproject.toml".into(),
        "[project]\nname = \"demo\"\nversion = \"0.3.0\"\n".into(),
    );
    let (gh, shared) = start(fake).await;
    let done = publish(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(done.plan.version, v("v0.3.0"));
    assert_eq!(done.plan.previous, None);
    let release = done.release.unwrap();
    assert_eq!(release.tag, "v0.3.0");
    let posted = shared.lock().unwrap().posted.clone();
    assert_eq!(posted.len(), 1);
    assert_eq!(posted[0]["tag_name"], "v0.3.0");
    assert_eq!(posted[0]["name"], "v0.3.0");
    assert_eq!(posted[0]["target_commitish"], sha(2));
    assert_eq!(posted[0]["prerelease"], false);
}

#[tokio::test]
async fn first_release_falls_back_to_other_files_then_to_zero_one_zero() {
    let mut fake = unreleased();
    fake.files
        .insert("package.json".into(), "{\"version\":\"1.4.2\"}".into());
    fake.files.insert(
        "Cargo.toml".into(),
        "[package]\nversion = \"9.9.9\"\n".into(),
    );
    let (gh, _) = start(fake).await;
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.version, v("v1.4.2"));

    let mut fake = unreleased();
    fake.files.insert(
        "Cargo.toml".into(),
        "[package]\nversion = \"2.0.1\"\n".into(),
    );
    let (gh, _) = start(fake).await;
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.version, v("v2.0.1"));

    let (gh, _) = start(unreleased()).await;
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.version, v("v0.1.0"));
    assert!(p.release);
}

#[tokio::test]
async fn first_option_wins_over_the_files() {
    let mut fake = unreleased();
    fake.files
        .insert("package.json".into(), "{\"version\":\"1.4.2\"}".into());
    let (gh, _) = start(fake).await;
    let options = Options {
        first: Some(v("v0.7.0")),
        always: false,
    };
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &options)
        .await
        .unwrap();
    assert_eq!(p.version, v("v0.7.0"));
}

#[tokio::test]
async fn promote_bumps_by_the_biggest_change() {
    let fake = released(
        "v0.3.0",
        &[
            "Merge pull request #5 from a/b\n\nfeat: add x",
            "fix: mend y (#6)",
            "docs: words (#7)",
            "Merge branch 'main' into b",
        ],
    );
    let (gh, shared) = start(fake).await;
    let done = publish(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(done.plan.version, v("v0.4.0"));
    assert_eq!(done.plan.previous, Some(v("v0.3.0")));
    assert_eq!(done.plan.summary, "1 new, 1 fix");
    assert_eq!(
        done.plan.notes,
        "## New\n\n- add x (#5)\n\n## Fixes\n\n- mend y (#6)\n"
    );
    let posted = shared.lock().unwrap().posted.clone();
    assert_eq!(posted[0]["body"], done.plan.notes.as_str());
}

#[tokio::test]
async fn breaking_before_one_point_zero_raises_the_middle_number() {
    let (gh, _) = start(released("v0.19.4", &["feat!: new shape (#9)"])).await;
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.version, v("v0.20.0"));
    assert_eq!(p.changes[0].kind, Kind::Breaking);

    let (gh, _) = start(released("v1.2.0", &["feat!: new shape (#9)"])).await;
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.version, v("v2.0.0"));
}

#[tokio::test]
async fn the_latest_prod_tag_is_the_highest_not_the_newest() {
    let mut fake = released("v0.10.0", &["fix: a (#1)"]);
    fake.tags.push(("v0.9.0".into(), sha(1)));
    fake.tags.push(("v0.11.0-dev.4".into(), sha(1)));
    fake.tags.push(("not-a-version".into(), sha(1)));
    let (gh, _) = start(fake).await;
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.previous, Some(v("v0.10.0")));
    assert_eq!(p.version, v("v0.10.1"));
}

#[tokio::test]
async fn dev_numbers_count_up_and_are_made_as_pre_releases() {
    let mut fake = released("v0.3.0", &["feat: add x (#5)"]);
    let (gh, shared) = start({
        fake.commits.insert("next".into(), sha(3));
        fake.compare.insert(
            format!("v0.3.0...{}", sha(3)),
            vec!["feat: add x (#5)".into(), "fix: mend (#6)".into()],
        );
        fake
    })
    .await;
    let first = publish(&gh, &repo(), "main", &Channel::Dev, &Options::default())
        .await
        .unwrap();
    assert_eq!(first.plan.version, v("v0.4.0-dev.1"));
    let second = publish(&gh, &repo(), "next", &Channel::Dev, &Options::default())
        .await
        .unwrap();
    assert_eq!(second.plan.version, v("v0.4.0-dev.2"));
    let posted = shared.lock().unwrap().posted.clone();
    assert_eq!(posted.len(), 2);
    assert_eq!(posted[1]["prerelease"], true);
    assert_eq!(posted[1]["tag_name"], "v0.4.0-dev.2");

    // The same commit again gives the same version and makes nothing new.
    let again = publish(&gh, &repo(), "next", &Channel::Dev, &Options::default())
        .await
        .unwrap();
    assert_eq!(again.plan.version, v("v0.4.0-dev.2"));
    assert!(again.plan.existing);
    assert_eq!(shared.lock().unwrap().posted.len(), 2);
}

#[tokio::test]
async fn test_versions_use_the_pr_number_and_add_the_pr_title() {
    let (gh, shared) = start(released("v0.19.2", &["fix: small thing (#3)"])).await;
    let channel = Channel::Test {
        pr: 27,
        title: "feat(ui): shiny button".into(),
    };
    let done = publish(&gh, &repo(), "main", &channel, &Options::default())
        .await
        .unwrap();
    assert_eq!(done.plan.version, v("v0.20.0-pr.27"));
    assert!(done.plan.notes.contains("- shiny button (#27)"));
    assert!(done.plan.notes.contains("- small thing (#3)"));
    let posted = shared.lock().unwrap().posted.clone();
    assert_eq!(posted[0]["prerelease"], true);
    assert_eq!(posted[0]["tag_name"], "v0.20.0-pr.27");
}

#[tokio::test]
async fn a_test_tag_on_another_commit_is_an_error() {
    let mut fake = released("v0.19.2", &[]);
    fake.tags.push(("v0.19.3-pr.27".into(), sha(9)));
    let (gh, _) = start(fake).await;
    let channel = Channel::Test {
        pr: 27,
        title: "fix: again".into(),
    };
    let err = plan(&gh, &repo(), "main", &channel, &Options::default())
        .await
        .unwrap_err();
    assert!(
        err.contains("v0.19.3-pr.27") && err.contains("another commit"),
        "{err}"
    );
}

#[tokio::test]
async fn nothing_to_release_when_only_quiet_changes() {
    let (gh, shared) = start(released(
        "v0.3.0",
        &["docs: words (#1)", "chore: tidy (#2)"],
    ))
    .await;
    let done = publish(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert!(!done.plan.release);
    assert!(done.release.is_none());
    assert_eq!(done.plan.version, v("v0.3.0"));
    assert!(shared.lock().unwrap().posted.is_empty());
}

#[tokio::test]
async fn always_bumps_the_last_number() {
    let (gh, _) = start(released("v0.3.0", &["docs: words (#1)"])).await;
    let options = Options {
        first: None,
        always: true,
    };
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &options)
        .await
        .unwrap();
    assert!(p.release);
    assert_eq!(p.version, v("v0.3.1"));
}

#[tokio::test]
async fn the_same_commit_twice_gives_the_same_release() {
    let (gh, shared) = start(released("v0.3.0", &["feat: add x (#5)"])).await;
    let first = publish(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(first.plan.version, v("v0.4.0"));
    let second = publish(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert!(second.plan.release && second.plan.existing);
    assert_eq!(second.plan.version, v("v0.4.0"));
    assert_eq!(second.release.unwrap().tag, "v0.4.0");
    assert_eq!(shared.lock().unwrap().posted.len(), 1);
}

#[tokio::test]
async fn create_release_returns_the_existing_one_and_refuses_another_commit() {
    let mut fake = Fake::default();
    fake.tags.push(("v0.4.0".into(), sha(2)));
    fake.releases.push(
        json!({"tag_name": "v0.4.0", "name": "v0.4.0", "body": "old notes", "prerelease": false}),
    );
    let (gh, shared) = start(fake).await;
    let r = gh
        .create_release(&repo(), "v0.4.0", &sha(2), "new notes", false)
        .await
        .unwrap();
    assert_eq!(r.body, "old notes");
    assert!(shared.lock().unwrap().posted.is_empty());

    let err = gh
        .create_release(&repo(), "v0.4.0", &sha(3), "x", false)
        .await
        .unwrap_err();
    assert!(err.contains("already exists on another commit"), "{err}");
    assert!(shared.lock().unwrap().posted.is_empty());
}

#[tokio::test]
async fn a_tag_without_a_release_gets_its_release() {
    let mut fake = Fake::default();
    fake.tags.push(("v0.4.0".into(), sha(2)));
    let (gh, shared) = start(fake).await;
    let r = gh
        .create_release(&repo(), "v0.4.0", &sha(2), "notes", false)
        .await
        .unwrap();
    assert_eq!(r.tag, "v0.4.0");
    assert_eq!(shared.lock().unwrap().posted.len(), 1);
}

#[tokio::test]
async fn long_lists_are_read_page_by_page() {
    let mut fake = released("v0.3.0", &[]);
    let messages: Vec<String> = (1..=250)
        .map(|n| format!("fix: thing {n} (#{n})"))
        .collect();
    fake.compare
        .insert(format!("v0.3.0...{}", sha(2)), messages);
    for n in 0..150 {
        fake.tags.push((format!("v0.1.{n}"), sha(1)));
    }
    let (gh, _) = start(fake).await;
    assert_eq!(gh.list_tags(&repo()).await.unwrap().len(), 151);
    let p = plan(&gh, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap();
    assert_eq!(p.changes.len(), 250);
    assert_eq!(p.version, v("v0.3.1"));
}

#[tokio::test]
async fn releases_are_listed_with_their_commits() {
    let mut fake = Fake::default();
    fake.tags.push(("v0.4.0".into(), sha(2)));
    fake.releases.push(json!({
        "tag_name": "v0.4.0", "name": "v0.4.0", "body": "notes", "prerelease": false,
        "target_commitish": "main", "published_at": "2026-02-03T00:00:00Z", "html_url": "https://x/y",
    }));
    let (gh, _) = start(fake).await;
    let list = gh.list_releases(&repo()).await.unwrap();
    assert_eq!(list.len(), 1);
    assert_eq!(list[0].tag, "v0.4.0");
    assert_eq!(list[0].commit, sha(2));
    assert_eq!(list[0].date, "2026-02-03T00:00:00Z");
    assert_eq!(list[0].body, "notes");
    assert!(!list[0].prerelease);
}

#[tokio::test]
async fn errors_are_clear_and_never_show_the_token() {
    let (gh, _) = start(unreleased()).await;
    let err = plan(&gh, &repo(), "nope", &Channel::Prod, &Options::default())
        .await
        .unwrap_err();
    assert!(err.contains("no commit nope"), "{err}");

    let bad = GitHub::new(&gh_base(&gh), "wrong-token-xyz").unwrap();
    let err = plan(&bad, &repo(), "main", &Channel::Prod, &Options::default())
        .await
        .unwrap_err();
    assert!(
        err.contains("Bad credentials") && err.contains("GITHUB_TOKEN"),
        "{err}"
    );
    assert!(!err.contains("wrong-token-xyz") && !err.contains(TOKEN));

    let err = plan(&gh, &repo(), "../etc", &Channel::Prod, &Options::default())
        .await
        .unwrap_err();
    assert!(err.contains("not a usable"), "{err}");
}

/// The base address of a client made by `start`.
fn gh_base(gh: &GitHub) -> String {
    gh.base().to_string()
}

fn cli(line: &str) -> crate::releases::Cli {
    let args: Vec<String> = line.split_whitespace().map(String::from).collect();
    parse_args(&args).unwrap()
}

#[tokio::test]
async fn the_command_prints_the_version_and_the_notes() {
    let (gh, shared) = start(released("v0.3.0", &["feat: add x (#5)"])).await;
    let (mut out, mut err) = (Vec::new(), Vec::new());
    run(
        &cli("prod --repo rupaai/demo --commit main --dry-run"),
        &gh,
        &mut out,
        &mut err,
    )
    .await
    .unwrap();
    assert_eq!(String::from_utf8(out).unwrap(), "v0.4.0\n");
    let err = String::from_utf8(err).unwrap();
    assert!(
        err.contains("- add x (#5)") && err.contains("Dry run"),
        "{err}"
    );
    assert!(shared.lock().unwrap().posted.is_empty());

    let (mut out, mut err) = (Vec::new(), Vec::new());
    run(
        &cli("prod --repo rupaai/demo --commit main"),
        &gh,
        &mut out,
        &mut err,
    )
    .await
    .unwrap();
    assert_eq!(String::from_utf8(out).unwrap(), "v0.4.0\n");
    assert_eq!(shared.lock().unwrap().posted.len(), 1);
}

#[tokio::test]
async fn the_command_prints_nothing_when_there_is_nothing_to_release() {
    let (gh, shared) = start(released("v0.3.0", &["ci: pipeline (#5)"])).await;
    let (mut out, mut err) = (Vec::new(), Vec::new());
    run(
        &cli("prod --repo rupaai/demo --commit main"),
        &gh,
        &mut out,
        &mut err,
    )
    .await
    .unwrap();
    assert!(out.is_empty());
    assert!(String::from_utf8(err)
        .unwrap()
        .contains("Nothing to release since v0.3.0"));
    assert!(shared.lock().unwrap().posted.is_empty());
}

// ---------------------------------------------------------------------------------------
// The HTTP routes

mod routes {
    use super::*;
    use crate::releases_api::{REPO_HEADER, SCOPE_READ, TOKEN_HEADER};
    use crate::testutil::{admin, app_scoped, person, request, system, test_env_github};
    use axum::http::Method;
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    /// Sends one signed request and gives the status and the JSON body.
    async fn call(
        github: &GitHub,
        who: &Value,
        method: Method,
        target: &str,
        body: Value,
        extra: &[(&str, &str)],
    ) -> (StatusCode, Value) {
        let env = test_env_github(1024, github.base());
        let mut headers = vec![("content-type", "application/json")];
        headers.extend_from_slice(extra);
        let bytes = if body.is_null() {
            vec![]
        } else {
            serde_json::to_vec(&body).unwrap()
        };
        let req = request(who, method.as_str(), target, bytes, &headers);
        let res = env.router.clone().oneshot(req).await.unwrap();
        let status = res.status();
        let bytes = res.into_body().collect().await.unwrap().to_bytes();
        (
            status,
            serde_json::from_slice(&bytes).unwrap_or(Value::Null),
        )
    }

    fn publish_body(channel: &str) -> Value {
        json!({"repo": "rupaai/demo", "commit": "main", "channel": channel, "token": TOKEN, "always": true})
    }

    #[tokio::test]
    async fn publish_makes_a_dev_pre_release_and_answers_with_the_notes() {
        let (gh, shared) = start(released(
            "v0.3.0",
            &["feat: add x (#5)", "feat: add y (#6)", "fix: z (#7)"],
        ))
        .await;
        let (status, out) = call(
            &gh,
            &system(),
            Method::POST,
            "/internal/releases/publish",
            publish_body("dev"),
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK, "{out}");
        assert_eq!(out["version"], "v0.4.0-dev.1");
        assert_eq!(out["summary"], "2 new, 1 fix");
        assert_eq!(out["existing"], false);
        assert_eq!(out["released"], true);
        assert_eq!(out["previous"], "v0.3.0");
        assert!(out["notes"].as_str().unwrap().contains("## New"));
        let posted = shared.lock().unwrap().posted.clone();
        assert_eq!(posted.len(), 1);
        assert_eq!(posted[0]["tag_name"], "v0.4.0-dev.1");
        assert_eq!(posted[0]["prerelease"], true);
    }

    #[tokio::test]
    async fn publish_prod_after_dev_is_the_same_commit_and_existing_on_the_second_call() {
        let (gh, shared) = start(released("v0.3.0", &["feat: add x (#5)"])).await;
        for existing in [false, true] {
            let (status, out) = call(
                &gh,
                &system(),
                Method::POST,
                "/internal/releases/publish",
                publish_body("prod"),
                &[],
            )
            .await;
            assert_eq!(status, StatusCode::OK, "{out}");
            assert_eq!(out["version"], "v0.4.0");
            assert_eq!(out["existing"], existing);
        }
        let posted = shared.lock().unwrap().posted.clone();
        assert_eq!(posted.len(), 1);
        assert_eq!(posted[0]["target_commitish"], sha(2));
        assert_eq!(posted[0]["prerelease"], false);
    }

    #[tokio::test]
    async fn publish_dry_run_makes_nothing() {
        let (gh, shared) = start(released("v0.3.0", &["fix: a (#5)"])).await;
        let mut body = publish_body("prod");
        body["dry_run"] = json!(true);
        let (status, out) = call(
            &gh,
            &system(),
            Method::POST,
            "/internal/releases/publish",
            body,
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(out["version"], "v0.3.1");
        assert!(shared.lock().unwrap().posted.is_empty());
    }

    #[tokio::test]
    async fn publish_bootstraps_the_first_release_from_the_manifest() {
        let mut fake = unreleased();
        fake.files.insert(
            "pyproject.toml".into(),
            "[project]\nversion = \"0.3.0\"\n".into(),
        );
        let (gh, _) = start(fake).await;
        let (status, out) = call(
            &gh,
            &system(),
            Method::POST,
            "/internal/releases/publish",
            publish_body("prod"),
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(out["version"], "v0.3.0");
        assert_eq!(out["previous"], "");
    }

    #[tokio::test]
    async fn publish_checks_its_input() {
        let (gh, _) = start(unreleased()).await;
        for (field, value) in [
            ("channel", json!("test")),
            ("repo", json!("not a repo")),
            ("token", json!("  ")),
        ] {
            let mut body = publish_body("dev");
            body[field] = value;
            let (status, _) = call(
                &gh,
                &system(),
                Method::POST,
                "/internal/releases/publish",
                body,
                &[],
            )
            .await;
            assert_eq!(status, StatusCode::BAD_REQUEST, "{field}");
        }
    }

    #[tokio::test]
    async fn a_token_github_refuses_is_a_403_without_the_token_in_the_answer() {
        let (gh, shared) = start(released("v0.3.0", &["fix: a (#5)"])).await;
        let mut body = publish_body("dev");
        body["token"] = json!("ghp_wrong_token_value");
        let (status, out) = call(
            &gh,
            &system(),
            Method::POST,
            "/internal/releases/publish",
            body,
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        assert!(out["error"].as_str().unwrap().contains("401"));
        assert!(!out.to_string().contains("ghp_wrong_token_value"));
        assert!(shared.lock().unwrap().posted.is_empty());
    }

    #[tokio::test]
    async fn other_github_problems_are_424() {
        let (gh, _) = start(unreleased()).await;
        let mut body = publish_body("dev");
        body["commit"] = json!("nope");
        let (status, out) = call(
            &gh,
            &system(),
            Method::POST,
            "/internal/releases/publish",
            body,
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::FAILED_DEPENDENCY, "{out}");
        assert!(out["error"].as_str().unwrap().contains("no commit"));
    }

    fn with_releases() -> Fake {
        let mut fake = Fake::default();
        fake.tags.push(("v0.3.0".into(), sha(1)));
        fake.tags.push(("v0.4.0-dev.1".into(), sha(2)));
        fake.releases.push(json!({
            "tag_name": "v0.4.0-dev.1", "prerelease": true, "published_at": "2026-02-01T00:00:00Z",
            "body": "## New\n\n- add x (#5)\n- add y (#6)\n\n## Fixes\n\n- z (#7)\n",
        }));
        fake.releases.push(json!({
            "tag_name": "v0.3.0", "prerelease": false, "published_at": "2026-01-01T00:00:00Z",
            "body": "",
        }));
        fake
    }

    #[tokio::test]
    async fn the_list_has_tag_commit_date_summary_and_notes() {
        let (gh, _) = start(with_releases()).await;
        let body = json!({"repo": "rupaai/demo", "token": TOKEN});
        let (status, out) = call(
            &gh,
            &system(),
            Method::POST,
            "/internal/releases/list",
            body,
            &[],
        )
        .await;
        assert_eq!(status, StatusCode::OK, "{out}");
        let list = out["releases"].as_array().unwrap();
        assert_eq!(list.len(), 2);
        assert_eq!(list[0]["tag"], "v0.4.0-dev.1");
        assert_eq!(list[0]["prerelease"], true);
        assert_eq!(list[0]["commit"], sha(2));
        assert_eq!(list[0]["date"], "2026-02-01T00:00:00Z");
        assert_eq!(list[0]["summary"], "2 new, 1 fix");
        assert!(list[0]["notes"].as_str().unwrap().contains("add x"));
        assert_eq!(list[1]["summary"], "");
        assert!(!out.to_string().contains(TOKEN));
    }

    #[tokio::test]
    async fn an_app_lists_its_own_releases_with_its_key() {
        let (gh, _) = start(with_releases()).await;
        let who = app_scoped("demo", "prod", &[SCOPE_READ]);
        let extra = [(REPO_HEADER, "rupaai/demo"), (TOKEN_HEADER, TOKEN)];
        let (status, out) =
            call(&gh, &who, Method::GET, "/api/releases", Value::Null, &extra).await;
        assert_eq!(status, StatusCode::OK, "{out}");
        assert_eq!(out["releases"].as_array().unwrap().len(), 2);
        assert!(!out.to_string().contains(TOKEN));

        let (status, out) = call(
            &gh,
            &who,
            Method::GET,
            "/api/releases?limit=1",
            Value::Null,
            &extra,
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(out["releases"].as_array().unwrap().len(), 1);
        let (status, _) = call(
            &gh,
            &who,
            Method::GET,
            "/api/releases?limit=0",
            Value::Null,
            &extra,
        )
        .await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn an_app_needs_the_scope_and_the_repo_homelabd_adds() {
        let (gh, _) = start(with_releases()).await;
        let extra = [(REPO_HEADER, "rupaai/demo"), (TOKEN_HEADER, TOKEN)];
        let no_scope = app_scoped("demo", "prod", &["users:read"]);
        let (status, _) = call(
            &gh,
            &no_scope,
            Method::GET,
            "/api/releases",
            Value::Null,
            &extra,
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);

        let who = app_scoped("demo", "prod", &[SCOPE_READ]);
        let (status, _) = call(&gh, &who, Method::GET, "/api/releases", Value::Null, &[]).await;
        assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE);
    }

    #[tokio::test]
    async fn people_admin_keys_and_apps_are_kept_out_of_the_wrong_routes() {
        let (gh, _) = start(with_releases()).await;
        let extra = [(REPO_HEADER, "rupaai/demo"), (TOKEN_HEADER, TOKEN)];
        for who in [person(), admin(&[SCOPE_READ])] {
            let (status, _) =
                call(&gh, &who, Method::GET, "/api/releases", Value::Null, &extra).await;
            assert_eq!(status, StatusCode::FORBIDDEN);
        }
        let app = app_scoped("demo", "prod", &[SCOPE_READ]);
        for who in [person(), admin(&[SCOPE_READ]), app] {
            for path in [
                "/internal/releases/publish",
                "/internal/releases/list",
                "/internal/releases/other",
            ] {
                let (status, _) =
                    call(&gh, &who, Method::POST, path, publish_body("dev"), &[]).await;
                assert_eq!(status, StatusCode::FORBIDDEN, "{path}");
            }
        }
        // homelabd itself is not an app.
        let (status, _) = call(
            &gh,
            &system(),
            Method::GET,
            "/api/releases",
            Value::Null,
            &extra,
        )
        .await;
        assert_eq!(status, StatusCode::FORBIDDEN);
    }
}

#[test]
fn a_summary_can_be_read_back_from_notes() {
    use crate::releases::{notes, summary, summary_of_notes, Change};
    let changes: Vec<Change> = [
        (Kind::Breaking, "gone"),
        (Kind::New, "a"),
        (Kind::New, "b"),
        (Kind::Fix, "c"),
        (Kind::Fix, "d"),
    ]
    .iter()
    .enumerate()
    .map(|(i, (kind, text))| Change {
        kind: *kind,
        text: text.to_string(),
        pr: Some(i as u64 + 1),
    })
    .collect();
    assert_eq!(summary_of_notes(&notes(&changes)), summary(&changes));
    assert_eq!(
        summary_of_notes(&notes(&changes)),
        "1 breaking, 2 new, 2 fixes"
    );
    assert_eq!(summary_of_notes(""), "");
    assert_eq!(summary_of_notes("Just some words\n- not a change"), "");
}
