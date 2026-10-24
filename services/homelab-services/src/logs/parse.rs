//! Reading one log line: its level, time, message and extra fields.
//!
//! A line that starts with `{` and is a JSON object is read as JSON. Any other line is plain
//! text: the level is a word near the start (`INFO`, `[error]`, `level=warn`, `ERROR:root:x`).
//! A line with no level is `warn` on stderr and `info` on stdout.

use chrono::DateTime;
use serde_json::{json, Map, Value};

use super::Level;

/// The longest message kept, in bytes. A longer one is cut and gets `…`.
pub const MAX_LINE: usize = 16 * 1024;
/// The biggest `extra`, in bytes of JSON text. A bigger one is dropped.
pub const MAX_EXTRA: usize = 8 * 1024;

/// Keys that hold the level, the message and the time, in the order they are tried.
const LEVEL_KEYS: [&str; 4] = ["level", "lvl", "severity", "levelname"];
const MSG_KEYS: [&str; 3] = ["msg", "message", "event"];
const TIME_KEYS: [&str; 4] = ["time", "timestamp", "ts", "@timestamp"];
/// Homelab sets these. An app can't fake them, so they are dropped.
const DROPPED_KEYS: [&str; 3] = ["app", "env", "service"];

/// What a line says.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Parsed {
    /// Unix milliseconds.
    pub ts: i64,
    pub level: Level,
    pub msg: String,
    /// A JSON object as text, or empty when there is nothing extra.
    pub extra: String,
}

/// Reads one line. `docker_ms` is the time Docker gave it; it is used when the line has none.
pub fn parse_line(text: &str, stream: &str, docker_ms: i64) -> Parsed {
    let text = text.trim_end_matches(['\r', '\n']);
    if text.trim_start().starts_with('{') {
        if let Some(parsed) = parse_json(text, docker_ms) {
            return parsed;
        }
    }
    let level = plain_level(text).unwrap_or(if stream == "stderr" {
        Level::Warn
    } else {
        Level::Info
    });
    Parsed {
        ts: docker_ms,
        level,
        msg: cut(text),
        extra: String::new(),
    }
}

fn parse_json(text: &str, docker_ms: i64) -> Option<Parsed> {
    let Value::Object(mut map) = serde_json::from_str::<Value>(text.trim()).ok()? else {
        return None;
    };
    for key in DROPPED_KEYS {
        map.remove(key);
    }
    let mut level = Level::Unknown;
    for key in LEVEL_KEYS {
        if let Some(v) = map.remove(key) {
            level = json_level(&v);
            break;
        }
    }
    let mut msg = None;
    for key in MSG_KEYS {
        if let Some(v) = map.remove(key) {
            msg = Some(match v {
                Value::String(s) => s,
                other => other.to_string(),
            });
            break;
        }
    }
    let mut ts = docker_ms;
    for key in TIME_KEYS {
        let found = match map.get(key) {
            Some(Value::String(s)) => DateTime::parse_from_rfc3339(s).ok(),
            _ => None,
        };
        if let Some(t) = found {
            ts = t.timestamp_millis();
            map.remove(key);
            break;
        }
    }
    // No message key: the whole line is the message, so nothing is lost.
    let Some(msg) = msg else {
        return Some(Parsed {
            ts,
            level,
            msg: cut(text),
            extra: String::new(),
        });
    };
    Some(Parsed {
        ts,
        level,
        msg: cut(&msg),
        extra: extra_text(&map),
    })
}

/// The `extra` text for the keys left over. Too big means a note that it was dropped.
pub fn extra_text(map: &Map<String, Value>) -> String {
    if map.is_empty() {
        return String::new();
    }
    let text = Value::Object(map.clone()).to_string();
    if text.len() > MAX_EXTRA {
        return json!({ "extra_truncated": true }).to_string();
    }
    text
}

fn json_level(v: &Value) -> Level {
    match v {
        Value::String(s) => word_level(s).unwrap_or(Level::Unknown),
        Value::Number(n) => match n.as_i64() {
            Some(10 | 20) => Level::Debug,
            Some(30) => Level::Info,
            Some(40) => Level::Warn,
            Some(50) => Level::Error,
            Some(60) => Level::Fatal,
            _ => Level::Unknown,
        },
        _ => Level::Unknown,
    }
}

/// The level of a plain line: the first word, or for a line that starts with a digit (a
/// time) the first three words.
fn plain_level(text: &str) -> Option<Level> {
    let first = text.split_whitespace().next()?;
    let count = if first.starts_with(|c: char| c.is_ascii_digit()) {
        3
    } else {
        1
    };
    text.split_whitespace().take(count).find_map(word_level)
}

/// The level a single word names, ignoring case, brackets, `level=` and anything after a `:`.
pub fn word_level(word: &str) -> Option<Level> {
    const EDGE: &[char] = &['[', ']', '(', ')', '{', '}', '<', '>', ',', ';', '"', '\''];
    let lower = word.trim_matches(EDGE).to_ascii_lowercase();
    let w = lower.strip_prefix("level=").unwrap_or(&lower);
    let w = w.trim_matches(EDGE);
    let w = w.split(':').next().unwrap_or("");
    match w {
        "trace" | "debug" | "dbg" => Some(Level::Debug),
        "info" | "inf" | "notice" => Some(Level::Info),
        "warn" | "warning" | "wrn" => Some(Level::Warn),
        "error" | "err" => Some(Level::Error),
        "fatal" | "ftl" | "critical" | "crit" | "panic" => Some(Level::Fatal),
        _ => None,
    }
}

/// Cuts a message at 16 KB (on a character boundary) and adds `…`.
pub fn cut(text: &str) -> String {
    if text.len() <= MAX_LINE {
        return text.to_string();
    }
    let mut end = MAX_LINE;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}…", &text[..end])
}
