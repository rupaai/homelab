use std::sync::Arc;

use reqwest::Method;
use serde::Deserialize;
use serde_json::json;

use crate::client::Inner;
use crate::error::{Error, Result};
use crate::types::*;

/// Env var holding the app's name (set in every Homelab app).
pub const ENV_APP: &str = "HOMELAB_APP";
/// Env var holding the app's environment, `dev` or `prod` (set in every Homelab app).
pub const ENV_ENV: &str = "HOMELAB_ENV";

/// The app and environment: as given, or from `HOMELAB_APP` and `HOMELAB_ENV`.
fn place(app: Option<&str>, env: Option<&str>) -> Result<(String, String)> {
    let pick = |given: Option<&str>, var: &str, what: &str| -> Result<String> {
        match given {
            Some(v) => Ok(v.to_string()),
            None => std::env::var(var)
                .ok()
                .filter(|v| !v.is_empty())
                .ok_or_else(|| Error::config(format!("Set {var} or pass {what}"))),
        }
    };
    Ok((pick(app, ENV_APP, "app")?, pick(env, ENV_ENV, "env")?))
}

/// The Databases service. Get one with [`Homelab::db`](crate::Homelab::db).
///
/// Apps connect with `DATABASE_URL` and any normal driver; this is for looking inside
/// (databases, tables, rows and read-only SQL). It never writes. Wherever a call takes
/// `app` and `env`, `None` means `HOMELAB_APP` / `HOMELAB_ENV`.
#[derive(Clone)]
pub struct Db {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for Db {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Db").finish_non_exhaustive()
    }
}

#[derive(Deserialize)]
struct DbList {
    #[serde(default)]
    databases: Vec<DbInfo>,
}

#[derive(Deserialize)]
struct TableList {
    #[serde(default)]
    tables: Vec<DbTable>,
}

impl Db {
    pub(crate) fn new(inner: Arc<Inner>) -> Self {
        Self { inner }
    }

    /// `GET /api/db`: the databases this key can see. An app key sees only its own.
    pub async fn list(&self) -> Result<Vec<DbInfo>> {
        let req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "db"])?)
            .timeout(self.inner.timeout);
        let list: DbList = self.inner.send(req).await?.json().await?;
        Ok(list.databases)
    }

    /// `GET /api/db/{app}/{env}`.
    pub async fn info(&self, app: Option<&str>, env: Option<&str>) -> Result<DbInfo> {
        let (app, env) = place(app, env)?;
        let req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "db", &app, &env])?)
            .timeout(self.inner.timeout);
        Ok(self.inner.send(req).await?.json().await?)
    }

    /// `GET /api/db/{app}/{env}/tables`.
    pub async fn tables(&self, app: Option<&str>, env: Option<&str>) -> Result<Vec<DbTable>> {
        let (app, env) = place(app, env)?;
        let req = self
            .inner
            .request(
                Method::GET,
                self.inner.url(&["api", "db", &app, &env, "tables"])?,
            )
            .timeout(self.inner.timeout);
        let list: TableList = self.inner.send(req).await?.json().await?;
        Ok(list.tables)
    }

    /// `GET /api/db/{app}/{env}/tables/{table}/rows`: one page of a table.
    pub async fn rows(
        &self,
        app: Option<&str>,
        env: Option<&str>,
        table: &str,
        opts: RowsOptions,
    ) -> Result<DbRows> {
        let (app, env) = place(app, env)?;
        let url = self
            .inner
            .url(&["api", "db", &app, &env, "tables", table, "rows"])?;
        if matches!(opts.limit, Some(l) if !(1..=200).contains(&l)) {
            return Err(Error::config(
                "The limit must be a whole number from 1 to 200.",
            ));
        }
        let mut query: Vec<(&str, String)> = Vec::new();
        if let Some(v) = opts.schema {
            query.push(("schema", v));
        }
        if let Some(v) = opts.limit {
            query.push(("limit", v.to_string()));
        }
        if let Some(v) = opts.offset {
            query.push(("offset", v.to_string()));
        }
        let req = self
            .inner
            .request(Method::GET, url)
            .timeout(self.inner.timeout)
            .query(&query);
        Ok(self.inner.send(req).await?.json().await?)
    }

    /// `POST /api/db/{app}/{env}/query`: read-only SQL. Never sends `write`.
    pub async fn query(
        &self,
        app: Option<&str>,
        env: Option<&str>,
        sql: &str,
    ) -> Result<DbQueryResult> {
        let (app, env) = place(app, env)?;
        let req = self
            .inner
            .request(
                Method::POST,
                self.inner.url(&["api", "db", &app, &env, "query"])?,
            )
            .timeout(self.inner.timeout)
            .json(&json!({ "sql": sql }));
        Ok(self.inner.send(req).await?.json().await?)
    }
}
