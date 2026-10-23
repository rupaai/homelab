//! Release versions: pick the next version of a repo, write the release notes, and make the
//! GitHub release.
//!
//! Nothing here knows about Homelab. It only needs a GitHub repo and a token, so it also works
//! in any repo's CI (see `homelab-services release`). The logic is pure and sits at the top;
//! the GitHub calls are in `releases_github`.

use std::fmt;
use std::io::Write;
use std::process::ExitCode;

use crate::releases_github::{GitHub, Release, Tag};

// ---------------------------------------------------------------------------------------
// Versions

/// The pre-release part of a version.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Pre {
    /// `-dev.N`: a build on the dev channel.
    Dev(u64),
    /// `-pr.N`: a test build of pull request N.
    Pr(u64),
}

/// `vX.Y.Z`, with an optional `-dev.N` or `-pr.N`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Version {
    pub major: u64,
    pub minor: u64,
    pub patch: u64,
    pub pre: Option<Pre>,
}

impl Version {
    /// A release version with no pre-release part.
    pub fn new(major: u64, minor: u64, patch: u64) -> Version {
        Version {
            major,
            minor,
            patch,
            pre: None,
        }
    }

    /// Reads `vX.Y.Z`, `vX.Y.Z-dev.N` or `vX.Y.Z-pr.N`. Anything else is None.
    pub fn parse(text: &str) -> Option<Version> {
        let rest = text.strip_prefix('v')?;
        let (core, pre) = match rest.split_once('-') {
            Some((core, pre)) => (core, Some(pre)),
            None => (rest, None),
        };
        let mut numbers = core.split('.');
        let major = number(numbers.next()?)?;
        let minor = number(numbers.next()?)?;
        let patch = number(numbers.next()?)?;
        if numbers.next().is_some() {
            return None;
        }
        let pre = match pre {
            None => None,
            Some(pre) => {
                let (kind, n) = pre.split_once('.')?;
                let n = number(n)?;
                Some(match kind {
                    "dev" => Pre::Dev(n),
                    "pr" => Pre::Pr(n),
                    _ => return None,
                })
            }
        };
        Some(Version {
            major,
            minor,
            patch,
            pre,
        })
    }

    /// The same version without its pre-release part.
    pub fn release(self) -> Version {
        Version { pre: None, ..self }
    }

    /// True for `vX.Y.Z` with no pre-release part.
    pub fn is_prod(&self) -> bool {
        self.pre.is_none()
    }

    fn triple(&self) -> (u64, u64, u64) {
        (self.major, self.minor, self.patch)
    }
}

/// Digits only, no sign, no empty text.
fn number(text: &str) -> Option<u64> {
    if text.is_empty() || !text.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    text.parse().ok()
}

impl fmt::Display for Version {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "v{}.{}.{}", self.major, self.minor, self.patch)?;
        match self.pre {
            None => Ok(()),
            Some(Pre::Dev(n)) => write!(f, "-dev.{n}"),
            Some(Pre::Pr(n)) => write!(f, "-pr.{n}"),
        }
    }
}

impl PartialOrd for Version {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for Version {
    /// Numbers first. For the same numbers a pre-release is older than the release.
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        use std::cmp::Ordering::*;
        match self.triple().cmp(&other.triple()) {
            Equal => match (self.pre, other.pre) {
                (None, None) => Equal,
                (None, Some(_)) => Greater,
                (Some(_), None) => Less,
                (Some(a), Some(b)) => a.cmp(&b),
            },
            other => other,
        }
    }
}

// ---------------------------------------------------------------------------------------
// Changes

/// How big a change is. The order is the order of the release notes, biggest first.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Kind {
    Breaking,
    New,
    Fix,
}

/// One change that counts for a release.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Change {
    pub kind: Kind,
    /// The title without its `type:` prefix.
    pub text: String,
    pub pr: Option<u64>,
}

/// What a title and body say.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Classified {
    /// None when the change needs no release (docs, chore, ...).
    pub kind: Option<Kind>,
    pub text: String,
}

/// Types that never need a release.
const QUIET_TYPES: [&str; 7] = ["docs", "chore", "test", "ci", "style", "refactor", "build"];

/// Reads a conventional-commit title. A title with no known prefix counts as a Fix, so repos
/// that do not check titles never get stuck.
pub fn classify(title: &str, body: &str) -> Classified {
    let title = title.trim();
    let body_breaks = body.contains("BREAKING CHANGE") || body.contains("BREAKING-CHANGE");
    let Some((ty, bang, rest)) = split_prefix(title) else {
        return Classified {
            kind: Some(if body_breaks {
                Kind::Breaking
            } else {
                Kind::Fix
            }),
            text: title.to_string(),
        };
    };
    let ty = ty.to_ascii_lowercase();
    let known = ty == "feat" || ty == "fix" || QUIET_TYPES.contains(&ty.as_str());
    if !known && !bang && !body_breaks {
        // Not a type we know: keep the whole title.
        return Classified {
            kind: Some(Kind::Fix),
            text: title.to_string(),
        };
    }
    let kind = if bang || body_breaks {
        Some(Kind::Breaking)
    } else if ty == "feat" {
        Some(Kind::New)
    } else if ty == "fix" {
        Some(Kind::Fix)
    } else if known {
        None
    } else {
        Some(Kind::Fix)
    };
    let text = if rest.is_empty() { title } else { rest };
    Classified {
        kind,
        text: text.to_string(),
    }
}

/// Splits `type(scope)!: rest` into (type, has `!`, rest).
fn split_prefix(title: &str) -> Option<(&str, bool, &str)> {
    let colon = title.find(':')?;
    let mut head = &title[..colon];
    let rest = title[colon + 1..].trim();
    let bang = head.ends_with('!');
    if bang {
        head = &head[..head.len() - 1];
    }
    let ty = match head.find('(') {
        Some(i) => {
            if !head.ends_with(')') {
                return None;
            }
            &head[..i]
        }
        None => head,
    };
    if ty.is_empty() || !ty.bytes().all(|b| b.is_ascii_alphabetic()) {
        return None;
    }
    Some((ty, bang, rest))
}

/// A commit message, understood.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedCommit {
    pub pr: Option<u64>,
    pub title: String,
    pub body: String,
}

/// Reads one commit message. Merges that are not a pull request are skipped (None).
pub fn parse_commit(message: &str) -> Option<ParsedCommit> {
    let message = message.trim();
    let (first, rest) = message.split_once('\n').unwrap_or((message, ""));
    let first = first.trim();
    if first.is_empty() {
        return None;
    }
    if let Some(after) = first.strip_prefix("Merge pull request #") {
        let digits: String = after.chars().take_while(|c| c.is_ascii_digit()).collect();
        let pr = digits.parse().ok()?;
        let rest = rest.trim_start();
        let (title, body) = rest.split_once('\n').unwrap_or((rest, ""));
        let title = if title.trim().is_empty() {
            first
        } else {
            title.trim()
        };
        return Some(ParsedCommit {
            pr: Some(pr),
            title: title.to_string(),
            body: body.to_string(),
        });
    }
    if first.starts_with("Merge ") {
        return None;
    }
    let (title, pr) = match squash_number(first) {
        Some((title, pr)) => (title, Some(pr)),
        None => (first, None),
    };
    Some(ParsedCommit {
        pr,
        title: title.to_string(),
        body: rest.to_string(),
    })
}

/// "title (#12)" gives ("title", 12).
fn squash_number(line: &str) -> Option<(&str, u64)> {
    let inner = line.strip_suffix(')')?;
    let at = inner.rfind("(#")?;
    let pr = number(&inner[at + 2..])?;
    Some((inner[..at].trim_end(), pr))
}

/// The changes that count, from commit messages. Each pull request is counted once.
pub fn changes_from_commits<S: AsRef<str>>(messages: &[S]) -> Vec<Change> {
    let mut seen = std::collections::HashSet::new();
    let mut out = Vec::new();
    for message in messages {
        let Some(commit) = parse_commit(message.as_ref()) else {
            continue;
        };
        if let Some(pr) = commit.pr {
            if !seen.insert(pr) {
                continue;
            }
        }
        let class = classify(&commit.title, &commit.body);
        if let Some(kind) = class.kind {
            out.push(Change {
                kind,
                text: class.text,
                pr: commit.pr,
            });
        }
    }
    out
}

/// The biggest change, or None when there are none.
pub fn biggest(changes: &[Change]) -> Option<Kind> {
    changes.iter().map(|c| c.kind).min()
}

/// The next release version after `last`. Before 1.0 a breaking change raises the middle number.
pub fn bump(last: Version, kind: Kind) -> Version {
    let last = last.release();
    match kind {
        Kind::Breaking if last.major == 0 => Version::new(0, last.minor + 1, 0),
        Kind::Breaking => Version::new(last.major + 1, 0, 0),
        Kind::New => Version::new(last.major, last.minor + 1, 0),
        Kind::Fix => Version::new(last.major, last.minor, last.patch + 1),
    }
}

/// Release notes in markdown: Breaking, New, Fixes (only the ones that have lines).
pub fn notes(changes: &[Change]) -> String {
    let mut out = String::new();
    for (kind, heading) in [
        (Kind::Breaking, "Breaking"),
        (Kind::New, "New"),
        (Kind::Fix, "Fixes"),
    ] {
        let lines: Vec<&Change> = changes.iter().filter(|c| c.kind == kind).collect();
        if lines.is_empty() {
            continue;
        }
        if !out.is_empty() {
            out.push('\n');
        }
        out.push_str(&format!("## {heading}\n\n"));
        for change in lines {
            match change.pr {
                Some(pr) => out.push_str(&format!("- {} (#{pr})\n", change.text)),
                None => out.push_str(&format!("- {}\n", change.text)),
            }
        }
    }
    out
}

/// A short line like "1 breaking, 2 new, 1 fix".
pub fn summary(changes: &[Change]) -> String {
    let count = |kind| changes.iter().filter(|c| c.kind == kind).count();
    let mut parts = Vec::new();
    if count(Kind::Breaking) > 0 {
        parts.push(format!("{} breaking", count(Kind::Breaking)));
    }
    if count(Kind::New) > 0 {
        parts.push(format!("{} new", count(Kind::New)));
    }
    match count(Kind::Fix) {
        0 => {}
        1 => parts.push("1 fix".to_string()),
        n => parts.push(format!("{n} fixes")),
    }
    parts.join(", ")
}

/// The same line as `summary`, read back from release notes written by `notes`.
pub fn summary_of_notes(text: &str) -> String {
    let mut kind = None;
    let mut changes = Vec::new();
    for line in text.lines() {
        let line = line.trim();
        if let Some(heading) = line.strip_prefix("## ") {
            kind = match heading.trim() {
                "Breaking" => Some(Kind::Breaking),
                "New" => Some(Kind::New),
                "Fixes" => Some(Kind::Fix),
                _ => None,
            };
        } else if let (Some(kind), true) = (kind, line.starts_with("- ")) {
            changes.push(Change {
                kind,
                text: line[2..].to_string(),
                pr: None,
            });
        }
    }
    summary(&changes)
}

// ---------------------------------------------------------------------------------------
// Start version

/// Files that can hold a start version, in the order we look.
pub const MANIFESTS: [&str; 3] = ["pyproject.toml", "package.json", "Cargo.toml"];

/// Reads the version in a manifest file's text. None when it has none we can use.
pub fn manifest_version(file: &str, text: &str) -> Option<Version> {
    let raw = match file {
        "pyproject.toml" => toml_version(text, "project")?,
        "Cargo.toml" => toml_version(text, "package")?,
        "package.json" => {
            let json: serde_json::Value = serde_json::from_str(text).ok()?;
            json.get("version")?.as_str()?.to_string()
        }
        _ => return None,
    };
    loose_version(&raw)
}

/// `version = "x"` inside the `[section]` table.
fn toml_version(text: &str, section: &str) -> Option<String> {
    let mut inside = false;
    for line in text.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            inside = line.trim_end() == format!("[{section}]");
            continue;
        }
        if !inside {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        if key.trim() != "version" {
            continue;
        }
        let value = value.trim();
        let quote = value.chars().next().filter(|c| *c == '"' || *c == '\'')?;
        let inner = &value[1..];
        return Some(inner[..inner.find(quote)?].to_string());
    }
    None
}

/// "1.2.3", "v1.2", "1.2.3-beta.1" give a release version (missing numbers are 0).
fn loose_version(raw: &str) -> Option<Version> {
    let raw = raw.trim().trim_start_matches('v');
    let core = raw.split(['-', '+']).next()?;
    let parts: Vec<&str> = core.split('.').collect();
    if parts.is_empty() || parts.len() > 3 {
        return None;
    }
    let mut numbers = [0u64; 3];
    for (i, part) in parts.iter().enumerate() {
        numbers[i] = number(part)?;
    }
    Some(Version::new(numbers[0], numbers[1], numbers[2]))
}

// ---------------------------------------------------------------------------------------
// Plans

/// owner/name of a GitHub repo.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Repo {
    pub owner: String,
    pub name: String,
}

impl Repo {
    pub fn parse(text: &str) -> Result<Repo, String> {
        let ok = |s: &str| {
            !s.is_empty()
                && s.bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_' || b == b'.')
        };
        match text.split_once('/') {
            Some((owner, name)) if ok(owner) && ok(name) => Ok(Repo {
                owner: owner.to_string(),
                name: name.to_string(),
            }),
            _ => Err(format!(
                "the repo must look like owner/name, not \"{text}\""
            )),
        }
    }
}

impl fmt::Display for Repo {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}/{}", self.owner, self.name)
    }
}

/// Which kind of version to make.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Channel {
    /// `vX.Y.Z`
    Prod,
    /// `vX.Y.Z-dev.N`
    Dev,
    /// `vX.Y.Z-pr.N`, for pull request N with this title.
    Test { pr: u64, title: String },
}

/// Settings that are the same for every channel.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// The start version when the repo has no release yet.
    pub first: Option<Version>,
    /// Release even when no change counts (this bumps the last number).
    pub always: bool,
}

/// What a release would be.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Plan {
    pub version: Version,
    /// The latest prod release before this one. None for the first release.
    pub previous: Option<Version>,
    pub notes: String,
    pub summary: String,
    pub changes: Vec<Change>,
    /// False when nothing counts and `always` is off.
    pub release: bool,
    /// True when this commit already has this version, so nothing new will be made.
    pub existing: bool,
    /// The full commit sha.
    pub commit: String,
}

/// The highest prod version among the tags.
fn latest_prod(tags: &[Tag]) -> Option<(Version, &Tag)> {
    tags.iter()
        .filter_map(|t| Version::parse(&t.name).map(|v| (v, t)))
        .filter(|(v, _)| v.is_prod())
        .max_by_key(|(v, _)| *v)
}

/// Works out the next version and its notes. Creates nothing.
pub async fn plan(
    gh: &GitHub,
    repo: &Repo,
    commit: &str,
    channel: &Channel,
    options: &Options,
) -> Result<Plan, String> {
    let sha = gh.resolve_commit(repo, commit).await?;
    let tags = gh.list_tags(repo).await?;
    let latest = latest_prod(&tags);
    let previous = latest.as_ref().map(|(v, _)| *v);

    let mut changes = Vec::new();
    if let Some((version, tag)) = &latest {
        if tag.sha == sha && *channel == Channel::Prod {
            // This commit already is the latest release: nothing new to make.
            return Ok(finish(*version, previous, Vec::new(), true, true, sha));
        }
        let messages = gh.compare_messages(repo, &tag.name, &sha).await?;
        changes = changes_from_commits(&messages);
    }
    if let Channel::Test { pr, title } = channel {
        if !changes.iter().any(|c| c.pr == Some(*pr)) {
            let class = classify(title, "");
            if let Some(kind) = class.kind {
                changes.push(Change {
                    kind,
                    text: class.text,
                    pr: Some(*pr),
                });
            }
        }
    }

    let (base, release) = match latest {
        None => (start_version(gh, repo, &sha, options).await?, true),
        Some((last, _)) => match biggest(&changes) {
            Some(kind) => (bump(last, kind), true),
            None if options.always => (bump(last, Kind::Fix), true),
            None => (last, false),
        },
    };
    if !release {
        return Ok(finish(base, previous, changes, false, false, sha));
    }

    let version = match channel {
        Channel::Prod => base,
        Channel::Test { pr, .. } => Version {
            pre: Some(Pre::Pr(*pr)),
            ..base
        },
        Channel::Dev => {
            let mut highest = 0;
            for tag in &tags {
                let Some(v) = Version::parse(&tag.name) else {
                    continue;
                };
                if let (true, Some(Pre::Dev(n))) = (v.triple() == base.triple(), v.pre) {
                    if tag.sha == sha {
                        // This commit already has a dev version: reuse it.
                        return Ok(finish(v, previous, changes, true, true, sha));
                    }
                    highest = highest.max(n);
                }
            }
            Version {
                pre: Some(Pre::Dev(highest + 1)),
                ..base
            }
        }
    };
    let name = version.to_string();
    let existing = match tags.iter().find(|t| t.name == name) {
        Some(tag) if tag.sha == sha => true,
        Some(tag) => {
            return Err(format!(
                "the tag {name} already exists on another commit ({})",
                short(&tag.sha)
            ))
        }
        None => false,
    };
    Ok(finish(version, previous, changes, true, existing, sha))
}

fn finish(
    version: Version,
    previous: Option<Version>,
    changes: Vec<Change>,
    release: bool,
    existing: bool,
    commit: String,
) -> Plan {
    Plan {
        version,
        previous,
        notes: notes(&changes),
        summary: summary(&changes),
        changes,
        release,
        existing,
        commit,
    }
}

fn short(sha: &str) -> &str {
    &sha[..sha.len().min(7)]
}

/// The first prod version: `--first`, else a manifest in the repo, else 0.1.0.
async fn start_version(
    gh: &GitHub,
    repo: &Repo,
    sha: &str,
    options: &Options,
) -> Result<Version, String> {
    if let Some(first) = options.first {
        return Ok(first.release());
    }
    for file in MANIFESTS {
        if let Some(text) = gh.file_at(repo, file, sha).await? {
            if let Some(version) = manifest_version(file, &text) {
                return Ok(version);
            }
        }
    }
    Ok(Version::new(0, 1, 0))
}

/// A plan and what GitHub made for it.
#[derive(Debug, Clone)]
pub struct Published {
    pub plan: Plan,
    /// None when there was nothing to release.
    pub release: Option<Release>,
}

/// Plans, then makes the GitHub release (a pre-release for dev and test).
pub async fn publish(
    gh: &GitHub,
    repo: &Repo,
    commit: &str,
    channel: &Channel,
    options: &Options,
) -> Result<Published, String> {
    let planned = plan(gh, repo, commit, channel, options).await?;
    create(gh, repo, channel, planned).await
}

/// Makes the GitHub release for a plan you already have.
pub async fn create(
    gh: &GitHub,
    repo: &Repo,
    channel: &Channel,
    plan: Plan,
) -> Result<Published, String> {
    if !plan.release {
        return Ok(Published {
            plan,
            release: None,
        });
    }
    let prerelease = *channel != Channel::Prod;
    let release = gh
        .create_release(
            repo,
            &plan.version.to_string(),
            &plan.commit,
            &plan.notes,
            prerelease,
        )
        .await?;
    Ok(Published {
        plan,
        release: Some(release),
    })
}

// ---------------------------------------------------------------------------------------
// The command

/// Help for the release command.
pub const RELEASE_USAGE: &str =
    "usage: homelab-services release <prod|dev|test> --repo owner/name --commit <sha>\n\
                [--pr N --title \"...\"] [--first vX.Y.Z] [--always] [--dry-run]\n\
\n\
Picks the next version, writes the notes and makes the GitHub release.\n\
  GITHUB_TOKEN    a token that can write releases (required)\n\
  GITHUB_API_URL  the API address (optional, default https://api.github.com)\n\
\n\
Prints the version on stdout and the notes on stderr. With --dry-run nothing is created.\n\
Prints nothing (and exits 0) when there is nothing to release.";

/// The settings of one `release` command.
#[derive(Debug, Clone)]
pub struct Cli {
    pub channel: Channel,
    pub repo: Repo,
    pub commit: String,
    pub options: Options,
    pub dry_run: bool,
}

/// Reads the arguments after `release`.
pub fn parse_args(args: &[String]) -> Result<Cli, String> {
    let (channel, flags) = args
        .split_first()
        .ok_or("say which channel: prod, dev or test")?;
    let mut repo = None;
    let mut commit = None;
    let mut pr = None;
    let mut title = None;
    let mut options = Options::default();
    let mut dry_run = false;
    let mut flags = flags.iter();
    while let Some(flag) = flags.next() {
        let mut value = |name: &str| {
            flags
                .next()
                .cloned()
                .ok_or_else(|| format!("{name} needs a value"))
        };
        match flag.as_str() {
            "--repo" => repo = Some(Repo::parse(&value("--repo")?)?),
            "--commit" => commit = Some(value("--commit")?),
            "--pr" => {
                let text = value("--pr")?;
                pr = Some(
                    number(&text)
                        .ok_or_else(|| format!("--pr must be a number, not \"{text}\""))?,
                );
            }
            "--title" => title = Some(value("--title")?),
            "--first" => {
                let text = value("--first")?;
                let version = Version::parse(&text)
                    .filter(Version::is_prod)
                    .ok_or_else(|| format!("--first must look like v1.2.3, not \"{text}\""))?;
                options.first = Some(version);
            }
            "--always" => options.always = true,
            "--dry-run" => dry_run = true,
            other => return Err(format!("unknown option {other}")),
        }
    }
    let channel = match channel.as_str() {
        "prod" => Channel::Prod,
        "dev" => Channel::Dev,
        "test" => Channel::Test {
            pr: pr.ok_or("the test channel needs --pr")?,
            title: title.ok_or("the test channel needs --title")?,
        },
        other => {
            return Err(format!(
                "unknown channel \"{other}\": use prod, dev or test"
            ))
        }
    };
    Ok(Cli {
        channel,
        repo: repo.ok_or("--repo is needed")?,
        commit: commit.ok_or("--commit is needed")?,
        options,
        dry_run,
    })
}

/// Does the command. Writes the version to `out` and the notes and messages to `err`.
pub async fn run(
    cli: &Cli,
    gh: &GitHub,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<(), String> {
    let write_failed = |e: std::io::Error| format!("cannot write the result: {e}");
    let planned = plan(gh, &cli.repo, &cli.commit, &cli.channel, &cli.options).await?;
    if !planned.release {
        let after = planned
            .previous
            .map(|v| format!(" since {v}"))
            .unwrap_or_default();
        writeln!(
            err,
            "Nothing to release{after}: no change needs a new version."
        )
        .map_err(write_failed)?;
        return Ok(());
    }
    if planned.existing {
        writeln!(err, "{} is already made for this commit.", planned.version)
            .map_err(write_failed)?;
    }
    if !planned.notes.is_empty() {
        write!(err, "{}", planned.notes).map_err(write_failed)?;
    }
    let version = planned.version;
    if cli.dry_run {
        writeln!(err, "Dry run: nothing was created.").map_err(write_failed)?;
    } else {
        create(gh, &cli.repo, &cli.channel, planned).await?;
    }
    writeln!(out, "{version}").map_err(write_failed)?;
    Ok(())
}

/// `homelab-services release ...`: reads the environment, does the command, gives the exit code.
pub fn command(args: &[String]) -> ExitCode {
    let cli = match parse_args(args) {
        Ok(cli) => cli,
        Err(message) => {
            eprintln!("homelab-services: {message}\n\n{RELEASE_USAGE}");
            return ExitCode::from(2);
        }
    };
    let token = std::env::var("GITHUB_TOKEN").unwrap_or_default();
    if token.trim().is_empty() {
        eprintln!("homelab-services: GITHUB_TOKEN is not set");
        return ExitCode::FAILURE;
    }
    let base = std::env::var("GITHUB_API_URL")
        .ok()
        .filter(|v| !v.trim().is_empty())
        .unwrap_or_else(|| "https://api.github.com".to_string());
    let result = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| format!("cannot start: {e}"))
        .and_then(|rt| {
            rt.block_on(async {
                let gh = GitHub::new(&base, token.trim())?;
                run(&cli, &gh, &mut std::io::stdout(), &mut std::io::stderr()).await
            })
        });
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(message) => {
            eprintln!("homelab-services: {message}");
            ExitCode::FAILURE
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v(text: &str) -> Version {
        Version::parse(text).unwrap()
    }

    fn change(kind: Kind, text: &str, pr: Option<u64>) -> Change {
        Change {
            kind,
            text: text.to_string(),
            pr,
        }
    }

    #[test]
    fn versions_parse_and_print() {
        for text in ["v0.1.0", "v12.3.45", "v0.4.0-dev.7", "v1.0.0-pr.27"] {
            assert_eq!(v(text).to_string(), text);
        }
        for bad in [
            "0.1.0",
            "v1.2",
            "v1.2.3.4",
            "v1.2.x",
            "v1.2.3-rc.1",
            "v1.2.3-dev",
            "v1.2.3-dev.x",
            "v-1.2.3",
            "",
        ] {
            assert_eq!(Version::parse(bad), None, "{bad}");
        }
    }

    #[test]
    fn versions_compare_by_number() {
        assert!(v("v0.10.0") > v("v0.9.9"));
        assert!(v("v1.0.0") > v("v0.99.99"));
        assert!(v("v0.4.0") > v("v0.4.0-dev.3"));
        assert!(v("v0.4.0-dev.2") > v("v0.4.0-dev.1"));
        assert!(v("v0.4.0-dev.1") > v("v0.3.9"));
    }

    #[test]
    fn titles_are_classified() {
        let kind = |t: &str| classify(t, "").kind;
        assert_eq!(kind("feat: add x"), Some(Kind::New));
        assert_eq!(kind("fix: mend x"), Some(Kind::Fix));
        assert_eq!(kind("feat!: drop x"), Some(Kind::Breaking));
        assert_eq!(kind("fix!: drop x"), Some(Kind::Breaking));
        assert_eq!(kind("docs!: drop x"), Some(Kind::Breaking));
        assert_eq!(kind("wip!: drop x"), Some(Kind::Breaking));
        assert_eq!(kind("feat(api)!: drop x"), Some(Kind::Breaking));
        assert_eq!(kind("feat(api): add x"), Some(Kind::New));
        assert_eq!(kind("FEAT: add x"), Some(Kind::New));
        assert_eq!(kind("Fix(UI): mend x"), Some(Kind::Fix));
        for quiet in ["docs", "chore", "test", "ci", "style", "refactor", "build"] {
            assert_eq!(kind(&format!("{quiet}: x")), None, "{quiet}");
            assert_eq!(kind(&format!("{quiet}(scope): x")), None, "{quiet}");
        }
        // No known prefix counts as a fix.
        assert_eq!(kind("Update the readme"), Some(Kind::Fix));
        assert_eq!(kind("wip: something"), Some(Kind::Fix));
        assert_eq!(kind("Note: this is odd"), Some(Kind::Fix));
    }

    #[test]
    fn a_breaking_change_in_the_body_is_breaking() {
        let c = classify(
            "feat: new api",
            "Details\n\nBREAKING CHANGE: old api is gone",
        );
        assert_eq!(c.kind, Some(Kind::Breaking));
        assert_eq!(
            classify("chore: x", "BREAKING CHANGE: y").kind,
            Some(Kind::Breaking)
        );
        assert_eq!(
            classify("feat: new api", "no such thing").kind,
            Some(Kind::New)
        );
    }

    #[test]
    fn the_prefix_is_removed_from_the_text() {
        assert_eq!(classify("feat(api): add x", "").text, "add x");
        assert_eq!(classify("feat!: drop x", "").text, "drop x");
        assert_eq!(classify("Update the readme", "").text, "Update the readme");
        assert_eq!(classify("wip: something", "").text, "wip: something");
    }

    #[test]
    fn bump_follows_the_biggest_change() {
        let last = v("v1.2.3");
        assert_eq!(bump(last, Kind::Fix), v("v1.2.4"));
        assert_eq!(bump(last, Kind::New), v("v1.3.0"));
        assert_eq!(bump(last, Kind::Breaking), v("v2.0.0"));
    }

    #[test]
    fn breaking_raises_the_middle_number_before_one_point_zero() {
        assert_eq!(bump(v("v0.19.4"), Kind::Breaking), v("v0.20.0"));
        assert_eq!(bump(v("v0.19.4"), Kind::New), v("v0.20.0"));
        assert_eq!(bump(v("v0.19.4"), Kind::Fix), v("v0.19.5"));
    }

    #[test]
    fn biggest_picks_the_highest() {
        let list = [
            change(Kind::Fix, "a", None),
            change(Kind::Breaking, "b", None),
            change(Kind::New, "c", None),
        ];
        assert_eq!(biggest(&list), Some(Kind::Breaking));
        assert_eq!(biggest(&list[..1]), Some(Kind::Fix));
        assert_eq!(biggest(&[]), None);
    }

    #[test]
    fn notes_have_only_the_sections_with_lines() {
        let list = [
            change(Kind::Fix, "mend x", Some(3)),
            change(Kind::New, "add y", Some(2)),
            change(Kind::Breaking, "drop z", Some(4)),
            change(Kind::New, "add w", None),
        ];
        assert_eq!(
            notes(&list),
            "## Breaking\n\n- drop z (#4)\n\n## New\n\n- add y (#2)\n- add w\n\n## Fixes\n\n- mend x (#3)\n"
        );
        assert_eq!(notes(&list[..1]), "## Fixes\n\n- mend x (#3)\n");
        assert_eq!(notes(&[]), "");
    }

    #[test]
    fn summary_counts() {
        let list = [
            change(Kind::New, "a", None),
            change(Kind::New, "b", None),
            change(Kind::Fix, "c", None),
        ];
        assert_eq!(summary(&list), "2 new, 1 fix");
        let list = [
            change(Kind::Breaking, "a", None),
            change(Kind::Fix, "b", None),
            change(Kind::Fix, "c", None),
        ];
        assert_eq!(summary(&list), "1 breaking, 2 fixes");
        assert_eq!(summary(&[]), "");
    }

    #[test]
    fn commit_messages_are_read() {
        let merge =
            parse_commit("Merge pull request #12 from rupaai/branch\n\nfeat: add x\n\nmore")
                .unwrap();
        assert_eq!(merge.pr, Some(12));
        assert_eq!(merge.title, "feat: add x");
        assert_eq!(merge.body.trim(), "more");

        let squash = parse_commit("fix: mend y (#7)\n\n* detail").unwrap();
        assert_eq!(squash.pr, Some(7));
        assert_eq!(squash.title, "fix: mend y");

        let plain = parse_commit("Tidy things\n\nbody").unwrap();
        assert_eq!(plain.pr, None);
        assert_eq!(plain.title, "Tidy things");

        assert_eq!(parse_commit("Merge branch 'main' into feature"), None);
        assert_eq!(
            parse_commit("Merge remote-tracking branch 'origin/main'"),
            None
        );
        assert_eq!(parse_commit("  "), None);
    }

    #[test]
    fn changes_are_deduplicated_by_pr_and_quiet_ones_dropped() {
        let messages = [
            "Merge pull request #5 from a/b\n\nfeat: add x",
            "feat: add x (#5)",
            "fix: mend y (#6)",
            "docs: words (#7)",
            "Merge branch 'main' into b",
            "Odd commit",
        ];
        let list = changes_from_commits(&messages);
        assert_eq!(
            list,
            vec![
                change(Kind::New, "add x", Some(5)),
                change(Kind::Fix, "mend y", Some(6)),
                change(Kind::Fix, "Odd commit", None),
            ]
        );
    }

    #[test]
    fn start_version_comes_from_each_manifest() {
        let py =
            "[build-system]\nversion = \"9.9.9\"\n\n[project]\nname = \"x\"\nversion = \"0.3.0\"\n";
        assert_eq!(manifest_version("pyproject.toml", py), Some(v("v0.3.0")));
        let js = "{\"name\":\"x\",\"version\":\"1.4.2\"}";
        assert_eq!(manifest_version("package.json", js), Some(v("v1.4.2")));
        let rs = "[package]\nname = \"x\"\nversion = '2.1.0' # comment\n\n[dependencies]\nversion = \"7.0.0\"\n";
        assert_eq!(manifest_version("Cargo.toml", rs), Some(v("v2.1.0")));
        assert_eq!(
            manifest_version("Cargo.toml", "[package]\nversion = \"1.2.3-beta.1\"\n"),
            Some(v("v1.2.3"))
        );
        assert_eq!(
            manifest_version("Cargo.toml", "[package]\nversion = \"1.2\"\n"),
            Some(v("v1.2.0"))
        );
    }

    #[test]
    fn manifests_without_a_usable_version_give_none() {
        assert_eq!(
            manifest_version("pyproject.toml", "[project]\ndynamic = [\"version\"]\n"),
            None
        );
        assert_eq!(
            manifest_version("Cargo.toml", "[package]\nversion.workspace = true\n"),
            None
        );
        assert_eq!(manifest_version("package.json", "{\"name\":\"x\"}"), None);
        assert_eq!(manifest_version("package.json", "not json"), None);
        assert_eq!(
            manifest_version("Cargo.toml", "[package]\nversion = \"abc\"\n"),
            None
        );
        assert_eq!(manifest_version("README.md", "version = \"1.0.0\""), None);
    }

    #[test]
    fn repo_names_are_checked() {
        assert_eq!(
            Repo::parse("rupaai/homelab").unwrap().to_string(),
            "rupaai/homelab"
        );
        for bad in ["homelab", "a/b/c", "/b", "a/", "a b/c", "a/../b?x"] {
            assert!(Repo::parse(bad).is_err(), "{bad}");
        }
    }

    fn args(text: &str) -> Vec<String> {
        text.split_whitespace().map(String::from).collect()
    }

    #[test]
    fn arguments_are_read() {
        let cli = parse_args(&args(
            "prod --repo a/b --commit abc --first v0.2.0 --always --dry-run",
        ))
        .unwrap();
        assert_eq!(cli.channel, Channel::Prod);
        assert_eq!(cli.repo.to_string(), "a/b");
        assert_eq!(cli.commit, "abc");
        assert_eq!(cli.options.first, Some(v("v0.2.0")));
        assert!(cli.options.always && cli.dry_run);

        let mut test = args("test --repo a/b --commit abc --pr 27 --title");
        test.push("feat: add x".to_string());
        let cli = parse_args(&test).unwrap();
        assert_eq!(
            cli.channel,
            Channel::Test {
                pr: 27,
                title: "feat: add x".to_string()
            }
        );
    }

    #[test]
    fn bad_arguments_say_what_is_wrong() {
        for (line, word) in [
            ("", "channel"),
            ("weekly --repo a/b --commit c", "unknown channel"),
            ("prod --commit c", "--repo"),
            ("prod --repo a/b", "--commit"),
            ("prod --repo a/b --commit c --first 1.0", "--first"),
            ("prod --repo a/b --commit c --first v1.0.0-dev.1", "--first"),
            ("prod --repo a/b --commit c --bogus", "unknown option"),
            ("prod --repo", "needs a value"),
            ("test --repo a/b --commit c --title x", "--pr"),
            ("test --repo a/b --commit c --pr 1", "--title"),
            ("test --repo a/b --commit c --pr x --title t", "number"),
        ] {
            let err = parse_args(&args(line)).unwrap_err();
            assert!(err.contains(word), "{line}: {err}");
        }
    }
}
