import { Files } from "./files.js";
import { Transport, type HomelabOptions } from "./http.js";

/**
 * The Homelab client. In Node it reads HOMELAB_API_URL and HOMELAB_TOKEN from the
 * environment; in a browser pass `url` and `token`.
 */
export class Homelab {
  readonly files: Files;
  // Later: readonly db, readonly data (same Transport).

  constructor(options: HomelabOptions = {}) {
    const transport = new Transport(options);
    this.files = new Files(transport);
  }
}

export { HomelabError } from "./errors.js";
export type { HomelabOptions } from "./http.js";
export { Files } from "./files.js";
export type * from "./types.js";
