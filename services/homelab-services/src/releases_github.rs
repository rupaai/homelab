//! The GitHub REST calls that releases need. The token comes from the caller and is never
//! printed or put in an error.

use std::time::Duration;

use reqwest::{Method, StatusCode};
use serde_json::{json, Value};

use crate::releases::Repo;

/// How many items to ask for on each page.
const PER_PAGE: usize = 100;
/// Stop asking for pages after this many, so a broken server cannot keep us here.
const MAX_PAGES: usize = 200;

/// A git tag and the commit it points at.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Tag {
    pub name: String,
    pub sha: String,
}

/// A GitHub release.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Release {
    pub tag: String,
    pub name: String,
    pub prerelease: bool,
    /// The commit the tag points at (empty if unknown).
    pub commit: String,
    pub date: String,
    pub body: String,
    pub url: String,
}

/// A GitHub API client for one token.
pub struct GitHub {
    http: reqwest::Client,
    base: String,
    token: String,
}

impl GitHub {
    /// `base` is the API address, e.g. `https://api.github.com`.
    pub fn new(base: &str, token: &str) -> Result<GitHub, String> {
        let http = reqwest::Client::builder()
            .user_agent(concat!("homelab-services/", env!("CARGO_PKG_VERSION")))
            .timeout(Duration::from_secs(60))
            .build()
            .map_err(|e| format!("cannot start the GitHub client: {e}"))?;
        Ok(GitHub {
            http,
            base: base.trim_end_matches('/').to_string(),
            token: token.to_string(),
        })
    }

    /// The API address in use.
    pub fn base(&self) -> &str {
        &self.base
    }

    /// Sends one request. A 404 gives Ok(None). Other failures say what GitHub said.
    async fn call(
        &self,
        method: Method,
        path: &str,
        query: &[(&str, String)],
        body: Option<Value>,
        accept: &str,
    ) -> Result<Option<(StatusCode, String)>, String> {
        let url = format!("{}{}", self.base, path);
        let mut req = self
            .http
            .request(method.clone(), &url)
            .bearer_auth(&self.token)
            .header("Accept", accept)
            .header("X-GitHub-Api-Version", "2022-11-28")
            .query(query);
        if let Some(body) = body {
            req = req.json(&body);
        }
        // Errors from reqwest can carry the URL but never the token (it is in a header).
        let resp = req
            .send()
            .await
            .map_err(|e| format!("cannot reach GitHub at {}: {}", self.base, e.without_url()))?;
        let status = resp.status();
        let text = resp
            .text()
            .await
            .map_err(|e| format!("cannot read GitHub's answer: {}", e.without_url()))?;
        if status == StatusCode::NOT_FOUND && method == Method::GET {
            return Ok(None);
        }
        if !status.is_success() {
            let message = serde_json::from_str::<Value>(&text)
                .ok()
                .and_then(|v| v.get("message").and_then(Value::as_str).map(String::from))
                .unwrap_or_else(|| text.chars().take(200).collect());
            let hint = if status == StatusCode::UNAUTHORIZED {
                " (check GITHUB_TOKEN)"
            } else {
                ""
            };
            return Err(format!(
                "GitHub {method} {path} gave {}: {message}{hint}",
                status.as_u16()
            ));
        }
        Ok(Some((status, text)))
    }

    async fn get_json(
        &self,
        path: &str,
        query: &[(&str, String)],
    ) -> Result<Option<Value>, String> {
        let Some((_, text)) = self
            .call(
                Method::GET,
                path,
                query,
                None,
                "application/vnd.github+json",
            )
            .await?
        else {
            return Ok(None);
        };
        serde_json::from_str(&text)
            .map(Some)
            .map_err(|_| format!("GitHub {path} gave something that is not JSON"))
    }

    /// Every item of a list call, page after page.
    async fn get_all(&self, path: &str) -> Result<Vec<Value>, String> {
        let mut all = Vec::new();
        for page in 1..=MAX_PAGES {
            let query = [
                ("per_page", PER_PAGE.to_string()),
                ("page", page.to_string()),
            ];
            let Some(Value::Array(items)) = self.get_json(path, &query).await? else {
                return Err(format!("GitHub {path} did not give a list"));
            };
            let count = items.len();
            all.extend(items);
            if count < PER_PAGE {
                break;
            }
        }
        Ok(all)
    }

    /// The full sha of a commit, branch or tag name.
    pub async fn resolve_commit(&self, repo: &Repo, rev: &str) -> Result<String, String> {
        check_ref(rev)?;
        let path = format!("/repos/{repo}/commits/{rev}");
        let found = self.get_json(&path, &[]).await?;
        found
            .as_ref()
            .and_then(|v| v.get("sha"))
            .and_then(Value::as_str)
            .map(String::from)
            .ok_or_else(|| format!("{repo} has no commit {rev}"))
    }

    /// All tags of the repo with the commit each points at.
    pub async fn list_tags(&self, repo: &Repo) -> Result<Vec<Tag>, String> {
        let items = self.get_all(&format!("/repos/{repo}/tags")).await?;
        Ok(items
            .iter()
            .filter_map(|t| {
                Some(Tag {
                    name: t.get("name")?.as_str()?.to_string(),
                    sha: t.get("commit")?.get("sha")?.as_str()?.to_string(),
                })
            })
            .collect())
    }

    /// The commit messages after `base` up to `head`. Long ranges are read page by page.
    pub async fn compare_messages(
        &self,
        repo: &Repo,
        base: &str,
        head: &str,
    ) -> Result<Vec<String>, String> {
        check_ref(base)?;
        check_ref(head)?;
        let path = format!("/repos/{repo}/compare/{base}...{head}");
        let mut messages = Vec::new();
        for page in 1..=MAX_PAGES {
            let query = [
                ("per_page", PER_PAGE.to_string()),
                ("page", page.to_string()),
            ];
            let Some(found) = self.get_json(&path, &query).await? else {
                return Err(format!("{repo} has no way to compare {base} and {head}"));
            };
            let commits = found
                .get("commits")
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default();
            let count = commits.len();
            messages.extend(
                commits
                    .iter()
                    .filter_map(|c| c.get("commit")?.get("message")?.as_str().map(String::from)),
            );
            let total = found
                .get("total_commits")
                .and_then(Value::as_u64)
                .unwrap_or(0) as usize;
            if count < PER_PAGE || messages.len() >= total {
                break;
            }
        }
        Ok(messages)
    }

    /// The text of a file at a commit, or None when the file is not there.
    pub async fn file_at(
        &self,
        repo: &Repo,
        file: &str,
        sha: &str,
    ) -> Result<Option<String>, String> {
        let path = format!("/repos/{repo}/contents/{file}");
        let query = [("ref", sha.to_string())];
        let found = self
            .call(
                Method::GET,
                &path,
                &query,
                None,
                "application/vnd.github.raw+json",
            )
            .await?;
        Ok(found.map(|(_, text)| text))
    }

    /// Makes a release. If the tag is already on this commit, gives the release that exists.
    /// If it is on another commit, that is an error.
    pub async fn create_release(
        &self,
        repo: &Repo,
        tag: &str,
        sha: &str,
        notes: &str,
        prerelease: bool,
    ) -> Result<Release, String> {
        check_ref(tag)?;
        if let Some(on) = self
            .list_tags(repo)
            .await?
            .into_iter()
            .find(|t| t.name == tag)
        {
            if on.sha != sha {
                return Err(format!(
                    "the tag {tag} already exists on another commit ({})",
                    &on.sha[..on.sha.len().min(7)]
                ));
            }
            let path = format!("/repos/{repo}/releases/tags/{tag}");
            if let Some(found) = self.get_json(&path, &[]).await? {
                return Ok(release_from(&found, sha));
            }
        }
        let body = json!({
            "tag_name": tag,
            "target_commitish": sha,
            "name": tag,
            "body": notes,
            "prerelease": prerelease,
        });
        let path = format!("/repos/{repo}/releases");
        let (_, text) = self
            .call(
                Method::POST,
                &path,
                &[],
                Some(body),
                "application/vnd.github+json",
            )
            .await?
            .ok_or_else(|| format!("GitHub could not find {repo}"))?;
        let made: Value = serde_json::from_str(&text)
            .map_err(|_| "GitHub gave something that is not JSON".to_string())?;
        Ok(release_from(&made, sha))
    }

    /// All releases, newest first, with the commit of each tag.
    pub async fn list_releases(&self, repo: &Repo) -> Result<Vec<Release>, String> {
        let items = self.get_all(&format!("/repos/{repo}/releases")).await?;
        let tags = self.list_tags(repo).await?;
        Ok(items
            .iter()
            .map(|item| {
                let tag = item.get("tag_name").and_then(Value::as_str).unwrap_or("");
                let sha = tags
                    .iter()
                    .find(|t| t.name == tag)
                    .map(|t| t.sha.as_str())
                    .unwrap_or("");
                release_from(item, sha)
            })
            .collect())
    }
}

/// Tag and commit names go into URLs: keep to plain characters.
fn check_ref(text: &str) -> Result<(), String> {
    let ok = !text.is_empty()
        && !text.contains("..")
        && text
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_' || b == b'.' || b == b'/');
    if ok {
        Ok(())
    } else {
        Err(format!("\"{text}\" is not a usable commit or tag name"))
    }
}

fn release_from(v: &Value, sha: &str) -> Release {
    let text = |key: &str| v.get(key).and_then(Value::as_str).unwrap_or("").to_string();
    let date = match text("published_at") {
        d if d.is_empty() => text("created_at"),
        d => d,
    };
    Release {
        tag: text("tag_name"),
        name: text("name"),
        prerelease: v
            .get("prerelease")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        commit: sha.to_string(),
        date,
        body: text("body"),
        url: text("html_url"),
    }
}
