# API keys and the app door

What this page covers: the keys that apps and outside servers use to call Homelab, the separate address apps call (the app door), and how those calls reach the services program. Back to [the map](../map.md).

## In short

- Every app environment (dev and prod) gets its own app key. Homelab puts it in the app as `HOMELAB_TOKEN`, next to `HOMELAB_API_URL`.
- An app key works only on the app door, and only for its own app and environment.
- An admin makes an admin key in the companion, for a server outside Homelab. It has only the scopes picked for it, and works on the companion's address.
- The app door listens on port 7085 (`app_api_port` in `config.yaml`). On Linux it listens on Docker's bridge address, so apps can reach it as `host.docker.internal`.
- Calls for files, databases, data, secrets, releases and logs go on to `homelab-services`. `homelabd` signs each one, and the service checks the signature.

For the user-facing rules, see [platform-services.md](../platform-services.md#api-keys). Status and open work: [HANDOVER.md](../../HANDOVER.md).

## How it works

1. **Make a key.** An app key is made when an app environment first needs one, and kept with the app's secrets as `HOMELAB_TOKEN` (`appKey` in [keys.go](../../internal/hub/keys.go)). An admin key is made by an admin in the companion, with a name, at least one scope and an optional expiry (`CreateAdminKey` in the same file).
2. **Store only a hash.** The key starts with `hlk_`. Homelab keeps only its SHA-256 hash, in the Homelab state file `<home>/homelab.db`, table of API keys ([apikeys.go](../../internal/state/apikeys.go)). A lost key cannot be shown again.
3. **Listen.** `homelabd` starts the app door on `app_api_port` (default 7085). On Linux it listens on Docker's bridge gateway. On macOS it listens on `127.0.0.1` ([run.go](../../cmd/homelabd/run.go), `openAppDoor`).
4. **Check the key.** The door checks for a `Authorization: Bearer` header. The request goes to `serveKey` in [keys.go](../../internal/api/keys.go), which removes `Authorization` and any `X-Homelab-*` header and cookie. `Authenticate` in [keys.go (hub)](../../internal/hub/keys.go) finds the key by hash, refuses a wrong prefix, a revoked key, or an expired one.
5. **Pick the route.** `/api/users/me` and `/api/users` are answered by Homelab ([api/keys.go](../../internal/api/keys.go)). The service paths (`/api/files`, `/api/db`, `/api/data`, `/api/secrets`, `/api/releases`, `/api/logs`, listed in [services.go](../../internal/api/services.go)) are forwarded by `keyToServices`.
6. **Check scopes.** Each route checks the key's scopes. The secrets and releases routes refuse admin keys outright.
7. **Sign and forward.** `homelabd` adds `X-Homelab-Caller` (who is calling, for which request) and `X-Homelab-Signature` (an ed25519 signature). It signs with a key made at each start (`internal/adapters/services/services.go`).
8. **Verify in the service.** `homelab-services` checks the signature, the caller header size and kind, and that the call is less than 30 seconds old ([caller.rs](../../services/homelab-services/src/caller.rs)). Calls that fail are refused. Nothing calls the service without `homelabd`.
9. **Log.** The call is written to Activity. A key that reads the same thing again and again is logged once every 10 minutes. Every change is always logged (`LogKeyUse`).

## Deep dive

### Files

| File | What it does |
|---|---|
| `internal/hub/keys.go` | Key format, scopes, app and admin keys, `Authenticate`, revoke, the list shown to admins, `LogKeyUse`. |
| `internal/hub/keys_test.go` | Tests for keys and scopes. |
| `internal/state/apikeys.go` | Stores keys by hash: put, find by hash or id, list by kind, revoke, last-used time. |
| `internal/state/apikeys_test.go` | Tests for the store. |
| `internal/api/keys.go` | The key routes and the door: `Door`, `serveKey`, `keyRoutes`, `usersMe`, `usersList`, and the companion key handlers `listKeys`, `createKey`, `revokeKey`. |
| `internal/api/keys_test.go` | Tests for key calls. |
| `internal/api/services.go` | The service paths a key may reach, and `keyToServices` (scope check, signing, forwarding). |
| `internal/api/appauth.go` | The sign-in hand-off that gives a person's browser an app code. Not a key, but it uses the same app access rules. |
| `internal/api/api.go` | Routes the companion address. A call with a key (not a session) goes to `serveKey`. |
| `internal/adapters/services/services.go` | Makes the signing key, and signs each forwarded call. |
| `internal/adapters/docker/docker.go` | Finds the Docker bridge address for the door (Linux). |
| `cmd/homelabd/config.go` | `app_api_port` (default 7085) and `services_port` (default 7086), checked to be different. |
| `cmd/homelabd/run.go` | Starts the door and gives apps `HOMELAB_API_URL` (`http://host.docker.internal:<port>`). |
| `services/homelab-services/src/caller.rs` | Checks the signed caller headers. |
| `services/homelab-services/src/releases_api.rs` | Refuses admin keys on releases (`releases:read` is an app-key scope). |
| `services/homelab-services/src/secrets_api.rs` | Refuses people and admin keys on the secrets routes. |
| `web/src/routes/settings/keys/+page.svelte` | The API keys screen (Settings, API keys). |
| `web/src/lib/keys.ts` | Key scope names and key status text for that screen. |
| `docs/platform-services.md` | The user-facing guide to keys, scopes and the door. |

### Routes

Companion (signed-in admins), in `internal/api/api.go`:

- `GET /api/keys`: list admin and app keys (no secrets, only prefixes and names).
- `POST /api/keys`: make an admin key. The token is shown once.
- `DELETE /api/keys/{id}`: revoke a key.

App door and companion (key calls, `internal/api/keys.go` and `services.go`):

- `GET /api/users/me`: who the key is.
- `GET /api/users?app=<id>`: people with access to an app.
- `/api/files`, `/api/db`, `/api/data`, `/api/secrets`, `/api/releases`, `/api/logs` and their sub-paths: forwarded to `homelab-services`. Scope rules are in [platform-services.md](../platform-services.md#scopes).
- Any other path with a key gets `404` ("API keys can't use that part of Homelab.").

```sh
# From inside an app container
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/users"
```

Service internal (from `homelabd` only), in `services/homelab-services/src/app.rs` and the API files: the `/internal/...` paths for secrets, releases and the rest.

### Scopes

Scopes are checked in `internal/hub/keys.go`:

| Scope | Who has it | Gives |
|---|---|---|
| `users:read` | Admin keys (picked), every app key | Read people. |
| `files:read`, `files:write` | Admin keys (picked) | Read files; also write them. |
| `db:read` | Admin keys (picked) | Look inside databases, read only. |
| `logs:read` | Every app key; admin keys (picked) | Search logs. An app sees only its own lines. |
| `releases:read` | Every app key only | List the app's own releases. |
| `secrets:read` | App keys, when `secrets_api` is `read` or `write` | Read the app's own secrets. |
| `secrets:write` | App keys, when `secrets_api` is `write` | Save and delete the app's own secrets. |

Admin keys can only get the first five. The rule for app keys is `appKeyScopes` in `keys.go`: `users:read`, `releases:read` and `logs:read` always, and the secrets scopes from `secrets_api` in `homelab.yaml` (default `read`).

### Edge cases and rules

- **Who may make an admin key.** Only admins (`CreateAdminKey`). The name is 1 to 60 characters, with no control characters. At least one scope is needed, and each must be a known one.
- **Admin keys stop working** when they are revoked, when they expire, or when the person who made them is no longer an admin (see the comment on `Authenticate` and `isAdmin` in `keys.go`).
- **App keys work only on the app door.** An app key sent to the companion address is refused. Admin keys are refused on the door.
- **Revoking an app key** stops it at once. The app gets a new one the next time it is deployed.
- **A key cannot reach another app.** The app key carries its app and environment. A dev key never reaches prod data (`keyToServices` passes them on).
- **Bad or unknown keys** get `401`. A wrong prefix is refused before the database is read.
- **The door says why** when the wrong kind of key is used: "Only an app's own key (HOMELAB_TOKEN) works here. Use admin keys on the companion's own address." (`serveKey` in `internal/api/keys.go`).
- **Sessions are not keys.** On the companion address, a `Bearer` token that starts with `hls_` is a sign-in session (see [people.md](people.md)). Anything else with a `Bearer` header goes to the key check (`internal/api/api.go`).
- **The key list** shows names, prefixes, scopes and last use. It never shows a full key.
- **Headers from the caller are dropped** before forwarding, so nobody can forge `X-Homelab-*` headers (`stripCallerHeaders`).
- **Signed calls expire** after 30 seconds (`MAX_CLOCK_SKEW` in `caller.rs`). A call signed for another path or method is refused.
- **Ports.** `app_api_port` and `services_port` must each be 1024 to 65535, and they must differ (`cmd/homelabd/config.go`).
- **Key use is logged.** Changes always, reads once every 10 minutes (`LogKeyUse`). The last-used time is kept on the key.

### To change it

- To add a scope, add the constant and its check in `internal/hub/keys.go` (add it to `scopes` too, if admins may pick it), then the route check in `internal/api/services.go` and `services/homelab-services/src/`.
- To change what an app key gets by default, edit `appKeyScopes` in `internal/hub/keys.go`.
- To change a port, edit `app_api_port` or `services_port` in `config.yaml`, which `cmd/homelabd/config.go` reads.
- To add a path that keys may reach, add it to `servicePaths` in `internal/api/services.go`, and add its route in `services/homelab-services/src/app.rs`.
- To change the door's address, edit `openAppDoor` in `cmd/homelabd/run.go` and `BridgeGateway` in `internal/adapters/docker/docker.go`.

### Test it

```sh
go test ./internal/hub/... ./internal/state/... ./internal/api/... ./internal/adapters/services/...
make check-services
make check
```

The key screen is checked by `make check-web` (`web/src/lib/keys.test.ts`).
