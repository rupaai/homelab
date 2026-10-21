import { HomelabError } from "./errors.js";
import { seg, type Transport } from "./http.js";
import type {
  CreateFolderOptions,
  DownloadOptions,
  File,
  Folder,
  ListFilters,
  ListResult,
  UpdateFileOptions,
  UploadBody,
  UploadFileOptions,
  UploadOptions,
  UploadResult,
} from "./types.js";

const FILTER_KEYS: Array<[keyof ListFilters, string]> = [
  ["tag", "tag"],
  ["type", "type"],
  ["takenAfter", "taken_after"],
  ["takenBefore", "taken_before"],
  ["camera", "camera"],
  ["after", "after"],
  ["limit", "limit"],
];

/** `homelab.files`: thin wrappers over /api/files. */
export class Files {
  readonly #t: Transport;

  constructor(transport: Transport) {
    this.#t = transport;
  }

  /** GET /api/files/folders */
  async folders(): Promise<Folder[]> {
    return (await this.#t.json<{ folders: Folder[] }>("/api/files/folders")).folders;
  }

  /** POST /api/files/folders */
  createFolder(name: string, opts: CreateFolderOptions = {}): Promise<Folder> {
    const body: { name: string; disk?: string } = { name };
    if (opts.disk !== undefined) body.disk = opts.disk;
    return this.#t.json<Folder>("/api/files/folders", { method: "POST", json: body });
  }

  /** Create the folder; on 409 return the existing one from `folders()`. */
  async ensureFolder(name: string, opts: CreateFolderOptions = {}): Promise<Folder> {
    try {
      return await this.createFolder(name, opts);
    } catch (err) {
      if (!(err instanceof HomelabError) || err.status !== 409) throw err;
      const existing = (await this.folders()).find((f) => f.name === name);
      if (!existing) throw err;
      return existing;
    }
  }

  /** DELETE /api/files/folders/{name} (the folder must be empty). */
  async deleteFolder(name: string): Promise<void> {
    await this.#t.request(`/api/files/folders/${seg(name)}`, { method: "DELETE" });
  }

  /** POST /api/files/{folder}: the body is the raw bytes. */
  async upload(folder: string, data: UploadBody, opts: UploadOptions): Promise<UploadResult> {
    return this.#upload(folder, data, opts, {});
  }

  /**
   * Node only: stream a file from disk. `node:fs` is imported lazily, so the package
   * still loads in browsers. `name` defaults to the file's base name.
   */
  async uploadFile(folder: string, path: string, opts: UploadFileOptions = {}): Promise<UploadResult> {
    const [fs, nodePath, stream] = await Promise.all([
      import("node:fs"),
      import("node:path"),
      import("node:stream"),
    ]);
    const size = (await fs.promises.stat(path)).size;
    const body = stream.Readable.toWeb(fs.createReadStream(path)) as ReadableStream<Uint8Array>;
    return this.#upload(
      folder,
      body,
      { ...opts, name: opts.name ?? nodePath.basename(path) },
      { "Content-Length": String(size) },
    );
  }

  async #upload(
    folder: string,
    data: UploadBody,
    opts: UploadOptions,
    headers: Record<string, string>,
  ): Promise<UploadResult> {
    const query: Array<[string, string]> = [["name", opts.name]];
    if (opts.tags && opts.tags.length > 0) query.push(["tags", opts.tags.join(",")]);
    if (opts.sha256) query.push(["sha256", opts.sha256]);
    const res = await this.#t.request(`/api/files/${seg(folder)}`, {
      method: "POST",
      query,
      headers,
      body: data as BodyInit,
      noTimeout: true,
    });
    const file = (await res.json()) as File;
    return { file, duplicate: res.headers.get("X-Homelab-Duplicate") === "true" };
  }

  /** GET /api/files/{folder}: one page, newest first. */
  async list(folder: string, filters: ListFilters = {}): Promise<ListResult> {
    const query: Array<[string, string]> = [];
    for (const [key, param] of FILTER_KEYS) {
      const v = filters[key];
      if (v !== undefined) query.push([param, String(v)]);
    }
    return this.#t.json<ListResult>(`/api/files/${seg(folder)}`, { query });
  }

  /** Every file matching the filters, following `next` as `after` until it is null. */
  async *all(folder: string, filters: ListFilters = {}): AsyncGenerator<File, void, undefined> {
    let after = filters.after;
    for (;;) {
      const page = await this.list(folder, after === undefined ? filters : { ...filters, after });
      yield* page.files;
      if (page.next === null || page.next === undefined) return;
      after = page.next;
    }
  }

  /** GET /api/files/{folder}/{id}/info */
  async info(folder: string, id: string): Promise<File> {
    return this.#t.json<File>(`/api/files/${seg(folder)}/${seg(id)}/info`);
  }

  /** PATCH /api/files/{folder}/{id}/info: replaces `tags` and/or `info`. */
  async update(folder: string, id: string, changes: UpdateFileOptions): Promise<File> {
    const body: UpdateFileOptions = {};
    if (changes.tags !== undefined) body.tags = changes.tags;
    if (changes.info !== undefined) body.info = changes.info;
    return this.#t.json<File>(`/api/files/${seg(folder)}/${seg(id)}/info`, { method: "PATCH", json: body });
  }

  /**
   * GET /api/files/{folder}/{id}. Returns the fetch `Response` itself (status 200, or
   * 206 with `range`): use `.arrayBuffer()`, `.blob()` or `.body` (a stream). Non-2xx
   * answers throw HomelabError instead.
   */
  async download(folder: string, id: string, opts: DownloadOptions = {}): Promise<Response> {
    const headers: Record<string, string> = {};
    if (opts.range) headers["Range"] = opts.range;
    return this.#t.request(`/api/files/${seg(folder)}/${seg(id)}`, { headers, noTimeout: true });
  }

  /** DELETE /api/files/{folder}/{id}: moves the file to the trash. */
  async delete(folder: string, id: string): Promise<void> {
    await this.#t.request(`/api/files/${seg(folder)}/${seg(id)}`, { method: "DELETE" });
  }
}
