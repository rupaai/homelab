# Secrets

What this page covers: how Homelab keeps passwords and keys (secrets), how they are stored, and how apps get them. Back to [the map](../map.md).

## In short

- A secret is a value an app needs but must not show in code, such as an API key or a database password.
- Values are stored encrypted in `homelab-services` (the Rust program). The key that opens them is a file, `master.key`.
- An app gets its secrets as files in `/run/secrets`, and as environment variables unless its `homelab.yaml` sets `secrets_as: files`.
- The owner, and people the owner gives the "Can see secrets" role, can see or change values. Every reveal is written to the activity log, without the value.
- Each secret keeps its last 5 versions, and an old version can be restored.

Status and open work: [HANDOVER.md](../../HANDOVER.md).

## How it works

1. **Key file.** When `homelabd` starts, it makes sure `<home>/secrets/master.key` exists. It makes a new key only if no secrets store already holds a value. If a store has values and the key is missing, it stops and says so. See `EnsureMasterKey` in [secretstore.go](../../internal/adapters/secretstore/secretstore.go) and the call in [run.go](../../cmd/homelabd/run.go).
2. **Start the store.** `homelabd` starts `homelab-services` and passes it the key's path (`HOMELAB_SERVICES_SECRETS_KEY`), never the key itself. Nothing below starts until the secrets service is up, and nothing falls back to plain text.
3. **Store.** `homelab-services` keeps one SQLite file, `<home>/services/secrets.db`. Each value is sealed with AES-256-GCM under the master key, with its own random nonce. The sealed value is tied to its app, environment, name, kind and version, so a value copied to another place does not open.
4. **Old store.** On first start, `homelab-services` imports the old Go store (`<home>/secrets/secrets.db`, read only) once, with the same key. A failed import is tried again at the next start.
5. **Save.** The companion (or the app's own key, for secrets the app saves) calls `homelabd`. `homelabd` checks who may do it, then calls `homelab-services` on `/internal/secrets/...`.
6. **Deploy.** Before a deploy, `homelabd` reads the app's secrets and makes any listed in `generate_secrets` that are missing. It writes one file per secret into the environment's `secrets` folder, mounted read only at `/run/secrets`. Unless `secrets_as: files`, it also passes them as environment variables. See [docker.go](../../internal/adapters/docker/docker.go) and [compose.go](../../internal/adapters/docker/compose.go).
7. **Apps read.** The app reads the files (the plugins read `/run/secrets` first, see [secrets.ts](../../plugins/typescript/src/secrets.ts) and [secrets.rs](../../plugins/rust/src/secrets.rs)). If it needs one that is not a file, it can ask Homelab with its app key.
8. **Logs.** Secret values are hidden in the logs the app writes, see [logs.go](../../internal/hub/logs.go).

## Deep dive

### Files

| File | What it does |
|---|---|
| `internal/adapters/secretstore/secretstore.go` | Makes and checks `master.key` (`EnsureMasterKey`). Refuses to make a new key over old values. |
| `internal/adapters/secretstore/secretstore_test.go` | Tests for the key file rules. |
| `internal/adapters/services/secrets.go` | Go client for `homelab-services`: list, inject, set, delete, versions, restore. Returns `ErrSecretsUnavailable` while the service is down. |
| `internal/adapters/services/services.go` | Starts `homelab-services` and passes it the key and old-store paths. |
| `internal/adapters/services/secrets_test.go`, `secrets_service_test.go` | Tests for the client. |
| `internal/hub/manage.go` | The rules for each secret action: list, reveal, set, regenerate, delete, versions, restore, and saved secrets. Records `secret.viewed` in Activity. |
| `internal/hub/generated.go` | Makes `generate_secrets` values once per environment, on first deploy. |
| `internal/hub/hub.go` | Reads secrets for a deploy and puts them in the environment spec (env vars unless `secrets_as: files`). |
| `internal/hub/access.go` | The `secret-values` permission: owner, or the `secrets` role. |
| `internal/hub/people.go` | Only the owner can give or take the `secrets` role. |
| `internal/hub/logs.go` | Hides secret values in app log lines. |
| `internal/hub/secrets_test.go`, `saved_secrets_test.go`, `generated_test.go`, `access_test.go` | Tests for the hub rules. |
| `internal/api/api.go` | The companion routes (lines around 159 to 178). |
| `internal/api/secrets.go` | The companion handlers: list, reveal, set, versions, restore. |
| `internal/api/secrets_test.go`, `secrets_service_test.go` | Tests for the handlers and the app door. |
| `internal/manifest/manifest.go` | Reads `secrets`, `generate_secrets`, `secrets_as` and `secrets_api` from `homelab.yaml`. |
| `services/homelab-services/src/secrets.rs` | The encrypted store: seal and open, versions, the one-time import (`import_go`), the key check at start. |
| `services/homelab-services/src/secrets_api.rs` | The HTTP routes, and who may call them. |
| `services/homelab-services/src/secrets/tests.rs` | Tests for the store (including the five-version rule). |
| `services/homelab-services/src/app.rs` | Refuses callers who may not use the secrets paths at all. |
| `services/homelab-services/src/config.rs` | Reads `HOMELAB_SERVICES_SECRETS_KEY` and `HOMELAB_SERVICES_SECRETS_IMPORT`. |
| `web/src/routes/apps/[id]/secrets/+page.svelte` | The secrets screen for an app. |
| `web/src/lib/components/SecretRow.svelte`, `SecretSheet.svelte`, `SecretHistory.svelte` | One secret, its edit sheet, and its versions. |
| `docs/secrets.openapi.yaml` | The API reference for the secrets routes. |

### Routes

Companion (people, signed in), in `internal/api/api.go`:

- `GET /api/apps/{id}/secrets`: list an app's secrets, with values hidden.
- `PUT` and `DELETE /api/apps/{id}/envs/{env}/secrets/{name}`: set or delete a config secret.
- `POST .../secrets/{name}/reveal`: show one value.
- `POST .../secrets/{name}/regenerate`: make a new generated value.
- `GET .../secrets/{name}/versions`, `POST .../versions/{n}/restore`: the kept versions.
- `POST /api/apps/{id}/envs/{env}/saved/{name}/reveal` and `DELETE .../saved/{name}`: secrets an app saved itself.
- `GET /api/settings/secrets` and the matching `PUT`, `DELETE`, `reveal`, `versions` and `restore` routes under `/api/settings/secrets/{name}`: platform secrets (names such as the email settings), not tied to an app.

App door (app keys only), forwarded to `homelab-services`:

- `GET /api/secrets`, `GET /api/secrets/{name}`, `PUT` and `DELETE /api/secrets/{name}`, `GET /api/secrets/{name}/versions`. See [platform-services.md](../platform-services.md).

Internal (`homelabd` to `homelab-services` only), in `secrets_api.rs`: `/internal/secrets` (list), `/internal/secrets/item/{name}` (with `versions` and `restore`), `/internal/secrets/missing`, `/internal/secrets/values`, `/internal/secrets/app/{app}`.

### Edge cases and rules

- **Who may see values.** Only the owner, or someone with the `secrets` role, may reveal, set, regenerate, delete or restore a value. The check is in `internal/hub/access.go` and again in `manage.go`. API keys can never do it: the companion sign-in is required (`msgSecretSession` in `internal/api/api.go`).
- **Who may see names.** Admins see the list of secret names (`ActionSecrets`, admin only, in `internal/api/api.go`). Other people do not get the page.
- **What an app may do.** An app key reads its own app's config secrets, and never changes them. With `secrets_api: write` it may also save and delete its own saved secrets. It never sees platform secrets or `HOMELAB_` names (`secrets_api.rs`, header comment).
- **Names starting with `HOMELAB_`** are Homelab's own. They are never shown or set by an app (`reservedPrefix` in `internal/hub/keys.go`). Revealing them is refused.
- **Five versions.** Each secret keeps its last 5 values (`KEPT` in `secrets.rs`). Older ones are dropped. Restoring an old one makes it the current value again, and the number keeps going up.
- **Size.** A config value may be up to 8192 bytes (`MAX_CONFIG_VALUE` in `secrets.rs`). The request body limit is 512 KiB (`secrets_api.rs`), enough for a value with every byte escaped.
- **Activity.** Revealing a value writes `secret.viewed` to the activity log, with the name and environment, never the value.
- **Logs.** Secret values are removed from log lines before they are stored. The hiding list is kept for a short time and cleared when a secret changes or the app deploys.
- **Wrong or missing key.** `homelab-services` checks at start that the key opens what is stored. If not, it stops with a message and does not start. A missing key file is never replaced while values exist.
- **Service down.** While `homelab-services` is not running, every secret call fails with a plain message. A deploy does not go ahead without its secrets.
- **Key file permissions.** The key must be readable only by its owner (`chmod 600`). Otherwise both `homelabd` and `homelab-services` refuse to read it.
- **Generated secrets.** Made once per environment, the first time the app is deployed there. `postgres` apps also get the database password this way. The owner never types them.
- **Identity key.** The key that signs the headers apps get (see [people.md](people.md)) is kept as a platform secret too, made the first time.
- **Saved secrets are kept apart.** A secret an app saves itself (`secrets_api: write`) is sealed with kind `app`, and config secrets with kind `config`. A value of one kind does not open as the other (the AAD in `secrets.rs`).
- **Postgres URL.** For `database: postgres`, `homelabd` builds the database URL from the generated password at deploy time. It is a secret, so it follows `secrets_as` (`internal/hub/hub.go`). SQLite's path is a plain setting, not a secret.
- **Mail settings.** `EMAIL_ADDRESS` and `EMAIL_APP_PASSWORD` are platform secrets. The mailer reads them for each message, so a change applies at once (`cmd/homelabd/run.go`).
- **A reveal is logged only when it works.** `secret.viewed` is written after the value is found. A failed reveal writes nothing.
- **Names and values are checked before anything is stored** (`check_name_and_value` in `secrets.rs`). A config name uses letters, digits and `_`, and does not start with a digit. A value is 1 to 8192 bytes, with no line breaks or NUL bytes. App names are checked with `valid_name` in `src/disk.rs`.

### To change it

- To add a secret rule for apps, edit `appKeyScopes` and the secrets part of `internal/hub/keys.go`, then the checks in `services/homelab-services/src/secrets_api.rs`.
- To change who may see values, edit `ActionSecretValues` in `internal/hub/access.go`.
- To change how many versions are kept, edit `KEPT` in `services/homelab-services/src/secrets.rs`, then the test `keeps_five_versions_and_numbers_continue` in `src/secrets/tests.rs`.
- To change what an app gets, edit `internal/hub/hub.go` (the deploy spec) and `internal/adapters/docker/docker.go` (the files).
- To change a companion route, edit `internal/api/api.go` and `internal/api/secrets.go`, and the client in `internal/adapters/services/secrets.go`.

### Test it

```sh
go test ./internal/adapters/secretstore/... ./internal/adapters/services/... ./internal/hub/... ./internal/api/...
make check-services
make check
```

`make check` runs gofmt, `go vet` and `go test ./...`. `make check-services` runs format, clippy and the Rust tests. CI runs both. The web screen is checked by `make check-web`.
