import { Files } from "./files.js";
import { Db } from "./db.js";
import { Secrets } from "./secrets.js";
import { Transport, type HomelabOptions } from "./http.js";

/**
 * The Homelab client. In Node it reads HOMELAB_API_URL and HOMELAB_TOKEN from the
 * environment; in a browser pass `url` and `token`.
 */
export class Homelab {
  readonly files: Files;
  readonly secrets: Secrets;
  readonly db: Db;
  // Later: readonly data (same Transport).

  constructor(options: HomelabOptions = {}) {
    const transport = new Transport(options);
    this.files = new Files(transport);
    this.secrets = new Secrets(transport, options.secretsDir);
    this.db = new Db(transport);
  }
}

export { HomelabError } from "./errors.js";
export type { HomelabOptions } from "./http.js";
export { Files } from "./files.js";
export { Db } from "./db.js";
export { Secrets, DEFAULT_SECRETS_DIR } from "./secrets.js";
export type * from "./types.js";
