import { HomelabError } from "./errors.js";

export interface HomelabOptions {
  /** Defaults to env HOMELAB_API_URL. */
  url?: string;
  /** Defaults to env HOMELAB_TOKEN. */
  token?: string;
  /** Timeout for normal calls in ms (default 60000). Upload and download bodies have none. */
  timeoutMs?: number;
  /** Custom fetch (for tests or special runtimes). Defaults to global fetch. */
  fetch?: typeof fetch;
}

export interface RequestOptions {
  method?: string;
  query?: Array<[string, string]>;
  headers?: Record<string, string>;
  /** A JSON-serialisable value, sent as application/json. */
  json?: unknown;
  /** A raw body. */
  body?: BodyInit;
  /** No timeout (uploads and downloads). */
  noTimeout?: boolean;
}

function env(name: string): string | undefined {
  const p = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process;
  const v = p?.env?.[name];
  return v ? v : undefined;
}

/** Encode one path segment. "." and ".." are refused: URL parsing would resolve them and change the path. */
export function seg(value: string): string {
  if (value === "." || value === "..") throw new HomelabError(0, `Invalid path segment: "${value}"`);
  return encodeURIComponent(value);
}

/** Shared HTTP core. The token lives in a private field, so it never shows up in logs or JSON. */
export class Transport {
  readonly #token: string;
  readonly #base: string;
  readonly #timeoutMs: number;
  readonly #fetch: typeof fetch;

  constructor(opts: HomelabOptions = {}) {
    const url = opts.url ?? env("HOMELAB_API_URL");
    const token = opts.token ?? env("HOMELAB_TOKEN");
    if (!url) throw new HomelabError(0, "Set HOMELAB_API_URL or pass url");
    if (!token) throw new HomelabError(0, "Set HOMELAB_TOKEN or pass token");
    this.#base = url.replace(/\/+$/, "");
    this.#token = token;
    this.#timeoutMs = opts.timeoutMs ?? 60_000;
    this.#fetch = opts.fetch ?? ((...a) => globalThis.fetch(...a));
  }

  toJSON(): object {
    return { url: this.#base };
  }

  #redact(text: string): string {
    return text.split(this.#token).join("[redacted]");
  }

  /** Send a request. Resolves with the Response on 2xx, throws HomelabError otherwise. */
  async request(path: string, o: RequestOptions = {}): Promise<Response> {
    let url = this.#base + path;
    if (o.query && o.query.length > 0) {
      url += "?" + o.query.map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`).join("&");
    }
    const headers: Record<string, string> = { ...o.headers, Authorization: `Bearer ${this.#token}` };
    // Never follow redirects: the token must not travel to another place.
    const init: RequestInit & { duplex?: "half" } = { method: o.method ?? "GET", headers, redirect: "manual" };
    if (o.json !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(o.json);
    } else if (o.body !== undefined) {
      init.body = o.body;
      if (typeof ReadableStream !== "undefined" && o.body instanceof ReadableStream) init.duplex = "half";
    }
    if (!o.noTimeout && this.#timeoutMs > 0) init.signal = AbortSignal.timeout(this.#timeoutMs);

    let res: Response;
    try {
      res = await this.#fetch(url, init);
    } catch (err) {
      const e = err as { name?: string; message?: string; cause?: { message?: string } };
      if (e?.name === "TimeoutError" || e?.name === "AbortError") {
        throw new HomelabError(0, `Request timed out after ${this.#timeoutMs} ms`);
      }
      const detail = e?.cause?.message ?? e?.message ?? "request failed";
      throw new HomelabError(0, this.#redact(`Network error: ${detail}`));
    }
    if (res.ok) return res;

    let message = "";
    try {
      const text = await res.text();
      try {
        const body: unknown = JSON.parse(text);
        if (body && typeof body === "object" && typeof (body as { error?: unknown }).error === "string") {
          message = (body as { error: string }).error;
        }
      } catch {
        /* not JSON */
      }
    } catch {
      /* unreadable body */
    }
    throw new HomelabError(res.status, this.#redact(message || `HTTP ${res.status}${res.statusText ? " " + res.statusText : ""}`));
  }

  async json<T>(path: string, o: RequestOptions = {}): Promise<T> {
    const res = await this.request(path, o);
    return (await res.json()) as T;
  }
}
