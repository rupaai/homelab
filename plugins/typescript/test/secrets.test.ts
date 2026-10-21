import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inspect } from "node:util";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Homelab, HomelabError } from "../src/index.js";

const TOKEN = "hlk_secret_token_value_0123456789";
const VALUE = "ya29.super-secret-value";

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
  headers?: Record<string, string>;
}

let server: Server;
let url: string;
let requests: Recorded[] = [];
let handler: (req: Recorded) => Canned = () => ({ status: 200, json: {} });
let dir: string;

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
      res.writeHead(c.status ?? 200, {
        ...(c.json !== undefined ? { "Content-Type": "application/json" } : {}),
        ...c.headers,
      });
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
  dir = mkdtempSync(join(tmpdir(), "hl-secrets-"));
});

const hl = () => new Homelab({ url, token: TOKEN, secretsDir: dir });
const last = () => requests[requests.length - 1]!;

describe("secrets calls", () => {
  it("list: GET /api/secrets", async () => {
    const info = { name: "a", kind: "app", version: 2, updated_at: 5 };
    handler = () => ({ json: { secrets: [info] } });
    expect(await hl().secrets.list()).toEqual([info]);
    expect([last().method, last().path]).toEqual(["GET", "/api/secrets"]);
    expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
  });

  it("get (fresh): GET /api/secrets/{name} and returns the value", async () => {
    handler = () => ({ json: { name: "tok", value: VALUE, version: 3 } });
    expect(await hl().secrets.get("tok", { fresh: true })).toBe(VALUE);
    expect([last().method, last().path, last().query]).toEqual(["GET", "/api/secrets/tok", {}]);
  });

  it("get with a version asks Homelab with ?version=", async () => {
    handler = () => ({ json: { name: "tok", value: "old", version: 2 } });
    expect(await hl().secrets.get("tok", { version: 2 })).toBe("old");
    expect(last().path).toBe("/api/secrets/tok");
    expect(last().query).toEqual({ version: "2" });
  });

  it("set: PUT with a JSON body", async () => {
    handler = () => ({ json: { name: "tok", version: 4 } });
    expect(await hl().secrets.set("tok", VALUE)).toEqual({ name: "tok", version: 4 });
    expect([last().method, last().path]).toEqual(["PUT", "/api/secrets/tok"]);
    expect(last().headers["content-type"]).toBe("application/json");
    expect(JSON.parse(last().body)).toEqual({ value: VALUE });
  });

  it("delete: DELETE, 204", async () => {
    handler = () => ({ status: 204 });
    await hl().secrets.delete("tok");
    expect([last().method, last().path]).toEqual(["DELETE", "/api/secrets/tok"]);
  });

  it("versions: GET /api/secrets/{name}/versions", async () => {
    handler = () => ({ json: { versions: [{ version: 2, set_at: 9 }, { version: 1, set_at: 3 }] } });
    expect(await hl().secrets.versions("tok")).toEqual([
      { version: 2, set_at: 9 },
      { version: 1, set_at: 3 },
    ]);
    expect(last().path).toBe("/api/secrets/tok/versions");
  });

  it("encodes ':' and keeps '.' and '-' in the path", async () => {
    handler = () => ({ json: { name: "x", value: "v", version: 1 } });
    await hl().secrets.get("google_token:user.42-a", { fresh: true });
    expect(last().path).toBe("/api/secrets/google_token%3Auser.42-a");
  });
});

describe("file first", () => {
  it("returns the file contents without calling Homelab", async () => {
    writeFileSync(join(dir, "API_KEY"), "from-file");
    expect(await hl().secrets.get("API_KEY")).toBe("from-file");
    expect(requests).toHaveLength(0);
  });

  it("falls back to the API when there is no file", async () => {
    handler = () => ({ json: { name: "API_KEY", value: "from-api", version: 1 } });
    expect(await hl().secrets.get("API_KEY")).toBe("from-api");
    expect(requests).toHaveLength(1);
  });

  it("fresh: true skips the file", async () => {
    writeFileSync(join(dir, "API_KEY"), "from-file");
    handler = () => ({ json: { name: "API_KEY", value: "from-api", version: 2 } });
    expect(await hl().secrets.get("API_KEY", { fresh: true })).toBe("from-api");
  });

  it("a version skips the file", async () => {
    writeFileSync(join(dir, "API_KEY"), "from-file");
    handler = () => ({ json: { name: "API_KEY", value: "v1", version: 1 } });
    expect(await hl().secrets.get("API_KEY", { version: 1 })).toBe("v1");
  });

  it("names that are not plain identifiers never touch the disk", async () => {
    writeFileSync(join(dir, "a.b"), "from-file");
    writeFileSync(join(dir, "a:b"), "from-file");
    handler = () => ({ json: { name: "x", value: "from-api", version: 1 } });
    expect(await hl().secrets.get("a.b")).toBe("from-api");
    expect(await hl().secrets.get("a:b")).toBe("from-api");
    expect(await hl().secrets.get("1abc")).toBe("from-api");
    expect(requests).toHaveLength(3);
  });

  it("refuses names with / . .. before touching disk or network", async () => {
    for (const bad of ["../etc/passwd", "a/b", ".", "..", "", "a b", "x".repeat(201), "a\\b", "é"]) {
      await expect(hl().secrets.get(bad)).rejects.toMatchObject({ status: 0 });
    }
    expect(requests).toHaveLength(0);
  });

  it("defaults the directory to /run/secrets", async () => {
    handler = () => ({ json: { name: "NOPE_NOT_THERE", value: "api", version: 1 } });
    const c = new Homelab({ url, token: TOKEN });
    expect(await c.secrets.get("NOPE_NOT_THERE")).toBe("api");
  });
});

describe("404 and errors", () => {
  it("get throws HomelabError 404", async () => {
    handler = () => ({ status: 404, json: { error: "There's no such secret." } });
    await expect(hl().secrets.get("nope", { fresh: true })).rejects.toMatchObject({
      status: 404,
      message: "There's no such secret.",
    });
  });

  it("getOrNull returns null on 404 but throws other errors", async () => {
    handler = () => ({ status: 404, json: { error: "There's no such secret." } });
    expect(await hl().secrets.getOrNull("nope")).toBeNull();
    handler = () => ({ status: 403, json: { error: "This key can't do that." } });
    await expect(hl().secrets.getOrNull("nope")).rejects.toMatchObject({ status: 403 });
  });

  it("getOrNull still validates the name", async () => {
    await expect(hl().secrets.getOrNull("../x")).rejects.toBeInstanceOf(HomelabError);
  });

  it("set, delete and versions validate names client-side", async () => {
    const c = hl();
    await expect(c.secrets.set("a/b", "v")).rejects.toMatchObject({ status: 0 });
    await expect(c.secrets.set("..", "v")).rejects.toMatchObject({ status: 0 });
    await expect(c.secrets.delete("a b")).rejects.toMatchObject({ status: 0 });
    await expect(c.secrets.versions(".")).rejects.toMatchObject({ status: 0 });
    await expect(c.secrets.get("ok", { version: 0 })).rejects.toMatchObject({ status: 0 });
    expect(requests).toHaveLength(0);
  });

  it("maps 403 and 503", async () => {
    handler = () => ({ status: 403, json: { error: "It needs the secrets:write scope." } });
    await expect(hl().secrets.set("a", "v")).rejects.toMatchObject({ status: 403 });
    handler = () => ({ status: 503, json: { error: "The secrets service isn't running right now." } });
    await expect(hl().secrets.list()).rejects.toMatchObject({ status: 503 });
  });
});

describe("no leaks", () => {
  it("a failed set never has the value in the error, even if the server echoes it", async () => {
    handler = (r) => ({ status: 400, json: { error: `bad value ${JSON.parse(r.body).value}` } });
    const err = (await hl().secrets.set("a", VALUE).catch((e: unknown) => e)) as HomelabError;
    expect(err).toBeInstanceOf(HomelabError);
    for (const text of [err.message, err.stack ?? "", JSON.stringify(err), inspect(err)]) {
      expect(text).not.toContain(VALUE);
      expect(text).not.toContain(TOKEN);
    }
  });

  it("the client, secrets and results do not print values or the token", async () => {
    handler = () => ({ json: { name: "a", version: 1 } });
    const c = hl();
    const saved = await c.secrets.set("a", VALUE);
    for (const text of [JSON.stringify(c), inspect(c, { depth: 5 }), JSON.stringify(c.secrets), inspect(c.secrets), JSON.stringify(saved)]) {
      expect(text).not.toContain(VALUE);
      expect(text).not.toContain(TOKEN);
    }
  });

  it("list and versions carry no values", async () => {
    handler = () => ({ json: { secrets: [{ name: "a", kind: "app", version: 1, updated_at: 1 }] } });
    expect(JSON.stringify(await hl().secrets.list())).not.toContain("value");
  });

  it("the token is not in errors, even if the server echoes the header", async () => {
    handler = (r) => ({ status: 401, json: { error: `bad ${r.headers["authorization"]}` } });
    const err = (await hl().secrets.list().catch((e: unknown) => e)) as HomelabError;
    expect(err.message).not.toContain(TOKEN);
  });

  it("does not follow redirects", async () => {
    handler = () => ({ status: 302, headers: { Location: "http://127.0.0.1:1/steal" } });
    await expect(hl().secrets.get("a", { fresh: true })).rejects.toMatchObject({ status: 302 });
    expect(requests).toHaveLength(1);
  });
});
