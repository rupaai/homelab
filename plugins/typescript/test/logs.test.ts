import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Homelab, HomelabError } from "../src/index.js";

const TOKEN = "hlk_secret_token_value_0123456789";

interface Recorded {
  method: string;
  path: string;
  query: Record<string, string>;
  headers: IncomingMessage["headers"];
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
    const u = new URL(req.url ?? "/", "http://x");
    const rec: Recorded = {
      method: req.method ?? "",
      path: u.pathname,
      query: Object.fromEntries(u.searchParams),
      headers: req.headers,
    };
    requests.push(rec);
    const c = handler(rec);
    res.writeHead(c.status ?? 200, c.json !== undefined ? { "Content-Type": "application/json" } : {});
    res.end(c.json !== undefined ? JSON.stringify(c.json) : undefined);
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
const line = { id: 42, time: "2026-10-23T22:00:00.123Z", level: "error", msg: "boom", app: "a", env: "prod", service: "app", stream: "stderr", extra: { code: 7 } };
const counts = { debug: 0, info: 3, warn: 1, error: 2, fatal: 0, unknown: 0 };

describe("logs.search", () => {
  it("asks for everything with no options and parses the answer", async () => {
    handler = () => ({ json: { lines: [line], counts, next: "41" } });
    const out = await hl().logs.search();
    expect(last().method).toBe("GET");
    expect(last().path).toBe("/api/logs");
    expect(last().query).toEqual({});
    expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
    expect(out.lines[0]!.msg).toBe("boom");
    expect(out.lines[0]!.extra).toEqual({ code: 7 });
    expect(out.counts.error).toBe(2);
    expect(out.next).toBe("41");
  });

  it("sends every option under the name the server reads", async () => {
    handler = () => ({ json: { lines: [], counts, next: null } });
    await hl().logs.search({
      q: "disk full",
      level: ["error", "fatal"],
      minLevel: "warn",
      app: "a",
      env: "prod",
      service: "app",
      since: new Date("2026-10-23T10:00:00Z"),
      until: 1730000000000,
      before: 41,
      after: "7",
      limit: 50,
    });
    expect(last().query).toEqual({
      q: "disk full",
      level: "error,fatal",
      min_level: "warn",
      app: "a",
      env: "prod",
      service: "app",
      since: "2026-10-23T10:00:00.000Z",
      until: "1730000000000",
      before: "41",
      after: "7",
      limit: "50",
    });
  });

  it("takes a single level", async () => {
    handler = () => ({ json: { lines: [], counts, next: null } });
    await hl().logs.search({ level: "error" });
    expect(last().query).toEqual({ level: "error" });
  });

  it("leaves out empty options", async () => {
    handler = () => ({ json: { lines: [], counts, next: null } });
    await hl().logs.search({ q: "  ", app: "", level: [] });
    expect(last().query).toEqual({});
  });

  it("fills in what the answer leaves out", async () => {
    handler = () => ({ json: {} });
    const out = await hl().logs.search();
    expect(out).toEqual({ lines: [], counts: { unknown: 0, debug: 0, info: 0, warn: 0, error: 0, fatal: 0 }, next: null });
  });

  it("refuses bad options before sending anything", async () => {
    const bad = [
      { limit: 0 },
      { limit: 501 },
      { limit: 1.5 },
      { level: "loud" as never },
      { minLevel: "loud" as never },
      { before: "abc" },
      { after: -1 },
      { since: new Date("nope") },
    ];
    for (const o of bad) await expect(hl().logs.search(o)).rejects.toMatchObject({ status: 0 });
    expect(requests).toHaveLength(0);
  });

  it("turns an error into HomelabError and never shows the token", async () => {
    handler = () => ({ status: 403, json: { error: "This key can't do that. It needs the logs:read scope." } });
    const err = await hl().logs.search().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(HomelabError);
    expect((err as HomelabError).status).toBe(403);
    expect(String((err as HomelabError).message)).toContain("logs:read");
    expect(JSON.stringify(err)).not.toContain(TOKEN);
  });

  it("reports a 404 for another app's logs", async () => {
    handler = () => ({ status: 404, json: { error: "Homelab doesn't know that app." } });
    await expect(hl().logs.search({ app: "other" })).rejects.toMatchObject({ status: 404 });
  });
});
