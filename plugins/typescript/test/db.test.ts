import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Homelab } from "../src/index.js";

const TOKEN = "hlk_secret_token_value_0123456789";

interface Recorded {
  method: string;
  path: string;
  query: Record<string, string>;
  headers: IncomingMessage["headers"];
  body: string;
}
interface Canned {
  status?: number;
  json?: unknown;
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
      const rec: Recorded = {
        method: req.method ?? "",
        path: (req.url ?? "/").split("?")[0] ?? "/",
        query: Object.fromEntries(u.searchParams),
        headers: req.headers,
        body: Buffer.concat(chunks).toString("utf8"),
      };
      requests.push(rec);
      const c = handler(rec);
      res.writeHead(c.status ?? 200, c.json !== undefined ? { "Content-Type": "application/json" } : {});
      res.end(c.json !== undefined ? JSON.stringify(c.json) : undefined);
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
afterEach(() => {
  delete process.env["HOMELAB_APP"];
  delete process.env["HOMELAB_ENV"];
});

const hl = () => new Homelab({ url, token: TOKEN });
const last = () => requests[requests.length - 1]!;
const info = { app: "shop", env: "prod", engine: "postgres", status: "up", size_bytes: 10, connections: 2, tables: 3 };

describe("db calls", () => {
  it("list: GET /api/db", async () => {
    handler = () => ({ json: { databases: [info] } });
    expect(await hl().db.list()).toEqual([info]);
    expect([last().method, last().path]).toEqual(["GET", "/api/db"]);
    expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
  });

  it("info: GET /api/db/{app}/{env}", async () => {
    handler = () => ({ json: info });
    expect(await hl().db.info("shop", "prod")).toEqual(info);
    expect([last().method, last().path]).toEqual(["GET", "/api/db/shop/prod"]);
  });

  it("tables: GET /api/db/{app}/{env}/tables", async () => {
    const t = { schema: "public", name: "orders", rows: 7 };
    handler = () => ({ json: { tables: [t] } });
    expect(await hl().db.tables("shop", "prod")).toEqual([t]);
    expect(last().path).toBe("/api/db/shop/prod/tables");
  });

  it("rows: GET with schema, limit and offset, table encoded", async () => {
    const out = { columns: ["id"], rows: [[1]], truncated: false };
    handler = () => ({ json: out });
    expect(await hl().db.rows("shop", "prod", "my table", { schema: "app", limit: 20, offset: 40 })).toEqual(out);
    expect(last().path).toBe("/api/db/shop/prod/tables/my%20table/rows");
    expect(last().query).toEqual({ schema: "app", limit: "20", offset: "40" });
  });

  it("rows: no query when no options", async () => {
    handler = () => ({ json: { columns: [], rows: [], truncated: false } });
    await hl().db.rows("shop", "prod", "orders");
    expect(last().query).toEqual({});
  });

  it("rows: bad limit or offset is refused before sending", async () => {
    await expect(hl().db.rows("shop", "prod", "t", { limit: 0 })).rejects.toMatchObject({ status: 0 });
    await expect(hl().db.rows("shop", "prod", "t", { limit: 201 })).rejects.toMatchObject({ status: 0 });
    await expect(hl().db.rows("shop", "prod", "t", { offset: -1 })).rejects.toMatchObject({ status: 0 });
    expect(requests).toHaveLength(0);
  });

  it("query: POST {sql} and never write", async () => {
    const out = { columns: ["n"], rows: [[1]], row_count: 1, truncated: false, command: "SELECT", ms: 3 };
    handler = () => ({ json: out });
    expect(await hl().db.query("shop", "prod", "select 1 as n")).toEqual(out);
    expect([last().method, last().path]).toEqual(["POST", "/api/db/shop/prod/query"]);
    expect(last().headers["content-type"]).toBe("application/json");
    expect(JSON.parse(last().body)).toEqual({ sql: "select 1 as n" });
    expect(last().body).not.toContain("write");
  });
});

describe("app and env defaults", () => {
  it("come from HOMELAB_APP and HOMELAB_ENV", async () => {
    process.env["HOMELAB_APP"] = "blog";
    process.env["HOMELAB_ENV"] = "dev";
    handler = () => ({ json: { tables: [] } });
    await hl().db.tables();
    expect(last().path).toBe("/api/db/blog/dev/tables");
    await hl().db.query(undefined, undefined, "select 1");
    expect(last().path).toBe("/api/db/blog/dev/query");
    await hl().db.rows(undefined, undefined, "posts");
    expect(last().path).toBe("/api/db/blog/dev/tables/posts/rows");
  });

  it("explicit values win over the environment", async () => {
    process.env["HOMELAB_APP"] = "blog";
    process.env["HOMELAB_ENV"] = "dev";
    handler = () => ({ json: info });
    await hl().db.info("shop", "prod");
    expect(last().path).toBe("/api/db/shop/prod");
  });

  it("missing both is a clear error", async () => {
    await expect(hl().db.info()).rejects.toMatchObject({ status: 0, message: "Set HOMELAB_APP or pass app" });
    process.env["HOMELAB_APP"] = "blog";
    await expect(hl().db.info()).rejects.toMatchObject({ status: 0, message: "Set HOMELAB_ENV or pass env" });
    expect(requests).toHaveLength(0);
  });
});

describe("errors and safety", () => {
  it("refuses . and .. in app, env and table", async () => {
    const c = hl();
    await expect(c.db.info(".", "prod")).rejects.toMatchObject({ status: 0 });
    await expect(c.db.tables("shop", "..")).rejects.toMatchObject({ status: 0 });
    await expect(c.db.rows("shop", "prod", "..")).rejects.toMatchObject({ status: 0 });
    await expect(c.db.query("..", "prod", "select 1")).rejects.toMatchObject({ status: 0 });
    expect(requests).toHaveLength(0);
  });

  it("maps 404 and 403 to HomelabError", async () => {
    handler = () => ({ status: 404, json: { error: "There's no such database." } });
    await expect(hl().db.info("x", "dev")).rejects.toMatchObject({ status: 404, message: "There's no such database." });
    handler = () => ({ status: 403, json: { error: "This key can't do that. It needs the db:read scope." } });
    await expect(hl().db.list()).rejects.toMatchObject({ status: 403 });
  });

  it("does not leak the token", async () => {
    handler = (r) => ({ status: 401, json: { error: `bad ${r.headers["authorization"]}` } });
    const err = (await hl().db.list().catch((e: unknown) => e)) as Error;
    expect(err.message).not.toContain(TOKEN);
  });
});
