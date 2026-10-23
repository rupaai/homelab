import { HomelabError } from "./errors.js";
import { env, seg, type Transport } from "./http.js";
import type { DbInfo, DbQueryResult, DbRows, DbRowsOptions, DbTable } from "./types.js";

/** The app and environment: as given, or from HOMELAB_APP and HOMELAB_ENV (set in every Homelab app). */
function place(app: string | undefined, envName: string | undefined): [string, string] {
  const a = app ?? env("HOMELAB_APP");
  const e = envName ?? env("HOMELAB_ENV");
  if (!a) throw new HomelabError(0, "Set HOMELAB_APP or pass app");
  if (!e) throw new HomelabError(0, "Set HOMELAB_ENV or pass env");
  return [a, e];
}

function checkWhole(name: string, value: number, min: number, max?: number): void {
  if (!Number.isInteger(value) || value < min || (max !== undefined && value > max)) {
    throw new HomelabError(0, `The ${name} must be a whole number from ${min}${max !== undefined ? ` to ${max}` : ""}.`);
  }
}

/**
 * `homelab.db`: thin wrappers over /api/db, for looking inside a database. Apps connect
 * with DATABASE_URL and any normal driver. Read-only: writing is for people in the companion.
 * `app` and `env` default to HOMELAB_APP and HOMELAB_ENV; pass `undefined` to use them.
 */
export class Db {
  readonly #t: Transport;

  constructor(transport: Transport) {
    this.#t = transport;
  }

  /** GET /api/db: the databases this key can see. An app key sees only its own. */
  async list(): Promise<DbInfo[]> {
    return (await this.#t.json<{ databases: DbInfo[] }>("/api/db")).databases;
  }

  /** GET /api/db/{app}/{env} */
  async info(app?: string, envName?: string): Promise<DbInfo> {
    const [a, e] = place(app, envName);
    return this.#t.json<DbInfo>(`/api/db/${seg(a)}/${seg(e)}`);
  }

  /** GET /api/db/{app}/{env}/tables */
  async tables(app?: string, envName?: string): Promise<DbTable[]> {
    const [a, e] = place(app, envName);
    return (await this.#t.json<{ tables: DbTable[] }>(`/api/db/${seg(a)}/${seg(e)}/tables`)).tables;
  }

  /** GET /api/db/{app}/{env}/tables/{table}/rows: one page of a table. */
  async rows(app: string | undefined, envName: string | undefined, table: string, opts: DbRowsOptions = {}): Promise<DbRows> {
    const [a, e] = place(app, envName);
    const path = `/api/db/${seg(a)}/${seg(e)}/tables/${seg(table)}/rows`;
    if (opts.limit !== undefined) checkWhole("limit", opts.limit, 1, 200);
    if (opts.offset !== undefined) checkWhole("offset", opts.offset, 0);
    const query: Array<[string, string]> = [];
    if (opts.schema !== undefined) query.push(["schema", opts.schema]);
    if (opts.limit !== undefined) query.push(["limit", String(opts.limit)]);
    if (opts.offset !== undefined) query.push(["offset", String(opts.offset)]);
    return this.#t.json<DbRows>(path, { query });
  }

  /** POST /api/db/{app}/{env}/query: read-only SQL. Never sends `write`. */
  async query(app: string | undefined, envName: string | undefined, sql: string): Promise<DbQueryResult> {
    const [a, e] = place(app, envName);
    return this.#t.json<DbQueryResult>(`/api/db/${seg(a)}/${seg(e)}/query`, { method: "POST", json: { sql } });
  }
}
