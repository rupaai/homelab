import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Homelab, HomelabError, version } from "../src/index.js";

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
afterEach(() => {
  delete process.env["HOMELAB_VERSION"];
});

const hl = () => new Homelab({ url, token: TOKEN });
const last = () => requests[requests.length - 1]!;
const release = {
  tag: "v0.4.0",
  prerelease: false,
  commit: "9f3c2ab0",
  date: "2026-10-01T10:00:00Z",
  summary: "2 new, 1 fix",
  notes: "## New\n\n- a\n",
};

describe("releases", () => {
  it("lists the app's own releases", async () => {
    handler = () => ({ json: { releases: [release, { ...release, tag: "v0.4.0-dev.2", prerelease: true }] } });
    const out = await hl().releases.list();
    expect(out.map((r) => r.tag)).toEqual(["v0.4.0", "v0.4.0-dev.2"]);
    expect(out[1]!.prerelease).toBe(true);
    expect(out[0]!.summary).toBe("2 new, 1 fix");
    expect(last().method).toBe("GET");
    expect(last().path).toBe("/api/releases");
    expect(last().query).toEqual({});
    expect(last().headers["authorization"]).toBe(`Bearer ${TOKEN}`);
  });

  it("sends a limit", async () => {
    handler = () => ({ json: { releases: [release] } });
    await hl().releases.list({ limit: 5 });
    expect(last().query).toEqual({ limit: "5" });
  });

  it("refuses a bad limit before sending anything", async () => {
    for (const limit of [0, -1, 1.5, Number.NaN]) {
      await expect(hl().releases.list({ limit })).rejects.toMatchObject({ status: 0 });
    }
    expect(requests).toHaveLength(0);
  });

  it("turns an error into HomelabError and never shows the token", async () => {
    handler = () => ({ status: 403, json: { error: "This key can't do that. It needs the releases:read scope." } });
    const err = await hl().releases.list().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(HomelabError);
    expect((err as HomelabError).status).toBe(403);
    expect(String((err as HomelabError).message)).toContain("releases:read");
    expect(JSON.stringify(err)).not.toContain(TOKEN);
  });
});

describe("version", () => {
  it("is HOMELAB_VERSION", () => {
    process.env["HOMELAB_VERSION"] = "v0.4.0-dev.2";
    expect(hl().version()).toBe("v0.4.0-dev.2");
    expect(version()).toBe("v0.4.0-dev.2");
  });

  it("is null when it is missing or empty", () => {
    expect(hl().version()).toBeNull();
    process.env["HOMELAB_VERSION"] = "  ";
    expect(version()).toBeNull();
  });
});
