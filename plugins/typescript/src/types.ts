/** A folder, as returned by the Files API. */
export interface Folder {
  name: string;
  disk: string;
  /** Owning app, or "" for an admin-owned folder. */
  app: string;
  /** "dev", "prod", or "" for an admin-owned folder. */
  env: string;
  /** Files not in the trash (0 in the answer to a create). */
  files: number;
  /** Their total size in bytes (0 in the answer to a create). */
  bytes: number;
  /** Unix time in seconds. */
  created_at: number;
}

export interface Gps {
  lat: number;
  lon: number;
  alt?: number;
}

/** What the server found inside a file. Every key is optional; `{}` is normal. */
export interface Meta {
  width?: number;
  height?: number;
  taken_at?: string;
  camera_make?: string;
  camera_model?: string;
  lens?: string;
  orientation?: number;
  gps?: Gps;
  duration_s?: number;
  title?: string;
  artist?: string;
  album?: string;
  year?: number;
  pages?: number;
  author?: string;
  /** Unknown keys from newer servers are kept. */
  [key: string]: unknown;
}

/** A stored file. */
export interface File {
  id: string;
  folder: string;
  name: string;
  content_type: string;
  size: number;
  sha256: string;
  /** Unix time in seconds. */
  created_at: number;
  created_by: string;
  tags: string[];
  info: Record<string, unknown>;
  meta: Meta;
}

export interface CreateFolderOptions {
  /** A configured disk. The server's first disk when left out. */
  disk?: string;
}

export interface UploadOptions {
  /** The file's name (the server cleans it). */
  name: string;
  tags?: string[];
  /** Hex SHA-256. The server refuses the upload when the bytes do not hash to this. */
  sha256?: string;
}

export interface UploadFileOptions extends Omit<UploadOptions, "name"> {
  /** Defaults to the base name of the path. */
  name?: string;
}

export interface UploadResult {
  file: File;
  /** True when the folder already held this file (status 200); nothing was stored. */
  duplicate: boolean;
}

/** Accepted upload bodies. */
export type UploadBody = Uint8Array | ArrayBuffer | Blob | ReadableStream<Uint8Array>;

export interface ListFilters {
  /** Only files that have exactly this tag. */
  tag?: string;
  /** Content type prefix, for example "image/". */
  type?: string;
  /** RFC 3339 date or date-time. */
  takenAfter?: string;
  /** RFC 3339 date or date-time (exclusive). */
  takenBefore?: string;
  /** Case-insensitive text in camera make or model. */
  camera?: string;
  /** A file id: only files older than it. */
  after?: string;
  /** 1-200, default 50. */
  limit?: number;
}

export interface ListResult {
  files: File[];
  /** Pass as `after` for the next page; null on the last page. */
  next: string | null;
}

export interface UpdateFileOptions {
  /** Replaces the tags. */
  tags?: string[];
  /** Replaces `info`. */
  info?: Record<string, unknown>;
}

export interface DownloadOptions {
  /** A Range header value such as "bytes=0-99". */
  range?: string;
}

/** One secret in the list. Never holds the value. */
export interface SecretInfo {
  name: string;
  /** "config" was set by the owner in the companion; "app" was saved by the app. */
  kind: "config" | "app";
  version: number;
  /** Unix time in seconds. */
  updated_at: number;
}

/** One kept version of a secret. Never holds the value. */
export interface SecretVersion {
  version: number;
  /** Unix time in seconds. */
  set_at: number;
}

/** The answer to `secrets.set`. */
export interface SecretSaved {
  name: string;
  version: number;
}

export interface GetSecretOptions {
  /** An older kept version (1 or more). Always asks Homelab, never reads a file. */
  version?: number;
  /** Skip the `/run/secrets/<name>` file and always ask Homelab. */
  fresh?: boolean;
}

/** One database, as returned by the Databases API. */
export interface DbInfo {
  app: string;
  env: string;
  /** "postgres" or "sqlite". */
  engine: string;
  /** For example "up" or "down". */
  status: string;
  size_bytes: number;
  connections: number;
  tables: number;
}

/** One table in a database. */
export interface DbTable {
  schema: string;
  name: string;
  /** An estimate of the row count. */
  rows: number;
}

export interface DbRowsOptions {
  /** Defaults to "public" on the server. */
  schema?: string;
  /** 1-200, default 50. */
  limit?: number;
  /** From 0. */
  offset?: number;
}

/** The answer to `db.rows`. Each row is an array in the order of `columns`. */
export interface DbRows {
  columns: string[];
  rows: unknown[][];
  /** True when more rows were left out. */
  truncated: boolean;
}

/** The answer to `db.query` (read-only SQL). */
export interface DbQueryResult extends DbRows {
  row_count: number;
  /** The SQL command, for example "SELECT". */
  command: string;
  /** How long the query took, in milliseconds. */
  ms: number;
}
