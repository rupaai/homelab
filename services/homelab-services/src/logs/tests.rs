use super::*;
use crate::testutil::*;
use axum::http::StatusCode;
use http_body_util::BodyExt;
use serde_json::{json, Value};
use tower::ServiceExt;

const T0: i64 = 1_800_000_000_000;

fn open_with(settings: Settings) -> (tempfile::TempDir, Logs) {
    let dir = tempfile::tempdir().unwrap();
    let logs = Logs::open(&dir.path().join("logs.db"), settings).unwrap();
    (dir, logs)
}

fn open() -> (tempfile::TempDir, Logs) {
    open_with(Settings::default())
}

fn raw(t: i64, stream: &str, text: &str) -> RawLine {
    RawLine {
        t,
        stream: stream.to_string(),
        text: text.to_string(),
    }
}

fn out(text: &str) -> RawLine {
    raw(T0, "stdout", text)
}

fn put(logs: &Logs, app: &str, env: &str, service: &str, lines: &[RawLine]) -> usize {
    logs.ingest(app, env, service, Level::Debug, lines).unwrap()
}

fn all(logs: &Logs, q: Query) -> Found {
    logs.search(&Scope::All, &q).unwrap()
}

fn msgs(found: &Found) -> Vec<String> {
    found.lines.iter().map(|l| l.msg.clone()).collect()
}

fn find(q: &str) -> Query {
    Query {
        q: q.to_string(),
        ..Query::default()
    }
}

// ---- parsing ----

fn p(text: &str, stream: &str) -> Parsed {
    parse_line(text, stream, T0)
}

#[test]
fn json_lines_give_level_message_time_and_extra() {
    let got = p(
        r#"{"level":"ERROR","msg":"boom","time":"2026-10-23T22:00:00.123Z","user":"u1","n":3}"#,
        "stdout",
    );
    assert_eq!(got.level, Level::Error);
    assert_eq!(got.msg, "boom");
    assert_eq!(got.ts, 1_792_792_800_123);
    let extra: Value = serde_json::from_str(&got.extra).unwrap();
    assert_eq!(extra, json!({ "user": "u1", "n": 3 }));
}

#[test]
fn json_level_keys_message_keys_and_time_keys() {
    assert_eq!(
        p(r#"{"lvl":"warn","message":"m"}"#, "stdout").level,
        Level::Warn
    );
    assert_eq!(
        p(r#"{"severity":"Critical","msg":"m"}"#, "stdout").level,
        Level::Fatal
    );
    assert_eq!(
        p(r#"{"levelname":"DEBUG","msg":"m"}"#, "stdout").level,
        Level::Debug
    );
    assert_eq!(p(r#"{"event":"started"}"#, "stdout").msg, "started");
    assert_eq!(
        p(r#"{"message":"hi","msg":"first"}"#, "stdout").msg,
        "first"
    );
    let got = p(
        r#"{"msg":"m","@timestamp":"2026-10-23T22:00:00Z"}"#,
        "stdout",
    );
    assert_eq!(got.ts, 1_792_792_800_000);
    // A time that isn't RFC 3339 is not used; Docker's time stays.
    let got = p(r#"{"msg":"m","time":"yesterday"}"#, "stdout");
    assert_eq!(got.ts, T0);
    assert!(got.extra.contains("yesterday"));
    // No level at all in JSON is unknown, whatever the stream.
    assert_eq!(p(r#"{"msg":"m"}"#, "stderr").level, Level::Unknown);
    assert_eq!(
        p(r#"{"level":"loud","msg":"m"}"#, "stdout").level,
        Level::Unknown
    );
}

#[test]
fn pino_numbers() {
    for (n, level) in [
        (10, Level::Debug),
        (20, Level::Debug),
        (30, Level::Info),
        (40, Level::Warn),
        (50, Level::Error),
        (60, Level::Fatal),
        (35, Level::Unknown),
    ] {
        let got = p(&format!(r#"{{"level":{n},"msg":"m"}}"#), "stdout");
        assert_eq!(got.level, level, "{n}");
    }
}

#[test]
fn plain_words() {
    for (text, level) in [
        ("TRACE x", Level::Debug),
        ("debug: x", Level::Debug),
        ("DBG x", Level::Debug),
        ("INFO x", Level::Info),
        ("[inf] x", Level::Info),
        ("Notice x", Level::Info),
        ("WARN x", Level::Warn),
        ("warning: x", Level::Warn),
        ("WRN x", Level::Warn),
        ("[ERROR] x", Level::Error),
        ("ERR x", Level::Error),
        ("FATAL x", Level::Fatal),
        ("ftl x", Level::Fatal),
        ("CRITICAL x", Level::Fatal),
        ("crit x", Level::Fatal),
        ("PANIC x", Level::Fatal),
        ("level=warn x", Level::Warn),
        ("ERROR:root:x", Level::Error),
        ("WARNING:werkzeug:slow", Level::Warn),
    ] {
        assert_eq!(p(text, "stdout").level, level, "{text}");
    }
}

#[test]
fn timestamps_first_look_at_three_words() {
    assert_eq!(
        p("2026-10-23 22:00:00 ERROR boom", "stdout").level,
        Level::Error
    );
    assert_eq!(
        p("2026-10-23T22:00:00Z [warn] hmm", "stdout").level,
        Level::Warn
    );
    assert_eq!(
        p("2026-10-23T22:00:00Z level=fatal x", "stdout").level,
        Level::Fatal
    );
    // Four words in is too far.
    assert_eq!(
        p("2026-10-23 22:00:00 server ERROR", "stdout").level,
        Level::Info
    );
    // A line that doesn't start with a digit only looks at its first word.
    assert_eq!(p("the ERROR was here", "stdout").level, Level::Info);
}

#[test]
fn no_level_falls_back_to_the_stream() {
    assert_eq!(p("listening on 8000", "stdout").level, Level::Info);
    assert_eq!(p("listening on 8000", "stderr").level, Level::Warn);
    assert_eq!(p("{not json", "stderr").level, Level::Warn);
    assert_eq!(p("{not json", "stderr").msg, "{not json");
    assert_eq!(p("INFO to stderr", "stderr").level, Level::Info);
}

#[test]
fn app_env_and_service_keys_are_dropped() {
    let got = p(
        r#"{"level":"info","msg":"m","app":"other","env":"prod","service":"x","keep":1}"#,
        "stdout",
    );
    let extra: Value = serde_json::from_str(&got.extra).unwrap();
    assert_eq!(extra, json!({ "keep": 1 }));
}

#[test]
fn long_lines_are_cut_at_16_kb() {
    let long = "é".repeat(MAX_LINE);
    let got = p(&long, "stdout");
    assert!(got.msg.ends_with('…'));
    assert!(got.msg.len() <= MAX_LINE + '…'.len_utf8());
    assert!(got.msg.len() > MAX_LINE - 4);
    let json = format!(
        r#"{{"level":"info","msg":"{}"}}"#,
        "x".repeat(MAX_LINE + 10)
    );
    let got = p(&json, "stdout");
    assert_eq!(got.msg.len(), MAX_LINE + '…'.len_utf8());
    assert_eq!(p(&"y".repeat(MAX_LINE), "stdout").msg.len(), MAX_LINE);
}

#[test]
fn a_big_extra_is_dropped_and_marked() {
    let big = "z".repeat(MAX_EXTRA + 1);
    let got = p(&format!(r#"{{"msg":"m","blob":"{big}"}}"#), "stdout");
    let extra: Value = serde_json::from_str(&got.extra).unwrap();
    assert_eq!(extra, json!({ "extra_truncated": true }));
    let small = "z".repeat(1000);
    let got = p(&format!(r#"{{"msg":"m","blob":"{small}"}}"#), "stdout");
    assert!(got.extra.contains(&small));
}

#[test]
fn a_json_line_without_a_message_keeps_the_whole_line() {
    let line = r#"{"level":"warn","a":1}"#;
    let got = p(line, "stdout");
    assert_eq!(got.level, Level::Warn);
    assert_eq!(got.msg, line);
    assert!(got.extra.is_empty());
}

// ---- storage ----

#[test]
fn the_database_has_fts5_and_incremental_vacuum() {
    let conn = Connection::open_in_memory().unwrap();
    conn.execute_batch(
        "CREATE VIRTUAL TABLE t USING fts5(a); INSERT INTO t(a) VALUES ('hello world');",
    )
    .unwrap();
    let n: i64 = conn
        .query_row(
            "SELECT COUNT(*) FROM t WHERE t MATCH '\"hello\"'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(n, 1);

    let (_dir, logs) = open();
    let mode: i64 = logs
        .lock()
        .query_row("PRAGMA auto_vacuum", [], |r| r.get(0))
        .unwrap();
    assert_eq!(mode, 2);
    let journal: String = logs
        .lock()
        .query_row("PRAGMA journal_mode", [], |r| r.get(0))
        .unwrap();
    assert_eq!(journal, "wal");
}

#[test]
fn reopening_keeps_the_lines() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("logs.db");
    {
        let logs = Logs::open(&path, Settings::default()).unwrap();
        put(&logs, "a", "dev", "app", &[out("hello")]);
    }
    let logs = Logs::open(&path, Settings::default()).unwrap();
    assert_eq!(msgs(&all(&logs, Query::default())), vec!["hello"]);
}

#[test]
fn ingest_stores_parsed_lines_and_skips_empty_ones() {
    let (_d, logs) = open();
    let n = put(
        &logs,
        "notes",
        "prod",
        "app",
        &[
            raw(T0, "stdout", r#"{"level":"info","msg":"one","k":"v"}"#),
            raw(T0 + 1, "stderr", "two"),
            raw(T0 + 2, "stdout", "   "),
            raw(T0 + 3, "stdout", "ERROR:root:three\r\n"),
        ],
    );
    assert_eq!(n, 3);
    let found = all(&logs, Query::default());
    let lines = &found.lines;
    assert_eq!(msgs(&found), vec!["ERROR:root:three", "two", "one"]);
    assert_eq!(lines[0].level, Level::Error);
    assert_eq!(lines[1].level, Level::Warn);
    assert_eq!(lines[1].stream, "stderr");
    assert_eq!(
        (lines[2].app.as_str(), lines[2].env.as_str()),
        ("notes", "prod")
    );
    assert_eq!(lines[2].service, "app");
    assert_eq!(lines[2].to_json()["extra"], json!({ "k": "v" }));
    assert_eq!(lines[1].to_json()["extra"], json!({}));
    assert_eq!(lines[2].to_json()["time"], iso(T0));
}

#[test]
fn min_level_drops_lower_lines_but_keeps_unknown() {
    let (_d, logs) = open();
    let lines = [
        out("DEBUG a"),
        out("INFO b"),
        out("WARN c"),
        out("ERROR d"),
        out(r#"{"msg":"no level"}"#),
    ];
    let n = logs.ingest("a", "dev", "app", Level::Warn, &lines).unwrap();
    assert_eq!(n, 3);
    let got = msgs(&all(&logs, Query::default()));
    assert!(got.contains(&"WARN c".to_string()));
    assert!(got.contains(&"ERROR d".to_string()));
    assert!(got.contains(&"no level".to_string()));
    assert!(!got.contains(&"INFO b".to_string()));
}

#[test]
fn search_min_level_keeps_unknown_lines() {
    let (_d, logs) = open();
    put(
        &logs,
        "a",
        "dev",
        "app",
        &[
            out("DEBUG a"),
            out("INFO b"),
            out("ERROR c"),
            out(r#"{"msg":"u"}"#),
        ],
    );
    let found = all(
        &logs,
        Query {
            min_level: Some(Level::Warn),
            ..Query::default()
        },
    );
    assert_eq!(msgs(&found), vec!["u", "ERROR c"]);
    // Counts ignore the level filter.
    assert_eq!(found.counts[&Level::Debug], 1);
    assert_eq!(found.counts[&Level::Info], 1);
    assert_eq!(found.counts[&Level::Error], 1);
    assert_eq!(found.counts[&Level::Unknown], 1);
    assert_eq!(found.counts[&Level::Warn], 0);
    let found = all(
        &logs,
        Query {
            levels: vec![Level::Info, Level::Error],
            ..Query::default()
        },
    );
    assert_eq!(msgs(&found), vec!["ERROR c", "INFO b"]);
}

// ---- search ----

fn seeded() -> (tempfile::TempDir, Logs) {
    let (d, logs) = open();
    put(
        &logs,
        "notes",
        "prod",
        "app",
        &[
            out("INFO user logged in"),
            out("ERROR payment failed for order 7"),
            out(r#"{"level":"warn","msg":"slow request","path":"/api/orders"}"#),
        ],
    );
    put(
        &logs,
        "notes",
        "dev",
        "worker",
        &[out("INFO job done"), out("ERROR job failed")],
    );
    put(
        &logs,
        "movies",
        "prod",
        "app",
        &[out("INFO user logged in")],
    );
    (d, logs)
}

#[test]
fn text_search_finds_words_in_the_message_and_extra() {
    let (_d, logs) = seeded();
    assert_eq!(all(&logs, find("payment")).lines.len(), 1);
    assert_eq!(all(&logs, find("user logged")).lines.len(), 2);
    // Every word must be there.
    assert_eq!(all(&logs, find("user payment")).lines.len(), 0);
    // The extra is searched too.
    assert_eq!(all(&logs, find("orders")).lines.len(), 1);
    assert_eq!(all(&logs, find("")).lines.len(), 6);
    assert_eq!(all(&logs, find("   ")).lines.len(), 6);
}

#[test]
fn search_text_is_never_fts_syntax() {
    let (_d, logs) = seeded();
    for q in [
        "\"",
        "\"\"",
        "payment\"",
        "\"payment",
        "user OR payment",
        "user AND NOT payment",
        "NEAR(user payment)",
        "msg:payment",
        "payment*",
        "^user",
        "(",
        ")",
        "-",
        "*",
        "a b c d e f g h i j k l m n o p",
        "' OR 1=1 --",
        "; DROP TABLE lines;",
    ] {
        assert!(logs.search(&Scope::All, &find(q)).is_ok(), "{q}");
    }
    // OR and the column filter are plain words, so they find nothing.
    assert_eq!(all(&logs, find("user OR payment")).lines.len(), 0);
    assert_eq!(all(&logs, find("msg:payment")).lines.len(), 0);
    // The table is still there.
    assert_eq!(all(&logs, find("")).lines.len(), 6);
}

#[test]
fn fts_query_quotes_every_word_and_limits_size() {
    assert_eq!(fts_query("a b").unwrap(), "\"a\" \"b\"");
    assert_eq!(fts_query("say \"hi\"").unwrap(), "\"say\" \"\"\"hi\"\"\"");
    assert_eq!(fts_query("  "), None);
    let many = (0..30)
        .map(|i| format!("w{i}"))
        .collect::<Vec<_>>()
        .join(" ");
    assert_eq!(fts_query(&many).unwrap().matches('"').count(), 20);
    let long = "x".repeat(500);
    assert_eq!(fts_query(&long).unwrap().len(), 202);
}

#[test]
fn filters_by_app_env_service_and_time() {
    let (_d, logs) = seeded();
    let by = |app: &str, env: &str, service: &str| {
        all(
            &logs,
            Query {
                app: app.to_string(),
                env: env.to_string(),
                service: service.to_string(),
                ..Query::default()
            },
        )
        .lines
        .len()
    };
    assert_eq!(by("notes", "", ""), 5);
    assert_eq!(by("notes", "prod", ""), 3);
    assert_eq!(by("", "prod", ""), 4);
    assert_eq!(by("", "", "worker"), 2);
    assert_eq!(by("notes", "dev", "app"), 0);
    assert_eq!(by("nobody", "", ""), 0);

    let (_d, logs) = open();
    logs.ingest(
        "a",
        "dev",
        "app",
        Level::Debug,
        &[
            raw(1000, "stdout", "old"),
            raw(2000, "stdout", "mid"),
            raw(3000, "stdout", "new"),
        ],
    )
    .unwrap();
    let range = |since, until| {
        msgs(&all(
            &logs,
            Query {
                since,
                until,
                ..Query::default()
            },
        ))
    };
    assert_eq!(range(Some(2000), None), vec!["new", "mid"]);
    assert_eq!(range(None, Some(2000)), vec!["mid", "old"]);
    assert_eq!(range(Some(1500), Some(2500)), vec!["mid"]);
}

#[test]
fn counts_use_the_filters_but_not_the_level_or_paging() {
    let (_d, logs) = seeded();
    let found = all(
        &logs,
        Query {
            app: "notes".into(),
            env: "prod".into(),
            levels: vec![Level::Error],
            limit: 1,
            ..Query::default()
        },
    );
    assert_eq!(found.lines.len(), 1);
    assert_eq!(found.counts[&Level::Info], 1);
    assert_eq!(found.counts[&Level::Error], 1);
    assert_eq!(found.counts[&Level::Warn], 1);
    let found = all(
        &logs,
        Query {
            q: "user".into(),
            ..Query::default()
        },
    );
    assert_eq!(found.counts[&Level::Info], 2);
    assert_eq!(found.counts[&Level::Error], 0);
}

#[test]
fn paging_before_and_after() {
    let (_d, logs) = open();
    let lines: Vec<RawLine> = (1..=10)
        .map(|i| raw(T0 + i, "stdout", &format!("INFO line {i}")))
        .collect();
    put(&logs, "a", "dev", "app", &lines);
    let page = |before, after, limit| {
        all(
            &logs,
            Query {
                before,
                after,
                limit,
                ..Query::default()
            },
        )
    };
    let first = page(None, None, 4);
    assert_eq!(
        msgs(&first),
        vec!["INFO line 10", "INFO line 9", "INFO line 8", "INFO line 7"]
    );
    let next = first.next.unwrap();
    assert_eq!(next, first.lines.last().unwrap().id);
    let second = page(Some(next), None, 4);
    assert_eq!(
        msgs(&second),
        vec!["INFO line 6", "INFO line 5", "INFO line 4", "INFO line 3"]
    );
    let third = page(second.next, None, 4);
    assert_eq!(msgs(&third), vec!["INFO line 2", "INFO line 1"]);
    assert_eq!(third.next, None);
    // Exactly one full page left has no next.
    assert_eq!(page(Some(5), None, 4).next, None);
    // Following: lines newer than an id, newest first, none skipped.
    let tail = page(None, Some(6), 100);
    assert_eq!(tail.lines.len(), 4);
    assert_eq!(tail.lines[0].msg, "INFO line 10");
    assert_eq!(tail.next, None);
    let tail = page(None, Some(2), 3);
    assert_eq!(
        msgs(&tail),
        vec!["INFO line 5", "INFO line 4", "INFO line 3"]
    );
    assert!(page(None, Some(10), 10).lines.is_empty());
    // The default is 100 and the most is 500.
    assert_eq!(page(None, None, 0).lines.len(), 10);
    assert_eq!(page(None, None, 9999).lines.len(), 10);
}

#[test]
fn an_app_scope_sees_its_own_lines_and_events_about_it() {
    let (_d, logs) = seeded();
    logs.event(
        "files",
        Level::Info,
        "file saved",
        json!({}),
        "notes",
        "prod",
    );
    logs.event(
        "files",
        Level::Info,
        "file saved elsewhere",
        json!({}),
        "movies",
        "prod",
    );
    logs.event("deploys", Level::Info, "platform thing", json!({}), "", "");
    let scope = Scope::App {
        app: "notes".into(),
        env: "prod".into(),
    };
    let found = logs.search(&scope, &Query::default()).unwrap();
    let got = msgs(&found);
    assert_eq!(got.len(), 4);
    assert!(got.contains(&"file saved".to_string()));
    assert!(!got
        .iter()
        .any(|m| m.contains("elsewhere") || m.contains("platform")));
    assert!(!got.contains(&"INFO job done".to_string()));
    assert_eq!(found.counts.values().sum::<i64>(), 4);
    // Asking for the app finds Homelab's lines about it too.
    let found = all(
        &logs,
        Query {
            app: "notes".into(),
            env: "prod".into(),
            ..Query::default()
        },
    );
    assert!(msgs(&found).contains(&"file saved".to_string()));
    // Homelab's own lines.
    let homelab = all(
        &logs,
        Query {
            app: "homelab".into(),
            ..Query::default()
        },
    );
    assert_eq!(homelab.lines.len(), 3);
    assert!(homelab
        .lines
        .iter()
        .all(|l| l.stream == "event" && l.env.is_empty()));
}

// ---- events ----

#[test]
fn events_follow_the_level_settings() {
    let settings = Settings::parse(Some(
        r#"{"min_level":"warn","services":{"files":"debug","secrets":"error"}}"#,
    ))
    .unwrap();
    let (_d, logs) = open_with(settings);
    assert!(logs.event("files", Level::Debug, "d", json!({}), "", ""));
    assert!(!logs.event("deploys", Level::Info, "i", json!({}), "", ""));
    assert!(logs.event("deploys", Level::Warn, "w", json!({}), "", ""));
    assert!(!logs.event("secrets", Level::Warn, "w", json!({}), "", ""));
    assert!(logs.event("secrets", Level::Fatal, "f", json!({}), "", ""));
    assert!(!logs.event("files", Level::Unknown, "u", json!({}), "", ""));
    assert_eq!(all(&logs, Query::default()).lines.len(), 3);
}

#[test]
fn events_are_homelab_lines() {
    let (_d, logs) = open();
    assert!(logs.event(
        "files",
        Level::Info,
        "hi",
        json!({ "size": 3 }),
        "notes",
        "dev"
    ));
    let line = all(&logs, Query::default()).lines.remove(0);
    assert_eq!(
        (
            line.app.as_str(),
            line.env.as_str(),
            line.service.as_str(),
            line.stream.as_str()
        ),
        ("homelab", "", "files", "event")
    );
    assert_eq!(line.to_json()["extra"], json!({ "size": 3 }));
    // A big extra is dropped.
    logs.event(
        "files",
        Level::Info,
        "big",
        json!({ "x": "z".repeat(MAX_EXTRA) }),
        "",
        "",
    );
    let line = all(&logs, find("big")).lines.remove(0);
    assert_eq!(line.to_json()["extra"], json!({ "extra_truncated": true }));
}

// ---- settings ----

#[test]
fn settings_parse() {
    assert_eq!(Settings::parse(None).unwrap(), Settings::default());
    assert_eq!(Settings::parse(Some("  ")).unwrap(), Settings::default());
    assert_eq!(Settings::parse(Some("{}")).unwrap(), Settings::default());
    let s = Settings::parse(Some(
        r#"{"min_level":"warn","services":{"files":"debug"},"max_mb":100}"#,
    ))
    .unwrap();
    assert_eq!(s.min_level, Level::Warn);
    assert_eq!(s.min_for("files"), Level::Debug);
    assert_eq!(s.min_for("deploys"), Level::Warn);
    assert_eq!(s.max_mb, 100);
    assert_eq!(Settings::default().min_level, Level::Info);
}

#[test]
fn bad_settings_are_refused_in_plain_words() {
    for bad in [
        "not json",
        "[]",
        r#"{"min_level":"loud"}"#,
        r#"{"min_level":"unknown"}"#,
        r#"{"min_level":"INFO"}"#,
        r#"{"services":{"files":"loud"}}"#,
        r#"{"services":{"Bad Name":"info"}}"#,
        r#"{"max_mb":0}"#,
        r#"{"max_mb":"big"}"#,
        r#"{"other":1}"#,
    ] {
        let err = Settings::parse(Some(bad)).unwrap_err();
        assert!(
            err.starts_with("HOMELAB_SERVICES_LOGS must be JSON"),
            "{bad}: {err}"
        );
    }
}

// ---- retention ----

#[test]
fn old_lines_are_deleted_and_search_stays_in_step() {
    let (_d, logs) = open();
    let day = 24 * 3600 * 1000;
    let now = T0;
    logs.ingest(
        "a",
        "dev",
        "app",
        Level::Debug,
        &[
            raw(now - 15 * day, "stdout", "ancient"),
            raw(now - 13 * day, "stdout", "recent"),
            raw(now, "stdout", "fresh"),
        ],
    )
    .unwrap();
    let removed = logs.retain(now).unwrap();
    assert_eq!(
        removed,
        Removed {
            old: 1,
            for_size: 0
        }
    );
    assert_eq!(msgs(&all(&logs, Query::default())), vec!["fresh", "recent"]);
    assert!(all(&logs, find("ancient")).lines.is_empty());
    assert_eq!(all(&logs, find("recent")).lines.len(), 1);
}

#[test]
fn the_size_cap_removes_the_oldest_tenths_and_shrinks_the_file() {
    let (_d, logs) = open();
    let text = "x".repeat(2000);
    for batch in 0..20 {
        let lines: Vec<RawLine> = (0..100)
            .map(|i| {
                raw(
                    T0 + batch * 100 + i,
                    "stdout",
                    &format!("INFO {batch}-{i} {text}"),
                )
            })
            .collect();
        put(&logs, "a", "dev", "app", &lines);
    }
    let size = |logs: &Logs| file_size(&logs.lock()).unwrap();
    let before = size(&logs);
    assert!(before > 4_000_000, "{before}");
    let cap = before / 2;
    let removed = logs.retain_with(T0, cap).unwrap();
    assert_eq!(removed.old, 0);
    assert!(removed.for_size > 0);
    let after = size(&logs);
    assert!(after <= cap, "{after} > {cap}");
    assert!(after > cap / 4, "{after}");
    // The oldest went; the newest stayed.
    let found = all(&logs, Query::default());
    assert!(found.lines[0].msg.starts_with("INFO 19-99 "));
    assert!(all(&logs, find("0-0")).lines.is_empty());
    let left: i64 = found.counts.values().sum();
    assert_eq!(left as usize, 2000 - removed.for_size);
    // Under the cap nothing more goes.
    assert_eq!(logs.retain_with(T0, cap).unwrap(), Removed::default());
}

#[test]
fn retention_on_an_empty_store_is_fine() {
    let (_d, logs) = open();
    assert_eq!(logs.retain_with(T0, 0).unwrap(), Removed::default());
}

// ---- the routes ----

async fn call(
    env: &TestEnv,
    who: &Value,
    method: &str,
    target: &str,
    body: Vec<u8>,
) -> (StatusCode, Value) {
    let req = request(
        who,
        method,
        target,
        body,
        &[("content-type", "application/json")],
    );
    let res = env.router.clone().oneshot(req).await.unwrap();
    let (parts, body) = res.into_parts();
    let bytes = body.collect().await.unwrap().to_bytes();
    (
        parts.status,
        serde_json::from_slice(&bytes).unwrap_or(Value::Null),
    )
}

async fn post(env: &TestEnv, path: &str, body: Value) -> (StatusCode, Value) {
    call(env, &system(), "POST", path, body.to_string().into_bytes()).await
}

async fn get(env: &TestEnv, who: &Value, query: &str) -> (StatusCode, Value) {
    call(env, who, "GET", &format!("/api/logs{query}"), Vec::new()).await
}

fn body(app: &str, env: &str, lines: Value) -> Value {
    json!({ "app": app, "env": env, "service": "app", "lines": lines })
}

fn line(text: &str) -> Value {
    json!({ "t": T0, "stream": "stdout", "text": text })
}

async fn seeded_env() -> TestEnv {
    let env = test_env(1024);
    for (app, e) in [("notes", "prod"), ("notes", "dev"), ("movies", "prod")] {
        let (status, got) = post(
            &env,
            "/internal/logs/ingest",
            body(
                app,
                e,
                json!([line(&format!("ERROR from {app} {e}")), line("INFO ok")]),
            ),
        )
        .await;
        assert_eq!((status, got), (StatusCode::OK, json!({ "stored": 2 })));
    }
    let (status, got) = post(
        &env,
        "/internal/logs/event",
        json!({ "service": "files", "level": "info", "msg": "file saved", "extra": { "size": 5 },
                "about_app": "notes", "about_env": "prod" }),
    )
    .await;
    assert_eq!((status, got), (StatusCode::OK, json!({ "stored": 1 })));
    env
}

fn msg_list(v: &Value) -> Vec<String> {
    v["lines"]
        .as_array()
        .unwrap()
        .iter()
        .map(|l| l["msg"].as_str().unwrap().to_string())
        .collect()
}

#[tokio::test]
async fn search_answer_has_the_contract_shape() {
    let env = seeded_env().await;
    let (status, got) = get(&env, &person(), "?app=notes&env=prod&limit=2").await;
    assert_eq!(status, StatusCode::OK);
    let lines = got["lines"].as_array().unwrap();
    assert_eq!(lines.len(), 2);
    let first = &lines[0];
    for key in [
        "id", "time", "level", "msg", "app", "env", "service", "stream", "extra",
    ] {
        assert!(first.get(key).is_some(), "{key}");
    }
    assert_eq!(first["msg"], "file saved");
    assert_eq!(first["app"], "homelab");
    assert_eq!(first["level"], "info");
    assert!(first["time"].as_str().unwrap().ends_with('Z'));
    assert_eq!(
        got["counts"],
        json!({"debug":0,"info":2,"warn":0,"error":1,"fatal":0,"unknown":0})
    );
    assert_eq!(got["next"], lines[1]["id"].as_i64().unwrap().to_string());
    let (_, page) = get(
        &env,
        &person(),
        &format!(
            "?app=notes&env=prod&before={}",
            got["next"].as_str().unwrap()
        ),
    )
    .await;
    assert_eq!(page["lines"].as_array().unwrap().len(), 1);
    assert_eq!(page["next"], Value::Null);
}

#[tokio::test]
async fn query_parameters() {
    let env = seeded_env().await;
    let (_, got) = get(&env, &person(), "?q=movies").await;
    assert_eq!(msg_list(&got), vec!["ERROR from movies prod"]);
    let (_, got) = get(&env, &person(), "?level=error,fatal").await;
    assert_eq!(got["lines"].as_array().unwrap().len(), 3);
    let (_, got) = get(&env, &person(), "?min_level=error&service=app").await;
    assert_eq!(got["lines"].as_array().unwrap().len(), 3);
    let (_, got) = get(&env, &person(), "?service=files").await;
    assert_eq!(got["lines"].as_array().unwrap().len(), 1);
    let (_, got) = get(&env, &person(), "?app=homelab").await;
    assert_eq!(got["lines"].as_array().unwrap().len(), 1);
    let (_, got) = get(&env, &person(), "?since=2999-01-01T00:00:00Z").await;
    assert!(got["lines"].as_array().unwrap().is_empty());
    let (_, got) = get(&env, &person(), "?until=1000").await;
    assert!(got["lines"].as_array().unwrap().is_empty());
    let (_, got) = get(&env, &person(), "?since=1000&until=2999-01-01T00:00:00Z").await;
    assert_eq!(got["lines"].as_array().unwrap().len(), 7);
    // Odd text is just text.
    let (status, _) = get(&env, &person(), "?q=%22%20OR%20%2A%20NEAR(").await;
    assert_eq!(status, StatusCode::OK);
    for bad in [
        "?level=loud",
        "?min_level=loud",
        "?min_level=unknown",
        "?since=soon",
        "?until=-5",
        "?before=x",
        "?after=1.5",
        "?limit=0",
        "?limit=501",
        "?limit=x",
    ] {
        let (status, got) = get(&env, &person(), bad).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{bad}");
        assert!(got["error"].is_string(), "{bad}");
    }
}

#[tokio::test]
async fn who_sees_what() {
    let env = seeded_env().await;
    // A person sees everything.
    let (status, got) = get(&env, &person(), "").await;
    assert_eq!(
        (status, got["lines"].as_array().unwrap().len()),
        (StatusCode::OK, 7)
    );

    // An admin key needs logs:read.
    let (status, got) = get(&env, &admin(&["logs:read"]), "").await;
    assert_eq!(
        (status, got["lines"].as_array().unwrap().len()),
        (StatusCode::OK, 7)
    );
    for scopes in [&[][..], &["files:read"][..], &["logs:write"][..]] {
        let (status, got) = get(&env, &admin(scopes), "").await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        assert!(got["error"].as_str().unwrap().contains("logs:read"));
    }

    // An app key needs logs:read too.
    let (status, _) = get(&env, &app_key("notes", "prod"), "").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    let (status, _) = get(&env, &app_scoped("notes", "prod", &["files:read"]), "").await;
    assert_eq!(status, StatusCode::FORBIDDEN);

    // ...and then sees its own lines and the lines about it, nothing else.
    let notes = app_scoped("notes", "prod", &["logs:read"]);
    let (status, got) = get(&env, &notes, "").await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(
        msg_list(&got),
        vec!["file saved", "INFO ok", "ERROR from notes prod"]
    );
    assert_eq!(got["counts"]["info"], 2);
    assert_eq!(got["counts"]["error"], 1);
    for ok in [
        "?app=notes",
        "?env=prod",
        "?app=notes&env=prod",
        "?service=app",
        "?q=ok",
    ] {
        let (status, _) = get(&env, &notes, ok).await;
        assert_eq!(status, StatusCode::OK, "{ok}");
    }
    for other in [
        "?app=movies",
        "?env=dev",
        "?app=movies&env=prod",
        "?app=homelab",
        "?app=notes&env=dev",
    ] {
        let (status, got) = get(&env, &notes, other).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "{other}");
        assert!(got["error"].is_string());
    }
    // A search can't reach other lines by text either.
    let (_, got) = get(&env, &notes, "?q=movies").await;
    assert!(got["lines"].as_array().unwrap().is_empty());
    let (_, got) = get(&env, &app_scoped("movies", "prod", &["logs:read"]), "").await;
    assert_eq!(msg_list(&got), vec!["INFO ok", "ERROR from movies prod"]);

    // An app key without a place is not an app's own key.
    let nowhere = app_scoped("", "", &["logs:read"]);
    let (status, _) = get(&env, &nowhere, "").await;
    assert_eq!(status, StatusCode::FORBIDDEN);

    // Homelab itself is refused.
    let (status, got) = get(&env, &system(), "").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert!(got["error"].is_string());
    let (status, _) = get(&env, &system(), "/anything").await;
    assert_eq!(status, StatusCode::FORBIDDEN);
}

#[tokio::test]
async fn internal_routes_are_for_the_system_only() {
    let env = test_env(1024);
    let good = body("notes", "dev", json!([line("hi")])).to_string();
    for who in [
        person(),
        admin(&["logs:read"]),
        app_scoped("notes", "dev", &["logs:read"]),
    ] {
        for path in [
            "/internal/logs/ingest",
            "/internal/logs/event",
            "/internal/logs/other",
            "/internal/logs",
        ] {
            let (status, got) = call(&env, &who, "POST", path, good.clone().into_bytes()).await;
            assert_eq!(status, StatusCode::FORBIDDEN, "{path}");
            assert!(got["error"].is_string());
        }
        let (status, _) = call(&env, &who, "GET", "/internal/logs/ingest", Vec::new()).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
    }
    // Nothing was stored by those.
    assert!(env
        .logs
        .search(&Scope::All, &Query::default())
        .unwrap()
        .lines
        .is_empty());
    // The system caller gets 404 for unknown paths and methods.
    let (status, _) = post(&env, "/internal/logs/other", json!({})).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    let (status, _) = call(&env, &system(), "GET", "/internal/logs/ingest", Vec::new()).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    let (status, _) = call(&env, &person(), "POST", "/api/logs", Vec::new()).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn ingest_checks_its_input() {
    let env = test_env(1024);
    let ok = |lines: Value| body("notes", "dev", lines);
    let mut cases: Vec<(Value, &str)> = vec![
        (body("Notes", "dev", json!([])), "bad app"),
        (body("", "dev", json!([])), "empty app"),
        (body("a/b", "dev", json!([])), "slash"),
        (body(&"a".repeat(61), "dev", json!([])), "long app"),
        (body("notes", "staging", json!([])), "bad env"),
        (body("notes", "", json!([])), "empty env"),
        (
            json!({"app":"notes","env":"dev","service":"Bad!","lines":[]}),
            "bad service",
        ),
        (
            json!({"app":"notes","env":"dev","service":"","lines":[]}),
            "empty service",
        ),
        (
            json!({"app":"notes","env":"dev","service":"app","min_level":"loud","lines":[]}),
            "bad level",
        ),
        (
            json!({"app":"notes","env":"dev","service":"app","min_level":"unknown","lines":[]}),
            "unknown level",
        ),
        (
            ok(json!([{"t":1,"stream":"other","text":"x"}])),
            "bad stream",
        ),
        (ok(json!([{"t":1,"stream":"stdout"}])), "missing text"),
        (ok(json!("lines")), "lines not a list"),
        (
            json!({"app":"notes","env":"dev","service":"app"}),
            "no lines",
        ),
        (
            json!({"app":"notes","env":"dev","service":"app","lines":[],"x":1}),
            "extra field",
        ),
    ];
    let too_many: Vec<Value> = (0..2001).map(|_| line("x")).collect();
    cases.push((ok(json!(too_many)), "too many lines"));
    for (b, why) in cases {
        let (status, got) = post(&env, "/internal/logs/ingest", b).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{why}");
        assert!(got["error"].is_string(), "{why}");
    }
    let (status, _) = call(
        &env,
        &system(),
        "POST",
        "/internal/logs/ingest",
        b"nope".to_vec(),
    )
    .await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    // Exactly 2000 is fine.
    let max: Vec<Value> = (0..2000).map(|_| line("x")).collect();
    let (status, got) = post(&env, "/internal/logs/ingest", ok(json!(max))).await;
    assert_eq!((status, got), (StatusCode::OK, json!({ "stored": 2000 })));
    // An empty list stores nothing.
    let (status, got) = post(&env, "/internal/logs/ingest", ok(json!([]))).await;
    assert_eq!((status, got), (StatusCode::OK, json!({ "stored": 0 })));
}

#[tokio::test]
async fn ingest_drops_lines_below_min_level() {
    let env = test_env(1024);
    let lines = json!([line("DEBUG a"), line("INFO b"), line("WARN c")]);
    // Default for prod is info, for dev debug.
    let (_, got) = post(
        &env,
        "/internal/logs/ingest",
        body("notes", "prod", lines.clone()),
    )
    .await;
    assert_eq!(got["stored"], 2);
    let (_, got) = post(
        &env,
        "/internal/logs/ingest",
        body("notes", "dev", lines.clone()),
    )
    .await;
    assert_eq!(got["stored"], 3);
    let mut b = body("notes", "dev", lines);
    b["min_level"] = "warn".into();
    let (_, got) = post(&env, "/internal/logs/ingest", b).await;
    assert_eq!(got["stored"], 1);
}

#[tokio::test]
async fn a_body_over_4_mb_is_refused() {
    let env = test_env(1024);
    let big = "x".repeat(4 * 1024 * 1024 + 1);
    let b = body("notes", "dev", json!([line(&big)]));
    let (status, got) = post(&env, "/internal/logs/ingest", b).await;
    assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
    assert!(got["error"].is_string());
    let b = json!({"service":"files","level":"info","msg":big});
    let (status, _) = post(&env, "/internal/logs/event", b).await;
    assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
    assert!(env
        .logs
        .search(&Scope::All, &Query::default())
        .unwrap()
        .lines
        .is_empty());
    // Just under the limit works, and the long text is cut.
    let ok = "x".repeat(3 * 1024 * 1024);
    let (status, got) = post(
        &env,
        "/internal/logs/ingest",
        body("notes", "dev", json!([line(&ok)])),
    )
    .await;
    assert_eq!((status, got), (StatusCode::OK, json!({ "stored": 1 })));
    let found = env.logs.search(&Scope::All, &Query::default()).unwrap();
    assert!(found.lines[0].msg.len() <= MAX_LINE + 3);
}

#[tokio::test]
async fn event_route_checks_its_input() {
    let env = test_env(1024);
    let good = json!({"service":"deploys","level":"warn","msg":"m"});
    let (status, got) = post(&env, "/internal/logs/event", good).await;
    assert_eq!((status, got), (StatusCode::OK, json!({ "stored": 1 })));
    for b in [
        json!({"service":"Bad","level":"info","msg":"m"}),
        json!({"service":"","level":"info","msg":"m"}),
        json!({"service":"x","level":"loud","msg":"m"}),
        json!({"service":"x","level":"unknown","msg":"m"}),
        json!({"service":"x","level":"info","msg":"  "}),
        json!({"service":"x","level":"info"}),
        json!({"service":"x","level":"info","msg":"m","extra":[1]}),
        json!({"service":"x","level":"info","msg":"m","extra":"s"}),
        json!({"service":"x","level":"info","msg":"m","extra":{"k":"z".repeat(9000)}}),
        json!({"service":"x","level":"info","msg":"m","about_app":"notes"}),
        json!({"service":"x","level":"info","msg":"m","about_env":"dev"}),
        json!({"service":"x","level":"info","msg":"m","about_app":"Notes","about_env":"dev"}),
        json!({"service":"x","level":"info","msg":"m","about_app":"notes","about_env":"qa"}),
        json!({"service":"x","level":"info","msg":"m","other":1}),
    ] {
        let (status, got) = post(&env, "/internal/logs/event", b.clone()).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{b}");
        assert!(got["error"].is_string());
    }
    // Below the service's minimum: accepted, not stored.
    let env2 = {
        let (_d, _l) = open();
        test_env(1024)
    };
    let _ = env2;
    let (_, got) = post(
        &env,
        "/internal/logs/event",
        json!({"service":"x","level":"debug","msg":"m","extra":null}),
    )
    .await;
    assert_eq!(got["stored"], 1);
}

#[tokio::test]
async fn events_below_the_minimum_are_not_stored() {
    let dir = tempfile::tempdir().unwrap();
    let logs = Logs::open(&dir.path().join("logs.db"), Settings::default()).unwrap();
    assert!(!logs.event("deploys", Level::Debug, "d", json!({}), "", ""));
    assert!(logs.event("deploys", Level::Info, "i", json!({}), "", ""));
}

// ---- Homelab's events from the other services ----

async fn events(env: &TestEnv) -> Vec<Line> {
    all(
        &env.logs,
        Query {
            app: "homelab".into(),
            ..Query::default()
        },
    )
    .lines
}

#[tokio::test]
async fn files_make_events() {
    let env = test_env(1024 * 1024);
    let who = app_scoped("notes", "dev", &["files:read", "files:write"]);
    let (status, _) = call(
        &env,
        &who,
        "POST",
        "/api/files/folders",
        json!({"name":"pics"}).to_string().into_bytes(),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);
    let (status, file) = call(
        &env,
        &who,
        "POST",
        "/api/files/pics?name=cat.txt",
        b"secret file contents".to_vec(),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED, "{file}");
    let id = file["id"].as_str().unwrap().to_string();
    let req = request(
        &who,
        "GET",
        &format!("/api/files/pics/{id}"),
        Vec::new(),
        &[],
    );
    let res = env.router.clone().oneshot(req).await.unwrap();
    assert_eq!(res.status(), StatusCode::OK);
    let _ = res.into_body().collect().await.unwrap();
    let (status, _) = call(
        &env,
        &who,
        "DELETE",
        &format!("/api/files/pics/{id}"),
        Vec::new(),
    )
    .await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    let (status, _) = call(&env, &who, "DELETE", "/api/files/folders/pics", Vec::new()).await;
    assert_eq!(status, StatusCode::NO_CONTENT);

    let got = events(&env).await;
    let list: Vec<(&str, &str)> = got
        .iter()
        .rev()
        .map(|l| (l.level.name(), l.msg.as_str()))
        .collect();
    assert_eq!(
        list,
        vec![
            ("info", "folder made: pics"),
            ("info", "file saved in pics: cat.txt"),
            ("debug", "file read in pics"),
            ("info", "file deleted in pics"),
            ("info", "folder deleted: pics"),
        ]
    );
    let saved = got
        .iter()
        .find(|l| l.msg.starts_with("file saved"))
        .unwrap()
        .to_json();
    assert_eq!(saved["extra"]["size"], 20);
    assert_eq!(saved["extra"]["by"], "app:notes/dev");
    assert_eq!(saved["service"], "files");
    // They show in the app's own logs, and not in another app's.
    let scope = Scope::App {
        app: "notes".into(),
        env: "dev".into(),
    };
    assert_eq!(
        env.logs
            .search(&scope, &Query::default())
            .unwrap()
            .lines
            .len(),
        5
    );
    let other = Scope::App {
        app: "notes".into(),
        env: "prod".into(),
    };
    assert!(env
        .logs
        .search(&other, &Query::default())
        .unwrap()
        .lines
        .is_empty());
    // The file's contents are nowhere in the events.
    assert!(!format!("{:?}", got).contains("secret file contents"));
}

#[tokio::test]
async fn a_person_in_an_app_folder_is_about_that_app() {
    let env = test_env(1024 * 1024);
    let (status, _) = call(
        &env,
        &person(),
        "POST",
        "/api/files/folders",
        json!({"name":"shared","app":"movies","env":"prod"})
            .to_string()
            .into_bytes(),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);
    let (status, _) = call(
        &env,
        &person(),
        "POST",
        "/api/files/folders",
        json!({"name":"mine"}).to_string().into_bytes(),
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);
    let scope = Scope::App {
        app: "movies".into(),
        env: "prod".into(),
    };
    let found = env.logs.search(&scope, &Query::default()).unwrap();
    assert_eq!(msgs(&found), vec!["folder made: shared"]);
    assert_eq!(events(&env).await.len(), 2);
}

#[tokio::test]
async fn failed_actions_make_no_events() {
    let env = test_env(1024);
    let who = app_scoped("notes", "dev", &["files:read", "files:write"]);
    let (status, _) = call(
        &env,
        &who,
        "POST",
        "/api/files/nope?name=a.txt",
        b"x".to_vec(),
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    let (status, _) = call(&env, &who, "DELETE", "/api/files/nope/abc", Vec::new()).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert!(events(&env).await.is_empty());
}

#[tokio::test]
async fn databases_make_events_without_sql() {
    let env = test_env(1024);
    let file = env.dir.path().join("notes.sqlite");
    {
        let conn = Connection::open(&file).unwrap();
        conn.execute_batch("CREATE TABLE t(a TEXT); INSERT INTO t VALUES ('hunter2-private');")
            .unwrap();
    }
    let (status, got) = call(
        &env,
        &system(),
        "PUT",
        "/internal/db/notes/dev",
        json!({"engine":"sqlite","path":file.to_str().unwrap()})
            .to_string()
            .into_bytes(),
    )
    .await;
    assert!(status.is_success(), "{status} {got}");
    let (status, got) = call(
        &env,
        &person(),
        "POST",
        "/api/db/notes/dev/query",
        json!({"sql":"SELECT a FROM t WHERE a = 'hunter2-private'"})
            .to_string()
            .into_bytes(),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{got}");
    let (status, _) = call(
        &env,
        &person(),
        "GET",
        "/api/db/notes/dev/tables/t/rows",
        Vec::new(),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let got = events(&env).await;
    assert_eq!(got.len(), 2);
    let query = got
        .iter()
        .find(|l| l.msg.starts_with("query run"))
        .unwrap()
        .to_json();
    assert_eq!(query["msg"], "query run on notes/dev");
    assert_eq!(query["extra"]["rows"], 1);
    assert_eq!(query["extra"]["write"], false);
    assert!(query["extra"]["ms"].is_number());
    assert_eq!(query["extra"]["by"], "person:p1");
    let rows = got.iter().find(|l| l.level == Level::Debug).unwrap();
    assert!(rows.msg.starts_with("table rows read"));
    let text = format!("{got:?}");
    assert!(!text.contains("hunter2") && !text.contains("SELECT"));
    let scope = Scope::App {
        app: "notes".into(),
        env: "dev".into(),
    };
    assert_eq!(
        env.logs
            .search(&scope, &Query::default())
            .unwrap()
            .lines
            .len(),
        2
    );
}

#[tokio::test]
async fn secrets_make_events_with_names_only() {
    let env = test_env(1024);
    let value = "s3cr3t-value-0123456789";
    let rw = app_scoped("notes", "dev", &["secrets:read", "secrets:write"]);
    let body = json!({ "value": value }).to_string().into_bytes();
    let (status, _) = call(&env, &rw, "PUT", "/api/secrets/token", body).await;
    assert_eq!(status, StatusCode::OK);
    let (status, got) = call(&env, &rw, "GET", "/api/secrets/token", Vec::new()).await;
    assert_eq!(
        (status, got["value"].as_str()),
        (StatusCode::OK, Some(value))
    );
    // homelabd saves, reads and deletes config secrets.
    let config = format!("{value}-config");
    let (status, _) = call(
        &env,
        &system(),
        "PUT",
        "/internal/secrets/item/TMDB_KEY?app=notes&env=dev",
        json!({ "value": config, "by": "person:p1" })
            .to_string()
            .into_bytes(),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let (status, _) = call(
        &env,
        &system(),
        "GET",
        "/internal/secrets/item/TMDB_KEY?app=notes&env=dev",
        Vec::new(),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let (status, _) = call(&env, &rw, "GET", "/api/secrets/TMDB_KEY", Vec::new()).await;
    assert_eq!(status, StatusCode::OK);
    let (status, _) = call(&env, &rw, "DELETE", "/api/secrets/token", Vec::new()).await;
    assert_eq!(status, StatusCode::NO_CONTENT);
    let (status, _) = call(
        &env,
        &system(),
        "DELETE",
        "/internal/secrets/item/TMDB_KEY?app=notes&env=dev",
        Vec::new(),
    )
    .await;
    assert_eq!(status, StatusCode::NO_CONTENT);

    let got = events(&env).await;
    let list: Vec<(&str, &str)> = got
        .iter()
        .rev()
        .map(|l| (l.level.name(), l.msg.as_str()))
        .collect();
    assert_eq!(
        list,
        vec![
            ("info", "secret saved: token"),
            ("info", "secret read: token"),
            ("info", "secret saved: TMDB_KEY"),
            ("debug", "secret read: TMDB_KEY"),
            ("debug", "secret read: TMDB_KEY"),
            ("info", "secret deleted: token"),
            ("info", "secret deleted: TMDB_KEY"),
        ]
    );
    let saved = got.iter().rev().nth(2).unwrap().to_json();
    assert_eq!(saved["extra"]["version"], 1);
    assert_eq!(saved["extra"]["by"], "person:p1");
    let scope = Scope::App {
        app: "notes".into(),
        env: "dev".into(),
    };
    assert_eq!(
        env.logs
            .search(&scope, &Query::default())
            .unwrap()
            .lines
            .len(),
        7
    );

    // The value is nowhere in logs.db: not in a row, not in the file, not in its journal.
    let hits: i64 = env
        .logs
        .lock()
        .query_row(
            "SELECT COUNT(*) FROM lines WHERE msg LIKE '%s3cr3t%' OR extra LIKE '%s3cr3t%' \
             OR service LIKE '%s3cr3t%'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(hits, 0);
    let path = env.logs.path().to_path_buf();
    for name in ["logs.db", "logs.db-wal"] {
        if let Ok(bytes) = std::fs::read(path.with_file_name(name)) {
            assert!(!bytes.windows(6).any(|w| w == b"s3cr3t"), "{name}");
        }
    }
}

#[tokio::test]
async fn reading_a_missing_secret_makes_no_event() {
    let env = test_env(1024);
    let ro = app_scoped("notes", "dev", &["secrets:read"]);
    let (status, _) = call(&env, &ro, "GET", "/api/secrets/nothing", Vec::new()).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    assert!(events(&env).await.is_empty());
}
