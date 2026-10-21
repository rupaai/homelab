# @homelab/sdk (TypeScript)

Thin TypeScript client for the Homelab API. Today: Files (`homelab.files`) and Secrets
(`homelab.secrets`). Databases and data (`homelab.db`, `homelab.data`) will be added next to them.

Zero runtime dependencies. Uses the global `fetch` (Node 18+ and browsers). ESM only.

## Install

Publishing to npm comes later. For now install from a checkout:

```sh
npm install /path/to/homelab/plugins/typescript
# or from a git checkout of the repo:
git clone <repo> && cd homelab/plugins/typescript && npm ci && npm run build
```

## Quick start

```ts
import { Homelab } from "@homelab/sdk";

const homelab = new Homelab(); // reads HOMELAB_API_URL and HOMELAB_TOKEN
await homelab.files.ensureFolder("posters");
const { file, duplicate } = await homelab.files.upload("posters", bytes, {
  name: "dune.jpg",
  tags: ["dune", "poster"],
});
console.log(file.id, file.meta.width, duplicate);
for await (const f of homelab.files.all("posters", { tag: "dune" })) console.log(f.name);

await homelab.secrets.set("google_token:user42", token); // needs secrets_api: write
const saved = await homelab.secrets.getOrNull("google_token:user42"); // null if missing
const apiKey = await homelab.secrets.get("STRIPE_KEY"); // reads /run/secrets/STRIPE_KEY first
```

In a browser there is no environment: `new Homelab({ url, token })`. Do not ship an
admin token to a browser.

Options: `url`, `token`, `timeoutMs` (default 60000; uploads and downloads have no
timeout), `secretsDir` (default `/run/secrets`), `fetch` (custom implementation). The token is kept in a private field and never
appears in errors, `JSON.stringify` or `console.log`.

## Methods

Names with a path part (`folder`, `id`) are URL-encoded for you. `"."` and `".."` are
refused with a `HomelabError` (status 0), because URL parsing would rewrite the path.

### `files.folders(): Promise<Folder[]>`
```ts
const folders = await homelab.files.folders();
```

### `files.createFolder(name, { disk? }): Promise<Folder>`
```ts
const f = await homelab.files.createFolder("posters", { disk: "big" });
```

### `files.ensureFolder(name, { disk? }): Promise<Folder>`
Creates the folder; if it already exists (409) returns the existing one.
```ts
const f = await homelab.files.ensureFolder("posters");
```

### `files.deleteFolder(name): Promise<void>`
The folder must have no files (otherwise 409).
```ts
await homelab.files.deleteFolder("posters");
```

### `files.upload(folder, data, { name, tags?, sha256? }): Promise<{ file, duplicate }>`
`data` is a `Uint8Array`, `ArrayBuffer`, `Blob` or `ReadableStream<Uint8Array>`. It is sent
as the raw request body (streams use `duplex: "half"`). `duplicate` is true when the folder
already held the same bytes (status 200 and `X-Homelab-Duplicate: true`).
```ts
const { file, duplicate } = await homelab.files.upload("posters", new Uint8Array(buf), {
  name: "dune.jpg", tags: ["dune"], sha256,
});
```

### `files.uploadFile(folder, path, { name?, tags?, sha256? }): Promise<{ file, duplicate }>`
Node only. Streams a file from disk (`node:fs` is imported inside this function, so the
package still loads in browsers). `name` defaults to the file's base name.
```ts
await homelab.files.uploadFile("videos", "./clip.mp4", { tags: ["trip"] });
```

### `files.list(folder, filters): Promise<{ files, next }>`
One page, newest first. Filters: `tag`, `type` (content type prefix like `"image/"`),
`takenAfter`, `takenBefore` (RFC 3339), `camera`, `after`, `limit` (1-200).
```ts
const { files, next } = await homelab.files.list("photos", { type: "image/", limit: 20 });
```

### `files.all(folder, filters): AsyncGenerator<File>`
Every matching file; fetches pages lazily, passing each `next` as `after`.
```ts
for await (const f of homelab.files.all("photos", { camera: "canon", takenAfter: "2024-05-01" })) {
  console.log(f.id);
}
```

### `files.info(folder, id): Promise<File>`
```ts
const f = await homelab.files.info("posters", id);
```

### `files.update(folder, id, { tags?, info? }): Promise<File>`
Replaces `tags` and/or `info` (a field you leave out stays as it is).
```ts
await homelab.files.update("posters", id, { tags: ["dune", "final"], info: { rating: 5 } });
```

### `files.download(folder, id, { range? }): Promise<Response>`
Returns the fetch `Response` (200, or 206 with `range`). Read it with `.arrayBuffer()`,
`.blob()`, `.text()`, or stream `.body`. Failures throw `HomelabError` instead.
```ts
const res = await homelab.files.download("posters", id);
const bytes = new Uint8Array(await res.arrayBuffer());
const head = await homelab.files.download("posters", id, { range: "bytes=0-99" });
```

### `files.delete(folder, id): Promise<void>`
Moves the file to the trash (removed for good after 30 days).
```ts
await homelab.files.delete("posters", id);
```

## Secrets

All on `homelab.secrets`. Names are checked before anything is sent or read: 1 to 200 of
`A-Z a-z 0-9 _ . : -`, never `.` or `..`. A bad name throws `HomelabError` (status 0).
`:` and the like are URL-encoded for you. A value never appears in an error, `JSON.stringify`
or `console.log`.

### `secrets.get(name, { version?, fresh? }): Promise<string>`
Without `version`, it reads the file `<secretsDir>/<name>` first when there is one (only for
names like `STRIPE_KEY`: letters, digits and `_`, not starting with a digit). The contents are
returned exactly as written. With no file, or with `fresh: true`, it asks Homelab. A `version`
(1 or more) always asks Homelab. A missing secret throws `HomelabError` with status 404.
```ts
const key = await homelab.secrets.get("STRIPE_KEY");
const live = await homelab.secrets.get("STRIPE_KEY", { fresh: true });
const old = await homelab.secrets.get("google_token:user42", { version: 2 });
```

### `secrets.getOrNull(name, { version?, fresh? }): Promise<string | null>`
The same, but `null` when there is no such secret (404). Other errors still throw.
```ts
const t = await homelab.secrets.getOrNull("google_token:user42");
```

### `secrets.set(name, value): Promise<{ name, version }>`
Needs `secrets_api: write` in `homelab.yaml`. A secret the owner set (`config`) cannot be changed (403).
```ts
const { version } = await homelab.secrets.set("google_token:user42", token);
```

### `secrets.delete(name): Promise<void>`
Deleting a secret that is not there is fine.
```ts
await homelab.secrets.delete("google_token:user42");
```

### `secrets.list(): Promise<SecretInfo[]>`
Names, kinds and versions. Never values.
```ts
for (const s of await homelab.secrets.list()) console.log(s.name, s.kind, s.version, s.updated_at);
```

### `secrets.versions(name): Promise<SecretVersion[]>`
The last 5 versions, newest first, as `{ version, set_at }`. 404 if the secret does not exist.
```ts
const vs = await homelab.secrets.versions("google_token:user42");
```

## Types

`Folder`, `File`, `Meta` (every key optional; unknown keys are kept), `ListFilters`,
`ListResult`, `UploadOptions`, `UploadResult`, `UpdateFileOptions`, `SecretInfo`, `SecretVersion`,
`SecretSaved`, `GetSecretOptions` and more are exported.
Note that `File` shadows the DOM `File` when imported; use `import type { File as HomelabFile }`.

## Errors

Every failure is a `HomelabError` with `status` (the HTTP status, or `0` for network errors,
timeouts, bad config and refused path segments) and `message` (the server's
`{"error": "..."}` text, or a plain fallback like `HTTP 413`).

```ts
import { Homelab, HomelabError } from "@homelab/sdk";

try {
  await homelab.files.info("posters", id);
} catch (err) {
  if (err instanceof HomelabError && err.status === 404) console.log("gone");
  else throw err;
}
```

Missing configuration throws at construction: `Set HOMELAB_API_URL or pass url` (and the
same for the token).

## Development

```sh
npm ci && npm run check && npm test && npm run build
```
