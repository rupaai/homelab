import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Homelab, HomelabError, type File, type Folder } from "../src/index.js";

const TOKEN = "hlk_secret_token_value_0123456789";

interface Recorded {
  method: string;
  path: string;
  query: Record<string, string>;
  headers: IncomingMessage["headers"];
  body: Buffer;
}
interface Canned {
  status?: number;
  headers?: Record<string, string>;
  json?: unknown;
  raw?: string | Buffer;
}

let server: Server;
let url: string;
let requests: Recorded[] = [];
let handler: (req: Recorded) => Canned = () => ({ status: 200, json: {} });

beforeAll(async () => {
  server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const u = new URL(req.url ?? "/", "http://x");
      // Keep the raw path so encoding can be asserted.
      const rawPath = (req.url ?? "/").split("?")[0] ?? "/";
      const rec: Recorded = {
        method: req.method ?? "",
        path: rawPath,
        query: Object.fromEntries(u.searchParams),
        headers: req.headers,
        body: Buffer.concat(chunks),
      };
      requests.push(rec);
      const c = handler(rec);
      const headers = { ...(c.json !== undefined ? { "Content-Type": "application/json" } : {}), ...c.headers };
      res.writeHead(c.status ?? 200, headers);
      res.end(c.json !== undefined ? JSON.stringify(c.json) : c.raw);
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});
afterAll(() => new Promise<void>((r) => server.close(() => r())));
beforeEach(() => {
  requests = [];
  handler = () => ({ status: 200, json: {} });
});

const hl = () => new Homelab({ url, token: TOKEN });
const last = () => requests[requests.length - 1]!;

const folder = (name: string): Folder => ({
  name, disk: "main", app: "", env: "", files: 0, bytes: 0, created_at: 1,
});
const file = (id: string, extra: Partial<File> = {}): File => ({
  id, folder: "posters", name: "a.jpg", content_type: "image/jpeg", size: 3, sha256: "ab",
  created_at: 1, created_by: "key:1", tags: [], info: {}, meta: {}, ...extra,
});

describe("config", () => {
  it("throws a clear error when url is missing", () => {
    const old = { u: process.env["HOMELAB_API_URL"], t: process.env["HOMELAB_TOKEN"] };
    delete process.env["HOMELAB_API_URL"];
    delete process.env["HOMELAB_TOKEN"];
    try {
      expect(() => new Homelab({ token: "x" })).toThrow(/Set HOMELAB_API_URL or pass url/);
      expect(() => new Homelab({ url })).toThrow(/Set HOMELAB_TOKEN or pass token/);
      expect(() => new Homelab()).toThrowError(HomelabError);
    } finally {
      if (old.u !== undefined) process.env["HOMELAB_API_URL"] = old.u;
      if (old.t !== undefined) process.env["HOMELAB_TOKEN"] = old.t;
    }
  });

  it("reads env vars and tolerates a trailing slash", async () => {
    process.env["HOMELAB_API_URL"] = url + "///";
    process.env["HOMELAB_TOKEN"] = TOKEN;
    try {
      handler = () => ({ json: { folders: [] } });
      await new Homelab().files.folders();
      expect(last().path).toBe("/api/files/folders");
      expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
    } finally {
      delete process.env["HOMELAB_API_URL"];
      delete process.env["HOMELAB_TOKEN"];
    }
  });

  it("never exposes the token when printed or serialised", () => {
    const c = hl();
    expect(JSON.stringify(c)).not.toContain(TOKEN);
    expect(String(Object.values(c.files))).not.toContain(TOKEN);
    expect(JSON.stringify(Object.getOwnPropertyNames(c.files))).not.toContain(TOKEN);
  });
});

describe("folders", () => {
  it("folders() GETs and unwraps", async () => {
    handler = () => ({ json: { folders: [folder("a"), folder("b")] } });
    const r = await hl().files.folders();
    expect(r.map((f) => f.name)).toEqual(["a", "b"]);
    expect(last().method).toBe("GET");
    expect(last().path).toBe("/api/files/folders");
    expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
  });

  it("createFolder() POSTs JSON", async () => {
    handler = () => ({ status: 201, json: folder("posters") });
    const r = await hl().files.createFolder("posters", { disk: "big" });
    expect(r.name).toBe("posters");
    expect(last().method).toBe("POST");
    expect(last().path).toBe("/api/files/folders");
    expect(last().headers["content-type"]).toBe("application/json");
    expect(JSON.parse(last().body.toString())).toEqual({ name: "posters", disk: "big" });
  });

  it("createFolder() omits disk when not given", async () => {
    handler = () => ({ status: 201, json: folder("p") });
    await hl().files.createFolder("p");
    expect(JSON.parse(last().body.toString())).toEqual({ name: "p" });
  });

  it("ensureFolder() returns the created folder", async () => {
    handler = () => ({ status: 201, json: folder("p") });
    await hl().files.ensureFolder("p");
    expect(requests).toHaveLength(1);
  });

  it("ensureFolder() on 409 returns the existing folder from folders()", async () => {
    handler = (r) =>
      r.method === "POST"
        ? { status: 409, json: { error: "There is already a folder with that name." } }
        : { json: { folders: [folder("other"), folder("p")] } };
    const r = await hl().files.ensureFolder("p");
    expect(r.name).toBe("p");
    expect(requests.map((x) => x.method)).toEqual(["POST", "GET"]);
  });

  it("ensureFolder() rethrows the 409 when the folder is not listed", async () => {
    handler = (r) =>
      r.method === "POST" ? { status: 409, json: { error: "exists" } } : { json: { folders: [] } };
    await expect(hl().files.ensureFolder("p")).rejects.toMatchObject({ status: 409, message: "exists" });
  });

  it("ensureFolder() does not swallow other errors", async () => {
    handler = () => ({ status: 403, json: { error: "no" } });
    await expect(hl().files.ensureFolder("p")).rejects.toMatchObject({ status: 403 });
    expect(requests).toHaveLength(1);
  });

  it("deleteFolder() DELETEs and returns void", async () => {
    handler = () => ({ status: 204 });
    expect(await hl().files.deleteFolder("posters")).toBeUndefined();
    expect(last().method).toBe("DELETE");
    expect(last().path).toBe("/api/files/folders/posters");
  });
});

describe("upload", () => {
  it("sends the raw bytes with name, tags and sha256 in the query", async () => {
    handler = () => ({ status: 201, json: file("01A") });
    const bytes = new Uint8Array([0, 1, 2, 255]);
    const r = await hl().files.upload("posters", bytes, { name: "a b.jpg", tags: ["dune", "my tag"], sha256: "f".repeat(64) });
    expect(r.duplicate).toBe(false);
    expect(r.file.id).toBe("01A");
    expect(last().method).toBe("POST");
    expect(last().path).toBe("/api/files/posters");
    expect(last().query).toEqual({ name: "a b.jpg", tags: "dune,my tag", sha256: "f".repeat(64) });
    expect([...last().body]).toEqual([0, 1, 2, 255]);
    expect(last().headers["content-type"]).not.toBe("application/json");
    expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
  });

  it("omits tags and sha256 when not given", async () => {
    handler = () => ({ status: 201, json: file("01A") });
    await hl().files.upload("posters", new Uint8Array([1]), { name: "x" });
    expect(last().query).toEqual({ name: "x" });
  });

  it("accepts ArrayBuffer, Blob and ReadableStream", async () => {
    handler = () => ({ status: 201, json: file("01A") });
    const c = hl();
    await c.files.upload("p", new Uint8Array([1, 2, 3]).buffer, { name: "a" });
    expect([...last().body]).toEqual([1, 2, 3]);
    await c.files.upload("p", new Blob([new Uint8Array([4, 5])]), { name: "a" });
    expect([...last().body]).toEqual([4, 5]);
    const stream = new ReadableStream<Uint8Array>({
      start(ctl) {
        ctl.enqueue(new Uint8Array([6]));
        ctl.enqueue(new Uint8Array([7, 8]));
        ctl.close();
      },
    });
    await c.files.upload("p", stream, { name: "a" });
    expect([...last().body]).toEqual([6, 7, 8]);
  });

  it("flags a duplicate from the X-Homelab-Duplicate header", async () => {
    handler = () => ({ status: 200, headers: { "X-Homelab-Duplicate": "true" }, json: file("01A") });
    const r = await hl().files.upload("p", new Uint8Array([1]), { name: "a" });
    expect(r.duplicate).toBe(true);
    expect(r.file.id).toBe("01A");
  });

  it("uploadFile() streams a file from disk, name defaults to the base name", async () => {
    handler = () => ({ status: 201, json: file("01A") });
    const dir = mkdtempSync(join(tmpdir(), "hl-"));
    const path = join(dir, "poster.jpg");
    const content = Buffer.alloc(300_000, 7);
    writeFileSync(path, content);
    const r = await hl().files.uploadFile("posters", path, { tags: ["x"] });
    expect(r.duplicate).toBe(false);
    expect(last().query).toEqual({ name: "poster.jpg", tags: "x" });
    expect(last().body.equals(content)).toBe(true);
    expect(last().headers["content-length"]).toBe("300000");
    await hl().files.uploadFile("posters", path, { name: "custom.bin" });
    expect(last().query["name"]).toBe("custom.bin");
  });

  it("uploadFile() rejects for a missing path", async () => {
    await expect(hl().files.uploadFile("p", "/no/such/file")).rejects.toThrow();
  });
});

describe("list / all", () => {
  it("list() maps filters to snake_case query parameters", async () => {
    handler = () => ({ json: { files: [file("01B")], next: "01B" } });
    const r = await hl().files.list("posters", {
      tag: "dune", type: "image/", takenAfter: "2024-05-01", takenBefore: "2024-06-01T10:00:00+02:00",
      camera: "canon eos", after: "01C", limit: 10,
    });
    expect(r.next).toBe("01B");
    expect(r.files).toHaveLength(1);
    expect(last().method).toBe("GET");
    expect(last().path).toBe("/api/files/posters");
    expect(last().query).toEqual({
      tag: "dune", type: "image/", taken_after: "2024-05-01", taken_before: "2024-06-01T10:00:00+02:00",
      camera: "canon eos", after: "01C", limit: "10",
    });
  });

  it("list() sends no query without filters", async () => {
    handler = () => ({ json: { files: [], next: null } });
    await hl().files.list("posters");
    expect(last().path).toBe("/api/files/posters");
    expect(requests[0]?.query).toEqual({});
  });

  it("all() follows next as after until null, one page at a time", async () => {
    handler = (r) => {
      const after = r.query["after"];
      if (after === undefined) return { json: { files: [file("03"), file("02")], next: "02" } };
      if (after === "02") return { json: { files: [file("01")], next: "01" } };
      return { json: { files: [], next: null } };
    };
    const ids: string[] = [];
    for await (const f of hl().files.all("posters", { tag: "t", limit: 2 })) ids.push(f.id);
    expect(ids).toEqual(["03", "02", "01"]);
    expect(requests.map((r) => r.query["after"])).toEqual([undefined, "02", "01"]);
    expect(requests.every((r) => r.query["tag"] === "t" && r.query["limit"] === "2")).toBe(true);
  });

  it("all() is lazy and can start from an `after`", async () => {
    handler = () => ({ json: { files: [file("05")], next: "05" } });
    const it = hl().files.all("p", { after: "09" });
    expect((await it.next()).value).toMatchObject({ id: "05" });
    expect(requests).toHaveLength(1);
    expect(requests[0]?.query["after"]).toBe("09");
    await it.return();
  });
});

describe("info / update / download / delete", () => {
  it("info() GETs /info", async () => {
    handler = () => ({ json: file("01A", { meta: { width: 10, future_key: 1 } }) });
    const f = await hl().files.info("posters", "01A");
    expect(f.meta.width).toBe(10);
    expect(f.meta["future_key"]).toBe(1);
    expect(last().method).toBe("GET");
    expect(last().path).toBe("/api/files/posters/01A/info");
  });

  it("update() PATCHes only the given fields as JSON", async () => {
    handler = () => ({ json: file("01A", { tags: ["x"] }) });
    const f = await hl().files.update("posters", "01A", { tags: ["x"] });
    expect(f.tags).toEqual(["x"]);
    expect(last().method).toBe("PATCH");
    expect(last().path).toBe("/api/files/posters/01A/info");
    expect(last().headers["content-type"]).toBe("application/json");
    expect(JSON.parse(last().body.toString())).toEqual({ tags: ["x"] });
    await hl().files.update("posters", "01A", { info: { a: 1 } });
    expect(JSON.parse(last().body.toString())).toEqual({ info: { a: 1 } });
  });

  it("download() returns the Response with the bytes", async () => {
    handler = () => ({ raw: Buffer.from([9, 8, 7]), headers: { "Content-Type": "application/octet-stream" } });
    const res = await hl().files.download("posters", "01A");
    expect(last().method).toBe("GET");
    expect(last().path).toBe("/api/files/posters/01A");
    expect(last().headers["range"]).toBeUndefined();
    expect([...new Uint8Array(await res.arrayBuffer())]).toEqual([9, 8, 7]);
  });

  it("download() streams via .body and passes Range", async () => {
    handler = () => ({ status: 206, raw: Buffer.from([1, 2]), headers: { "Content-Range": "bytes 0-1/10" } });
    const res = await hl().files.download("posters", "01A", { range: "bytes=0-1" });
    expect(last().headers["range"]).toBe("bytes=0-1");
    expect(res.status).toBe(206);
    const chunks: number[] = [];
    for await (const c of res.body as unknown as AsyncIterable<Uint8Array>) chunks.push(...c);
    expect(chunks).toEqual([1, 2]);
  });

  it("delete() DELETEs and returns void", async () => {
    handler = () => ({ status: 204 });
    expect(await hl().files.delete("posters", "01A")).toBeUndefined();
    expect(last().method).toBe("DELETE");
    expect(last().path).toBe("/api/files/posters/01A");
  });
});

describe("encoding", () => {
  it("URL-encodes folder and id in every path", async () => {
    handler = () => ({ status: 204 });
    const c = hl();
    await c.files.delete("a b/c?d", "x#y/z");
    expect(last().path).toBe("/api/files/a%20b%2Fc%3Fd/x%23y%2Fz");
    await c.files.deleteFolder("a/b");
    expect(last().path).toBe("/api/files/folders/a%2Fb");
    handler = () => ({ json: file("1") });
    await c.files.info("é", "ü");
    expect(last().path).toBe("/api/files/%C3%A9/%C3%BC/info");
  });

  it("refuses dot segments instead of letting them climb the path", async () => {
    await expect(hl().files.delete("..", "x")).rejects.toMatchObject({ status: 0 });
    await expect(hl().files.info("p", ".")).rejects.toBeInstanceOf(HomelabError);
    expect(requests).toHaveLength(0);
  });

  it("URL-encodes query values", async () => {
    handler = () => ({ json: { files: [], next: null } });
    await hl().files.list("p", { camera: "a&b=c d#é" });
    expect(last().query["camera"]).toBe("a&b=c d#é");
  });
});

describe("errors", () => {
  it("maps {error} bodies to HomelabError with status and message", async () => {
    handler = () => ({ status: 404, json: { error: "No such folder." } });
    const err = await hl().files.info("p", "x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(HomelabError);
    expect(err).toBeInstanceOf(Error);
    expect(err).toMatchObject({ name: "HomelabError", status: 404, message: "No such folder." });
  });

  it("falls back to a plain message for non-JSON bodies (413)", async () => {
    handler = () => ({ status: 413, raw: "<html>too big</html>" });
    await expect(hl().files.upload("p", new Uint8Array([1]), { name: "a" })).rejects.toMatchObject({
      status: 413,
      message: expect.stringContaining("413"),
    });
  });

  it("maps 401, 403, 503 and 416", async () => {
    for (const [status, error] of [[401, "bad key"], [403, "scope"], [503, "down"], [416, "range"]] as const) {
      handler = () => ({ status, json: { error } });
      await expect(hl().files.download("p", "x")).rejects.toMatchObject({ status, message: error });
    }
  });

  it("does not leak the token in error messages, even if the server echoes it", async () => {
    handler = (r) => ({ status: 401, json: { error: `bad header ${r.headers["authorization"]}` } });
    const err = (await hl().files.folders().catch((e: unknown) => e)) as HomelabError;
    expect(err.message).not.toContain(TOKEN);
    expect(err.stack ?? "").not.toContain(TOKEN);
    expect(JSON.stringify(err)).not.toContain(TOKEN);
  });

  it("network errors have status 0 and no token", async () => {
    const dead = createServer();
    await new Promise<void>((r) => dead.listen(0, "127.0.0.1", r));
    const port = (dead.address() as AddressInfo).port;
    await new Promise<void>((r) => dead.close(() => r()));
    const err = (await new Homelab({ url: `http://127.0.0.1:${port}`, token: TOKEN }).files
      .folders()
      .catch((e: unknown) => e)) as HomelabError;
    expect(err).toBeInstanceOf(HomelabError);
    expect(err.status).toBe(0);
    expect(err.message).not.toContain(TOKEN);
  });

  it("times out normal calls with status 0", async () => {
    const slow = createServer(() => {
      /* never answers */
    });
    await new Promise<void>((r) => slow.listen(0, "127.0.0.1", r));
    const port = (slow.address() as AddressInfo).port;
    try {
      const c = new Homelab({ url: `http://127.0.0.1:${port}`, token: TOKEN, timeoutMs: 100 });
      await expect(c.files.folders()).rejects.toMatchObject({ status: 0, message: expect.stringContaining("timed out") });
    } finally {
      slow.closeAllConnections();
      await new Promise<void>((r) => slow.close(() => r()));
    }
  });
});
