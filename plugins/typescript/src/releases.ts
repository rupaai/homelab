import { HomelabError } from "./errors.js";
import type { Transport } from "./http.js";
import type { Release, ReleasesOptions } from "./types.js";

/** The version of this running app, from the `HOMELAB_VERSION` setting Homelab gives it. `null` when there is none. */
export function version(): string | null {
  const v = typeof process !== "undefined" ? process.env?.["HOMELAB_VERSION"] : undefined;
  return v !== undefined && v.trim() !== "" ? v.trim() : null;
}

/** `homelab.releases`: the app's own versions, from GET /api/releases. */
export class Releases {
  readonly #t: Transport;

  constructor(transport: Transport) {
    this.#t = transport;
  }

  /** The app's releases, newest first. Dev builds are pre-releases (`prerelease: true`). */
  async list(opts: ReleasesOptions = {}): Promise<Release[]> {
    const query: Array<[string, string]> = [];
    if (opts.limit !== undefined) {
      if (!Number.isInteger(opts.limit) || opts.limit < 1) {
        throw new HomelabError(0, "The limit must be a whole number from 1.");
      }
      query.push(["limit", String(opts.limit)]);
    }
    return (await this.#t.json<{ releases: Release[] }>("/api/releases", { query })).releases;
  }
}
