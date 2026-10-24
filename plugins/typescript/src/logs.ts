import { HomelabError } from "./errors.js";
import type { Transport } from "./http.js";
import type { LogLevel, LogSearchOptions, LogSearchResult } from "./types.js";

const LEVELS: readonly string[] = ["unknown", "debug", "info", "warn", "error", "fatal"];

function level(value: string): string {
  if (!LEVELS.includes(value)) {
    throw new HomelabError(0, `Unknown log level "${value}". Use one of: ${LEVELS.join(", ")}.`);
  }
  return value;
}

function time(name: string, value: Date | string | number): string {
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) throw new HomelabError(0, `${name} is not a valid time.`);
    return value.toISOString();
  }
  return String(value);
}

function id(name: string, value: number | string): string {
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isInteger(n) || n < 0) throw new HomelabError(0, `${name} must be a line id (a whole number).`);
  return String(n);
}

/** `homelab.logs`: search the logs, from GET /api/logs. An app key sees its own app and environment only. */
export class Logs {
  readonly #t: Transport;

  constructor(transport: Transport) {
    this.#t = transport;
  }

  /** Searches the logs. Newest lines first. */
  async search(opts: LogSearchOptions = {}): Promise<LogSearchResult> {
    const query: Array<[string, string]> = [];
    if (opts.q !== undefined && opts.q.trim() !== "") query.push(["q", opts.q]);
    for (const key of ["app", "env", "service"] as const) {
      const v = opts[key];
      if (v !== undefined && v !== "") query.push([key, v]);
    }
    if (opts.level !== undefined) {
      const levels = (Array.isArray(opts.level) ? opts.level : [opts.level]).map(level);
      if (levels.length > 0) query.push(["level", levels.join(",")]);
    }
    if (opts.minLevel !== undefined) query.push(["min_level", level(opts.minLevel)]);
    if (opts.since !== undefined) query.push(["since", time("since", opts.since)]);
    if (opts.until !== undefined) query.push(["until", time("until", opts.until)]);
    if (opts.before !== undefined) query.push(["before", id("before", opts.before)]);
    if (opts.after !== undefined) query.push(["after", id("after", opts.after)]);
    if (opts.limit !== undefined) {
      if (!Number.isInteger(opts.limit) || opts.limit < 1 || opts.limit > 500) {
        throw new HomelabError(0, "The limit must be a whole number from 1 to 500.");
      }
      query.push(["limit", String(opts.limit)]);
    }
    const r = await this.#t.json<Partial<LogSearchResult>>("/api/logs", { query });
    const counts = { unknown: 0, debug: 0, info: 0, warn: 0, error: 0, fatal: 0, ...r.counts } as Record<LogLevel, number>;
    return { lines: r.lines ?? [], counts, next: r.next ?? null };
  }
}
