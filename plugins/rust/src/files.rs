use std::collections::VecDeque;
use std::path::Path;
use std::sync::Arc;

use bytes::Bytes;
use futures_util::stream::{self, Stream, StreamExt, TryStreamExt};
use reqwest::{Body, Method};
use serde::Deserialize;
use serde_json::{json, Map, Value};

use crate::client::Inner;
use crate::error::{Error, Result};
use crate::types::*;

/// The Files service. Get one with [`Homelab::files`](crate::Homelab::files).
#[derive(Clone)]
pub struct Files {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for Files {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Files").finish_non_exhaustive()
    }
}

#[derive(Deserialize)]
struct FolderList {
    #[serde(default)]
    folders: Vec<Folder>,
}

impl Files {
    pub(crate) fn new(inner: Arc<Inner>) -> Self {
        Self { inner }
    }

    /// `GET /api/files/folders`
    pub async fn folders(&self) -> Result<Vec<Folder>> {
        let req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "files", "folders"])?)
            .timeout(self.inner.timeout);
        let list: FolderList = self.inner.send(req).await?.json().await?;
        Ok(list.folders)
    }

    /// `POST /api/files/folders`
    pub async fn create_folder(&self, name: &str, opts: FolderOptions) -> Result<Folder> {
        let mut body = Map::new();
        body.insert("name".into(), json!(name));
        if let Some(d) = opts.disk {
            body.insert("disk".into(), json!(d));
        }
        let req = self
            .inner
            .request(Method::POST, self.inner.url(&["api", "files", "folders"])?)
            .timeout(self.inner.timeout)
            .json(&body);
        Ok(self.inner.send(req).await?.json().await?)
    }

    /// Creates the folder; if it already exists (409) returns the existing one.
    pub async fn ensure_folder(&self, name: &str, opts: FolderOptions) -> Result<Folder> {
        match self.create_folder(name, opts).await {
            Err(Error::Api {
                status: 409,
                message,
            }) => self
                .folders()
                .await?
                .into_iter()
                .find(|f| f.name == name)
                .ok_or(Error::Api {
                    status: 409,
                    message,
                }),
            other => other,
        }
    }

    /// `DELETE /api/files/folders/{name}` (the folder must be empty).
    pub async fn delete_folder(&self, name: &str) -> Result<()> {
        let req = self
            .inner
            .request(
                Method::DELETE,
                self.inner.url(&["api", "files", "folders", name])?,
            )
            .timeout(self.inner.timeout);
        self.inner.send(req).await?;
        Ok(())
    }

    /// `POST /api/files/{folder}` with `data` as the raw body.
    ///
    /// `data` can be `Bytes`, `Vec<u8>`, `String`, `&'static [u8]`, a
    /// `tokio::fs::File`, or a stream via `reqwest::Body::wrap_stream`.
    /// There is no timeout on the body.
    pub async fn upload(
        &self,
        folder: &str,
        data: impl Into<Body>,
        opts: UploadOptions,
    ) -> Result<Uploaded> {
        let mut query: Vec<(&str, String)> = Vec::new();
        if let Some(n) = opts.name {
            query.push(("name", n));
        }
        if !opts.tags.is_empty() {
            query.push(("tags", opts.tags.join(",")));
        }
        if let Some(s) = opts.sha256 {
            query.push(("sha256", s));
        }
        let req = self
            .inner
            .request(Method::POST, self.inner.url(&["api", "files", folder])?)
            .query(&query)
            .header(reqwest::header::CONTENT_TYPE, "application/octet-stream")
            .body(data);
        let resp = self.inner.send(req).await?;
        let duplicate = resp
            .headers()
            .get("x-homelab-duplicate")
            .and_then(|v| v.to_str().ok())
            .is_some_and(|v| v.trim().eq_ignore_ascii_case("true"));
        let file: File = resp.json().await?;
        Ok(Uploaded { file, duplicate })
    }

    /// Streams the file at `path` from disk. If `opts.name` is not set, the
    /// file's own name is used.
    pub async fn upload_file(
        &self,
        folder: &str,
        path: impl AsRef<Path>,
        mut opts: UploadOptions,
    ) -> Result<Uploaded> {
        let path = path.as_ref();
        if opts.name.is_none() {
            opts.name = path
                .file_name()
                .and_then(|n| n.to_str())
                .map(str::to_string);
        }
        let file = tokio::fs::File::open(path)
            .await
            .map_err(|e| Error::Config(format!("cannot open {}: {e}", path.display())))?;
        let body = Body::wrap_stream(tokio_util::io::ReaderStream::new(file));
        self.upload(folder, body, opts).await
    }

    /// `GET /api/files/{folder}`: one page.
    pub async fn list(&self, folder: &str, opts: ListOptions) -> Result<Page> {
        let mut query: Vec<(&str, String)> = Vec::new();
        if let Some(v) = opts.tag {
            query.push(("tag", v));
        }
        if let Some(v) = opts.content_type {
            query.push(("type", v));
        }
        if let Some(v) = opts.taken_after {
            query.push(("taken_after", v));
        }
        if let Some(v) = opts.taken_before {
            query.push(("taken_before", v));
        }
        if let Some(v) = opts.camera {
            query.push(("camera", v));
        }
        if let Some(v) = opts.after {
            query.push(("after", v));
        }
        if let Some(v) = opts.limit {
            query.push(("limit", v.to_string()));
        }
        let req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "files", folder])?)
            .query(&query)
            .timeout(self.inner.timeout);
        Ok(self.inner.send(req).await?.json().await?)
    }

    /// Every matching file, following `next` as `after` page by page.
    /// Pages are fetched lazily as the stream is polled.
    pub fn all(
        &self,
        folder: &str,
        opts: ListOptions,
    ) -> impl Stream<Item = Result<File>> + Send + 'static {
        struct State {
            files: Files,
            folder: String,
            opts: ListOptions,
            buf: VecDeque<File>,
            done: bool,
        }
        let state = State {
            files: self.clone(),
            folder: folder.to_string(),
            opts,
            buf: VecDeque::new(),
            done: false,
        };
        stream::try_unfold(state, |mut s| async move {
            loop {
                if let Some(f) = s.buf.pop_front() {
                    return Ok(Some((f, s)));
                }
                if s.done {
                    return Ok(None);
                }
                let page = s.files.list(&s.folder, s.opts.clone()).await?;
                match page.next {
                    Some(n) if !page.files.is_empty() => s.opts.after = Some(n),
                    _ => s.done = true,
                }
                s.buf.extend(page.files);
            }
        })
    }

    /// `GET /api/files/{folder}/{id}/info`
    pub async fn info(&self, folder: &str, id: &str) -> Result<File> {
        let req = self
            .inner
            .request(
                Method::GET,
                self.inner.url(&["api", "files", folder, id, "info"])?,
            )
            .timeout(self.inner.timeout);
        Ok(self.inner.send(req).await?.json().await?)
    }

    /// `PATCH /api/files/{folder}/{id}/info`
    pub async fn update(&self, folder: &str, id: &str, opts: UpdateOptions) -> Result<File> {
        let mut body = Map::new();
        if let Some(t) = opts.tags {
            body.insert("tags".into(), json!(t));
        }
        if let Some(i) = opts.info {
            body.insert("info".into(), Value::Object(i));
        }
        let req = self
            .inner
            .request(
                Method::PATCH,
                self.inner.url(&["api", "files", folder, id, "info"])?,
            )
            .timeout(self.inner.timeout)
            .json(&body);
        Ok(self.inner.send(req).await?.json().await?)
    }

    /// `GET /api/files/{folder}/{id}`: the whole body in memory.
    pub async fn download(&self, folder: &str, id: &str, opts: DownloadOptions) -> Result<Bytes> {
        let resp = self.download_response(folder, id, opts).await?;
        Ok(resp.bytes().await?)
    }

    /// Like [`download`](Self::download), but as a stream of chunks.
    pub async fn download_stream(
        &self,
        folder: &str,
        id: &str,
        opts: DownloadOptions,
    ) -> Result<impl Stream<Item = Result<Bytes>> + Send + 'static> {
        let resp = self.download_response(folder, id, opts).await?;
        Ok(resp.bytes_stream().map_err(Error::from).boxed())
    }

    async fn download_response(
        &self,
        folder: &str,
        id: &str,
        opts: DownloadOptions,
    ) -> Result<reqwest::Response> {
        let mut req = self
            .inner
            .request(Method::GET, self.inner.url(&["api", "files", folder, id])?);
        if let Some(r) = opts.range {
            req = req.header(reqwest::header::RANGE, r);
        }
        self.inner.send(req).await
    }

    /// `DELETE /api/files/{folder}/{id}`: moves the file to the trash.
    pub async fn delete(&self, folder: &str, id: &str) -> Result<()> {
        let req = self
            .inner
            .request(
                Method::DELETE,
                self.inner.url(&["api", "files", folder, id])?,
            )
            .timeout(self.inner.timeout);
        self.inner.send(req).await?;
        Ok(())
    }
}
