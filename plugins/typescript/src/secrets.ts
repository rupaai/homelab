import { HomelabError } from "./errors.js";
import { seg, type Transport } from "./http.js";
import type { GetSecretOptions, SecretInfo, SecretSaved, SecretVersion } from "./types.js";

/** Where Homelab puts config secrets as files inside an app. */
export const DEFAULT_SECRETS_DIR = "/run/secrets";

// The same rule as the server: 1 to 200 of letters, digits and _ . : - (never "." or "..").
const APP_NAME = /^[A-Za-z0-9_.:-]{1,200}$/;
// Only names that are safe file names are looked up on disk.
const FILE_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

function checkName(name: string): void {
  if (typeof name !== "string" || name === "." || name === ".." || !APP_NAME.test(name)) {
    throw new HomelabError(0, "A secret name is 1 to 200 letters, digits and _ . : -");
  }
}

function checkVersion(version: number): void {
  if (!Number.isInteger(version) || version < 1) {
    throw new HomelabError(0, "The version is a number from 1.");
  }
}

/** `homelab.secrets`: thin wrappers over /api/secrets. Values are never logged or put in errors. */
export class Secrets {
  readonly #t: Transport;
  readonly #dir: string;

  constructor(transport: Transport, secretsDir: string = DEFAULT_SECRETS_DIR) {
    this.#t = transport;
    this.#dir = secretsDir;
  }

  toJSON(): object {
    return { dir: this.#dir };
  }

  /**
   * The value of a secret. Without `version`, a file `<secretsDir>/<name>` is used when it
   * exists (Node only; the contents are returned exactly as written). Otherwise, or with
   * `fresh: true`, it asks Homelab: GET /api/secrets/{name}[?version=n].
   * A missing secret throws `HomelabError` with status 404; see `getOrNull`.
   */
  async get(name: string, opts: GetSecretOptions = {}): Promise<string> {
    checkName(name);
    if (opts.version !== undefined) checkVersion(opts.version);
    if (opts.version === undefined && !opts.fresh) {
      const fromFile = await this.#readFile(name);
      if (fromFile !== null) return fromFile;
    }
    const query: Array<[string, string]> = opts.version !== undefined ? [["version", String(opts.version)]] : [];
    const body = await this.#t.json<{ value: string }>(`/api/secrets/${seg(name)}`, { query });
    return body.value;
  }

  /** Like `get`, but returns `null` when there is no such secret (404). Other errors still throw. */
  async getOrNull(name: string, opts: GetSecretOptions = {}): Promise<string | null> {
    try {
      return await this.get(name, opts);
    } catch (err) {
      if (err instanceof HomelabError && err.status === 404) return null;
      throw err;
    }
  }

  /** PUT /api/secrets/{name}. Saves the value as the next version. Needs the `secrets:write` scope. */
  async set(name: string, value: string): Promise<SecretSaved> {
    checkName(name);
    try {
      return await this.#t.json<SecretSaved>(`/api/secrets/${seg(name)}`, { method: "PUT", json: { value } });
    } catch (err) {
      // The server never echoes a value, but make sure nothing we sent can come back in an error.
      if (err instanceof HomelabError && value !== "" && err.message.includes(value)) {
        throw new HomelabError(err.status, err.message.split(value).join("[redacted]"));
      }
      throw err;
    }
  }

  /** DELETE /api/secrets/{name}. Deleting a secret that is not there is fine. */
  async delete(name: string): Promise<void> {
    checkName(name);
    await this.#t.request(`/api/secrets/${seg(name)}`, { method: "DELETE" });
  }

  /** GET /api/secrets: names, kinds and versions. No values. */
  async list(): Promise<SecretInfo[]> {
    return (await this.#t.json<{ secrets: SecretInfo[] }>("/api/secrets")).secrets;
  }

  /** GET /api/secrets/{name}/versions: the kept versions, newest first. No values. 404 if unknown. */
  async versions(name: string): Promise<SecretVersion[]> {
    checkName(name);
    return (await this.#t.json<{ versions: SecretVersion[] }>(`/api/secrets/${seg(name)}/versions`)).versions;
  }

  /** The file's contents, or null when there is no such file (or no file system). */
  async #readFile(name: string): Promise<string | null> {
    if (!FILE_NAME.test(name)) return null;
    let fs: typeof import("node:fs");
    let path: typeof import("node:path");
    try {
      [fs, path] = await Promise.all([import("node:fs"), import("node:path")]);
    } catch {
      return null; // not Node (a browser)
    }
    try {
      return await fs.promises.readFile(path.join(this.#dir, name), "utf8");
    } catch (err) {
      const code = (err as { code?: string }).code;
      if (code === "ENOENT" || code === "ENOTDIR" || code === "EISDIR") return null;
      // Do not pass the OS message on; it is only a path, but keep errors short and plain.
      throw new HomelabError(0, `Could not read the secret file for "${name}" (${code ?? "error"})`);
    }
  }
}
