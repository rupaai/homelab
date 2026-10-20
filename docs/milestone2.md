# Milestone 2: any app, no setup

What milestone 2 adds on top of [milestone 1](milestone1.md). The design reasons are in the
project's design docs (milestone_2_plan.md); this file is what the code follows.

## New packages

| Package | Job |
|---|---|
| `internal/identity` | Signs the identity headers the proxy adds to every request (Ed25519) |
| `internal/adapters/secretstore` | `core.Secrets`: encrypted secrets in SQLite (replaces the secrets files) |
| `internal/adapters/gitmirror` | `core.Mirror`: a full `git clone --mirror` of each app repo |
| `internal/adapters/docker` (smoke.go) | `core.SmokeTester`: runs an app's smoke test inside its own image |

`internal/adapters/filesecrets` is now only used to import milestone 1's files once.

## homelab.yaml: what's new

| Field | Default | Meaning |
|---|---|---|
| `id` | from the repo: `rupaai/homelab-hello` -> `rupaai-homelab-hello` | 2-59 lowercase letters, digits or dashes |
| `name` | from the repo: `Homelab Hello` | |
| `image` | `ghcr.io/<owner>/<repo>`, lowercased | |
| `promotion` | `auto-dev` | Only the starting value. After the app is added, the companion setting wins |
| `promote_after` | `30m` | How long dev must stay healthy before an auto promote (1m to 168h) |
| `smoke_test` | none | A command, as a list, run inside the app's image before an auto promote, e.g. `[python, -m, app.smoke]` |

More containers next to the app (a media server, an indexer), shared device folders and
extra ports: see [services.md](services.md).

So a manifest can be as small as `port`, `health`, `tag` and `data`. (A starter template
repo that uses this is parked for later.)

## Identity headers

See the package comment in `internal/identity/identity.go`; it is the reference for every
app's helper. In short, Homelab adds `X-Homelab-User`, `-Name` (percent-encoded),
`-Groups`, `-Time` and `-Signature` and removes any a client sent. The signature is
Ed25519 over

```
homelab-identity-v1\n<app>\n<env>\n<user>\n<name>\n<groups joined by ,>\n<unix seconds>
```

Apps get the public key as `HOMELAB_IDENTITY_KEY` (`ed25519:<base64>`), and also
`HOMELAB_APP` and `HOMELAB_ENV`, which they use when verifying. Accept a signature only
within 5 minutes of now. Milestone 2 knows one person: on this computer it's user
`owner`, name `Owner`, groups `role:owner`. Anyone else gets no headers.

## Smoke tests

`docker run --rm` of the exact release image, on the dev environment's own network
(`homelab-<app>-dev_default`), with `HOMELAB_TARGET_URL=http://app:<port>`, `HOMELAB_APP`,
`HOMELAB_ENV=dev`, no mounts, no published ports, no secrets, 512 MB memory, and a
5-minute limit. Exit code 0 passes. It runs once per dev build, when dev has been healthy
for `promote_after`.

## Promotion

- `manual`: nothing happens by itself.
- `auto-dev`: new passing builds go to dev.
- `auto`: as auto-dev, and once dev has been healthy for `promote_after` and its smoke test
  passed, Homelab promotes it (actor `homelab`), unless a prod secret is missing.
- Every promote, by anyone, is watched for 10 minutes. If prod is unhealthy for two checks
  in a row in that time, Homelab puts back the previous prod version, keeping the data as
  it is, and logs `rollback.auto`. The watch lives in memory, so restarting homelabd ends it. (A promote that is unhealthy right away already goes
  back, with prod's data from before the promote.)
- A commit that failed its smoke test or was rolled back is **held**: auto never promotes
  it again. People can still promote it by hand.

## Secrets

`~/.homelab/secrets/secrets.db` (SQLite) holds every value encrypted with AES-256-GCM. The
key is `~/.homelab/secrets/master.key` (32 random bytes, base64, mode 0600), made on first
start. Each value is sealed with its own random nonce, and its app, env and name as
additional data, so a value can't be moved to another secret. If the database has values
and the key file is missing, homelabd stops and says so rather than making a new key.

At start, values from milestone 1's files (`platform.env`, `<app>.<env>.env`) that aren't in
the store yet are imported, and each file is renamed to `<file>.imported`. homelabd prints
the command to delete them.

Platform credentials: `GITHUB_TOKEN` (used right away) and `TS_AUTHKEY` (used at the next
start). The identity signing key is a platform secret Homelab makes itself and never lists.

Values never leave Homelab through the API. Known values are replaced by `••••••` in the
logs the companion shows.

## API additions

Every change (any method but GET) needs the `X-Homelab-Request: 1` header, as before.

| Method and path | Body | Returns |
|---|---|---|
| `PUT /api/apps/{id}/promotion` | `{"mode":"manual\|auto-dev\|auto"}` | `hub.AppView` |
| `DELETE /api/apps/{id}` | | 204. Stops both environments, removes addresses and secrets, keeps data |
| `GET /api/apps/{id}/secrets` | | `{"envs":[{"env":"dev","secrets":[SecretView]},{"env":"prod",...}]}` |
| `PUT /api/apps/{id}/envs/{env}/secrets/{name}` | `{"value":"..."}` | 204 |
| `DELETE /api/apps/{id}/envs/{env}/secrets/{name}` | | 204 |
| `GET /api/settings/secrets` | | `[SettingView]` |
| `PUT /api/settings/secrets/{name}` | `{"value":"..."}` | 204 |
| `DELETE /api/settings/secrets/{name}` | | 204 |

```ts
// One secret of an app environment.
type SecretView = {
  name: string;
  declared: boolean;   // listed in homelab.yaml; false = stored but no longer used
  set: boolean;
  updatedAt?: string;  // ISO time
  updatedBy?: string;
};
// One platform credential.
type SettingView = {
  name: string;          // GITHUB_TOKEN | TS_AUTHKEY
  description: string;   // what it's for and where to get it, for people
  set: boolean;
  updatedAt?: string;
  updatedBy?: string;
  appliesAfterRestart: boolean;
};
```

Values: 1 to 8192 characters, no line breaks. Names: letters, digits and `_`, not starting
with a digit.

`hub.AppView` gains:

```ts
promoteAfter: string;            // e.g. "30m"
autoPromote: null | {
  state: "waiting" | "smoke-testing" | "held" | "blocked" | "done";
  at?: string;                   // when it will promote, if dev stays healthy (waiting)
  detail: string;                // for people, e.g. "Promotes 9f3c2ab in 12 min if dev stays healthy"
};
held: { commit: string; short: string; reason: string; at: string }[];
mirror: null | { path: string; lastSync?: string; error?: string };
```

and each `EnvView` gains `missingSecrets: string[]`.
