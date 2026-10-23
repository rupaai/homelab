//! A small SQL reader for write mode: it splits a string into statements and finds the
//! risky ones. It is not a parser. It only knows quotes, comments and brackets, which is
//! enough to see where a statement starts and whether a DELETE or UPDATE has a WHERE.
//!
//! Quoted runs are '...' (and E'...' with backslash escapes), "..." and $tag$...$tag$.
//! Comments are -- to the end of the line and /* ... */ (which nest, as in Postgres).

/// One piece of a statement.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Tok {
    /// A bare word, in upper case.
    Word(String),
    /// Any quoted run.
    Quoted,
    Open,
    Close,
    /// A `;` outside quotes and comments.
    Semi,
    /// Anything else (numbers, operators).
    Other,
}

/// A token and where it is in the text.
struct Piece {
    tok: Tok,
    start: usize,
    end: usize,
}

fn word_start(c: u8) -> bool {
    c.is_ascii_alphabetic() || c == b'_' || c >= 0x80
}

fn word_char(c: u8) -> bool {
    word_start(c) || c.is_ascii_digit() || c == b'$'
}

/// The end of a quote that opens at `i` (which holds `quote`). A doubled quote stays
/// inside; with `backslash` a backslash escapes the next byte. An open quote runs to the end.
fn quote_end(b: &[u8], i: usize, quote: u8, backslash: bool) -> usize {
    let mut j = i + 1;
    while j < b.len() {
        if backslash && b[j] == b'\\' {
            j += 2;
        } else if b[j] == quote {
            if b.get(j + 1) == Some(&quote) {
                j += 2;
            } else {
                return j + 1;
            }
        } else {
            j += 1;
        }
    }
    b.len()
}

/// If a dollar quote opens at `i`, the end of its whole run.
fn dollar_end(b: &[u8], i: usize) -> Option<usize> {
    let mut j = i + 1;
    while j < b.len() && word_char(b[j]) && b[j] != b'$' {
        j += 1;
    }
    // The tag is empty or a word that doesn't start with a digit.
    if b.get(j) != Some(&b'$') || (j > i + 1 && b[i + 1].is_ascii_digit()) {
        return None;
    }
    let tag = &b[i..=j];
    let mut k = j + 1;
    while k + tag.len() <= b.len() {
        if &b[k..k + tag.len()] == tag {
            return Some(k + tag.len());
        }
        k += 1;
    }
    Some(b.len())
}

fn lex(sql: &str) -> Vec<Piece> {
    let b = sql.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        let start = i;
        let tok = if c.is_ascii_whitespace() {
            i += 1;
            continue;
        } else if c == b'-' && b.get(i + 1) == Some(&b'-') {
            while i < b.len() && b[i] != b'\n' {
                i += 1;
            }
            continue;
        } else if c == b'/' && b.get(i + 1) == Some(&b'*') {
            let mut depth = 1;
            i += 2;
            while i < b.len() && depth > 0 {
                if b[i] == b'/' && b.get(i + 1) == Some(&b'*') {
                    depth += 1;
                    i += 2;
                } else if b[i] == b'*' && b.get(i + 1) == Some(&b'/') {
                    depth -= 1;
                    i += 2;
                } else {
                    i += 1;
                }
            }
            continue;
        } else if c == b'\'' {
            i = quote_end(b, i, b'\'', false);
            Tok::Quoted
        } else if c == b'"' {
            i = quote_end(b, i, b'"', false);
            Tok::Quoted
        } else if c == b'$' && dollar_end(b, i).is_some() {
            i = dollar_end(b, i).unwrap_or(b.len());
            Tok::Quoted
        } else if word_start(c) {
            while i < b.len() && word_char(b[i]) {
                i += 1;
            }
            let word = &sql[start..i];
            // E'...' is a string where backslash escapes.
            if word.eq_ignore_ascii_case("e") && b.get(i) == Some(&b'\'') {
                i = quote_end(b, i, b'\'', true);
                Tok::Quoted
            } else {
                Tok::Word(word.to_ascii_uppercase())
            }
        } else if c == b';' {
            i += 1;
            Tok::Semi
        } else if c == b'(' {
            i += 1;
            Tok::Open
        } else if c == b')' {
            i += 1;
            Tok::Close
        } else {
            i += 1;
            Tok::Other
        };
        out.push(Piece { tok, start, end: i });
    }
    out
}

/// The tokens of each non-empty statement, with their text.
fn statements(sql: &str) -> Vec<(Vec<Tok>, &str)> {
    let mut found = Vec::new();
    let mut toks = Vec::new();
    let mut span: Option<(usize, usize)> = None;
    let mut close = |toks: &mut Vec<Tok>, span: &mut Option<(usize, usize)>| {
        if let Some((s, e)) = span.take() {
            found.push((std::mem::take(toks), &sql[s..e]));
        }
    };
    for p in lex(sql) {
        if p.tok == Tok::Semi {
            close(&mut toks, &mut span);
            continue;
        }
        span = Some((span.map_or(p.start, |s| s.0), p.end));
        toks.push(p.tok);
    }
    close(&mut toks, &mut span);
    found
}

/// The statements of a string, split on `;` outside quotes and comments. Empty ones are
/// left out. Comments before a statement are not part of it.
pub fn split_statements(sql: &str) -> Vec<String> {
    statements(sql)
        .into_iter()
        .map(|(_, text)| text.to_string())
        .collect()
}

/// The first word of a statement, if it starts with one.
fn first_word(toks: &[Tok]) -> &str {
    match toks.first() {
        Some(Tok::Word(w)) => w,
        _ => "",
    }
}

/// Whether the SQL has a transaction statement. Homelab runs a write in one go itself.
pub fn has_transaction_control(sql: &str) -> bool {
    statements(sql).iter().any(|(toks, _)| {
        matches!(
            first_word(toks),
            "BEGIN" | "COMMIT" | "ROLLBACK" | "END" | "START" | "ABORT"
        )
    })
}

/// Whether any statement is hard to undo: DROP, TRUNCATE, ALTER, GRANT, REVOKE, or a
/// DELETE or UPDATE without a WHERE.
pub fn is_risky(sql: &str) -> bool {
    statements(sql).iter().any(|(toks, _)| risky(toks))
}

fn risky(toks: &[Tok]) -> bool {
    match first_word(toks) {
        "DROP" | "TRUNCATE" | "ALTER" | "GRANT" | "REVOKE" => true,
        "DELETE" | "UPDATE" => !has_where(toks, 0),
        // WITH can carry a DELETE or UPDATE in its brackets.
        "WITH" => (0..toks.len()).any(|i| {
            let Tok::Word(w) = &toks[i] else { return false };
            if w != "DELETE" && w != "UPDATE" {
                return false;
            }
            // ON DELETE, FOR UPDATE and DO UPDATE are not statements.
            let before = i.checked_sub(1).map(|j| &toks[j]);
            let skip = matches!(before, Some(Tok::Word(p))
                if matches!(p.as_str(), "ON" | "FOR" | "DO" | "KEY" | "SHARE"));
            !skip && !has_where(toks, i)
        }),
        _ => false,
    }
}

/// Whether a WHERE follows `from`, at the same bracket level, before the statement or
/// that bracket ends.
fn has_where(toks: &[Tok], from: usize) -> bool {
    let mut depth = 0usize;
    for t in &toks[from + 1..] {
        match t {
            Tok::Open => depth += 1,
            Tok::Close if depth == 0 => return false,
            Tok::Close => depth -= 1,
            Tok::Word(w) if depth == 0 && w == "WHERE" => return true,
            _ => {}
        }
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    fn split(sql: &str) -> Vec<String> {
        split_statements(sql)
    }

    #[test]
    fn splits_on_semicolons() {
        assert_eq!(split("select 1; select 2;"), ["select 1", "select 2"]);
        assert_eq!(split(" ;; select 1 ;; "), ["select 1"]);
        assert!(split("").is_empty());
        assert!(split("  ; ; ").is_empty());
    }

    #[test]
    fn quotes_hide_semicolons() {
        assert_eq!(
            split("insert into t values ('a;b', 'it''s; ok'); select \"x;y\" from t"),
            [
                "insert into t values ('a;b', 'it''s; ok')",
                "select \"x;y\" from t"
            ]
        );
        // A doubled double quote stays inside the identifier.
        assert_eq!(split("select \"a\"\";b\"; select 2").len(), 2);
        // An open quote runs to the end.
        assert_eq!(split("select 'a; select 2").len(), 1);
    }

    #[test]
    fn backslash_only_matters_in_e_strings() {
        // In a plain string the backslash is a letter, so the quote after it ends the string.
        assert_eq!(split(r"select 'a\'; select 2").len(), 2);
        // In E'...' it escapes the quote, so the semicolon is inside.
        assert_eq!(split(r"select E'a\'; drop table t'; select 2").len(), 2);
        assert!(is_risky(r"select 'a\'; drop table t; select '"));
    }

    #[test]
    fn comments_hide_semicolons() {
        assert_eq!(
            split("select 1 -- a; b\n; select 2 /* c; d */; /* e */ select 3"),
            ["select 1", "select 2", "select 3"]
        );
        // Block comments nest.
        assert_eq!(split("select 1 /* a /* b; */ c; */; select 2").len(), 2);
        // An unfinished comment runs to the end.
        assert_eq!(split("select 1; /* select 2; select 3").len(), 1);
        // Quotes inside comments mean nothing; semicolons in quotes inside comments neither.
        assert_eq!(split("select 1 -- it's\n; select 2").len(), 2);
        assert_eq!(split("select 1; -- drop table t").len(), 1);
        assert!(!is_risky("select 1; -- drop table t"));
    }

    #[test]
    fn dollar_quotes_are_one_run() {
        assert_eq!(
            split("do $$ begin perform 1; perform 2; end $$; select 1"),
            ["do $$ begin perform 1; perform 2; end $$", "select 1"]
        );
        assert_eq!(
            split("select $tag$ a; $$ b; $tag$; select 2"),
            ["select $tag$ a; $$ b; $tag$", "select 2"]
        );
        // A different tag inside doesn't close it.
        assert_eq!(split("select $a$ x $b$ ; $a$; select 2").len(), 2);
        // $1 is a parameter, and a$b$c is one word.
        assert_eq!(split("select $1; select $2").len(), 2);
        assert_eq!(split("select a$b$c; select 2").len(), 2);
        // Text in a dollar quote is not a statement.
        assert!(!is_risky("select $$ drop table t; $$"));
        assert!(!has_transaction_control("select $$ commit; $$"));
    }

    #[test]
    fn finds_the_risky_kinds() {
        for sql in [
            "drop table t",
            "  DROP TABLE t",
            "/* x */ -- y\n truncate t",
            "alter table t add c int",
            "grant select on t to u",
            "REVOKE ALL ON t FROM u",
            "delete from t",
            "update t set a = 1",
            "select 1; delete from t",
            "select 1; /* ; */ drop table t",
            "delete from t where id in (select 1); update t set a = 1",
            "update t set a = (select max(x) from u where u.id = 1)",
            "delete from t using u",
            "with d as (delete from t returning *) select * from d",
            "with u as (update t set a = 1 returning *) select * from u",
        ] {
            assert!(is_risky(sql), "{sql}");
        }
    }

    #[test]
    fn plain_statements_are_not_risky() {
        for sql in [
            "select 1",
            "insert into t values (1)",
            "create table t(a int)",
            "create index i on t(a)",
            "delete from t where id = 1",
            "DELETE FROM t WHERE true",
            "update t set a = 1 where id = 2",
            "update t set a = 1 -- where\n where id = 2",
            "update t set a = (select 1) where id = 2",
            "delete from t where id in (select id from u where x)",
            "select 'drop table t'",
            "select \"delete\" from t",
            "create table c(id int references t on delete cascade on update cascade)",
            "insert into t values (1) on conflict (id) do update set a = 2",
            "select * from t for update",
            "with x as (select 1) select * from x",
            "with x as (select 1) delete from t where id in (select * from x)",
            "",
        ] {
            assert!(!is_risky(sql), "{sql}");
        }
    }

    #[test]
    fn finds_transaction_statements() {
        for sql in [
            "begin",
            "BEGIN;",
            "commit",
            "rollback",
            "end",
            "start transaction",
            "abort",
            "select 1; commit",
            "/* hi */ begin; insert into t values (1)",
            "insert into t values (1); COMMIT;",
        ] {
            assert!(has_transaction_control(sql), "{sql}");
        }
        for sql in [
            "select 1",
            "select 'commit'",
            "do $$ begin null; end $$",
            "create table commit_log(a int)",
            "insert into t values ('begin; commit')",
            "select 1 -- commit",
            "select \"end\" from t",
        ] {
            assert!(!has_transaction_control(sql), "{sql}");
        }
    }
}
