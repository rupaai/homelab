use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

/// A folder. Unknown fields from the server end up in `extra`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Folder {
    pub name: String,
    pub disk: String,
    /// Owning app, or "" for an admin-owned folder.
    pub app: String,
    /// `dev`, `prod`, or "".
    pub env: String,
    /// Files not in the trash.
    pub files: u64,
    /// Their total size in bytes.
    pub bytes: u64,
    /// Unix time in seconds.
    pub created_at: i64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// A stored file. Unknown fields from the server end up in `extra`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct File {
    /// A ULID.
    pub id: String,
    pub folder: String,
    pub name: String,
    pub content_type: String,
    /// Bytes.
    pub size: u64,
    /// Lower case hex.
    pub sha256: String,
    /// Unix time in seconds.
    pub created_at: i64,
    /// `person:<id>`, `key:<id>` or `app:<app>/<env>`.
    pub created_by: String,
    pub tags: Vec<String>,
    /// Whatever the app saved with `update`.
    pub info: Map<String, Value>,
    /// What the server found inside the file. Read only.
    pub meta: Meta,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// What the server found inside a file. Every key is `None` when not found.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Meta {
    pub width: Option<u32>,
    pub height: Option<u32>,
    /// RFC 3339; without an offset when the camera wrote none.
    pub taken_at: Option<String>,
    pub camera_make: Option<String>,
    pub camera_model: Option<String>,
    pub lens: Option<String>,
    /// EXIF orientation, 1 to 8.
    pub orientation: Option<u8>,
    pub gps: Option<Gps>,
    pub duration_s: Option<f64>,
    pub title: Option<String>,
    pub artist: Option<String>,
    pub album: Option<String>,
    pub year: Option<i32>,
    pub pages: Option<u32>,
    pub author: Option<String>,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// A GPS position.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Gps {
    pub lat: f64,
    pub lon: f64,
    pub alt: Option<f64>,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// The answer to an upload.
#[derive(Debug, Clone, PartialEq)]
pub struct Uploaded {
    pub file: File,
    /// True when the folder already held these bytes and nothing new was stored
    /// (`X-Homelab-Duplicate: true`, status 200).
    pub duplicate: bool,
}

/// One page of a listing.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Page {
    pub files: Vec<File>,
    /// Pass as `after` for the next page; `None` on the last page.
    pub next: Option<String>,
}

/// Options for creating a folder.
#[derive(Debug, Clone, Default)]
pub struct FolderOptions {
    pub disk: Option<String>,
}

impl FolderOptions {
    pub fn disk(mut self, disk: impl Into<String>) -> Self {
        self.disk = Some(disk.into());
        self
    }
}

/// Options for an upload.
#[derive(Debug, Clone, Default)]
pub struct UploadOptions {
    /// The file's name (the server cleans it).
    pub name: Option<String>,
    pub tags: Vec<String>,
    /// 64 hex digits; the server refuses the upload if the bytes differ.
    pub sha256: Option<String>,
}

impl UploadOptions {
    pub fn new(name: impl Into<String>) -> Self {
        Self {
            name: Some(name.into()),
            ..Self::default()
        }
    }
    pub fn name(mut self, name: impl Into<String>) -> Self {
        self.name = Some(name.into());
        self
    }
    pub fn tags<I, S>(mut self, tags: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        self.tags = tags.into_iter().map(Into::into).collect();
        self
    }
    pub fn sha256(mut self, sha256: impl Into<String>) -> Self {
        self.sha256 = Some(sha256.into());
        self
    }
}

/// Filters for `list` and `all`.
#[derive(Debug, Clone, Default)]
pub struct ListOptions {
    pub tag: Option<String>,
    /// Content type prefix, e.g. `image/`.
    pub content_type: Option<String>,
    /// RFC 3339 date or date-time.
    pub taken_after: Option<String>,
    pub taken_before: Option<String>,
    pub camera: Option<String>,
    /// A file id; only older files. Use a page's `next`.
    pub after: Option<String>,
    /// Files per page (the server clamps to 1-200).
    pub limit: Option<u32>,
}

impl ListOptions {
    pub fn tag(mut self, v: impl Into<String>) -> Self {
        self.tag = Some(v.into());
        self
    }
    pub fn content_type(mut self, v: impl Into<String>) -> Self {
        self.content_type = Some(v.into());
        self
    }
    pub fn taken_after(mut self, v: impl Into<String>) -> Self {
        self.taken_after = Some(v.into());
        self
    }
    pub fn taken_before(mut self, v: impl Into<String>) -> Self {
        self.taken_before = Some(v.into());
        self
    }
    pub fn camera(mut self, v: impl Into<String>) -> Self {
        self.camera = Some(v.into());
        self
    }
    pub fn after(mut self, v: impl Into<String>) -> Self {
        self.after = Some(v.into());
        self
    }
    pub fn limit(mut self, v: u32) -> Self {
        self.limit = Some(v);
        self
    }
}

/// Changes for `update`. A field left as `None` stays as it is.
#[derive(Debug, Clone, Default)]
pub struct UpdateOptions {
    /// Replaces the tags.
    pub tags: Option<Vec<String>>,
    /// Replaces `info`.
    pub info: Option<Map<String, Value>>,
}

impl UpdateOptions {
    pub fn tags<I, S>(mut self, tags: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        self.tags = Some(tags.into_iter().map(Into::into).collect());
        self
    }
    pub fn info(mut self, info: Map<String, Value>) -> Self {
        self.info = Some(info);
        self
    }
}

/// Options for a download.
#[derive(Debug, Clone, Default)]
pub struct DownloadOptions {
    /// A `Range` header value such as `bytes=0-99`.
    pub range: Option<String>,
}

impl DownloadOptions {
    pub fn range(mut self, range: impl Into<String>) -> Self {
        self.range = Some(range.into());
        self
    }
}

/// One secret in the list. Never holds the value. Unknown fields end up in `extra`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct SecretInfo {
    pub name: String,
    /// `config` (set by the owner) or `app` (saved by the app).
    pub kind: String,
    pub version: u64,
    /// Unix time in seconds.
    pub updated_at: i64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// One kept version of a secret. Never holds the value.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct SecretVersion {
    pub version: u64,
    /// Unix time in seconds.
    pub set_at: i64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// The answer to `set`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct SecretSaved {
    pub name: String,
    pub version: u64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// Options for `Secrets::get`.
#[derive(Debug, Clone, Default)]
pub struct GetSecretOptions {
    /// An older kept version (1 or more). Always asks Homelab, never reads a file.
    pub version: Option<u64>,
    /// Skip the `/run/secrets/<name>` file and always ask Homelab.
    pub fresh: bool,
}

impl GetSecretOptions {
    pub fn version(mut self, version: u64) -> Self {
        self.version = Some(version);
        self
    }
    pub fn fresh(mut self) -> Self {
        self.fresh = true;
        self
    }
}

/// One database. Unknown fields from the server end up in `extra`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DbInfo {
    pub app: String,
    /// `dev` or `prod`.
    pub env: String,
    /// `postgres` or `sqlite`.
    pub engine: String,
    /// For example `up` or `down`.
    pub status: String,
    pub size_bytes: u64,
    pub connections: u64,
    pub tables: u64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// One table. `rows` is an estimate.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DbTable {
    pub schema: String,
    pub name: String,
    pub rows: u64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// One page of a table. Each row is a list in the order of `columns`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DbRows {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<Value>>,
    /// True when more rows were left out.
    pub truncated: bool,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// The answer to `Db::query` (read-only SQL).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DbQueryResult {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<Value>>,
    pub truncated: bool,
    pub row_count: u64,
    /// The SQL command, for example `SELECT`.
    pub command: String,
    /// How long the query took, in milliseconds.
    pub ms: u64,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// Options for `Db::rows`.
#[derive(Debug, Clone, Default)]
pub struct RowsOptions {
    /// The server uses `public` when this is left out.
    pub schema: Option<String>,
    /// 1 to 200 (the server uses 50 when left out).
    pub limit: Option<u32>,
    /// From 0.
    pub offset: Option<u64>,
}

impl RowsOptions {
    pub fn schema(mut self, schema: impl Into<String>) -> Self {
        self.schema = Some(schema.into());
        self
    }
    pub fn limit(mut self, limit: u32) -> Self {
        self.limit = Some(limit);
        self
    }
    pub fn offset(mut self, offset: u64) -> Self {
        self.offset = Some(offset);
        self
    }
}

/// One release of the app. Dev builds are pre-releases.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Release {
    /// For example `v0.4.0` or `v0.4.0-dev.2`.
    pub tag: String,
    pub prerelease: bool,
    /// The full commit it was made from.
    pub commit: String,
    /// An ISO 8601 time.
    pub date: String,
    /// A short line such as `2 new, 1 fix`. Empty without notes.
    pub summary: String,
    /// The release notes as markdown.
    pub notes: String,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}
