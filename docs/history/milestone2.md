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
extra ports: see [services.md](../services.md).

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

homelab-services (Rust) holds every value in its own SQLite file, encrypted with AES-256-GCM
(see [platform-services.md](../platform-services.md#secrets) and [secrets.openapi.yaml](../secrets.openapi.yaml)).
homelabd talks to it as the `system` caller, which only homelabd can sign. The first time it
starts, it copies the values and kept versions from the old `~/.homelab/secrets/secrets.db`
in one step (nothing is copied if any value can't be read) and never deletes that file. The
key is `~/.homelab/secrets/master.key` (32 random bytes, base64, mode 0600), made on first
start. Each value is sealed with its own random nonce, and its app, env, name, kind and
version as additional data, so a value can't be moved to another secret. If the database has values
and the key file is missing, homelabd stops and says so rather than making a new key.

If a secret exists but can't be read or decrypted, homelabd stops and says so. It only makes
a new secret when it is really missing.

### Secrets Homelab makes

`generate_secrets: [ADMIN_TOKEN]` in homelab.yaml asks Homelab to make a secret itself, so
nobody types one. The names follow the same rules as `secrets`, and a name can't be in both
lists. Before each deploy, for every name with no value in that environment (dev and prod
each get their own), Homelab makes one and keeps it in the secret store. It looks like
`k7m3q-x9rt2-h4wpz-c8nfa` (letters and digits that are hard to mix up). A value that exists
is never replaced. It reaches the containers like any other secret and is never "missing".
On the app's **Secrets** page, people who may see values can show it, copy it, and press
**Make new** (`POST /api/apps/{id}/envs/{env}/secrets/{name}/regenerate`, 204; used from the
next deploy). An older Homelab ignores `generate_secrets`, so the app would start without
that variable.

Platform credentials: `GITHUB_TOKEN` (used right away) and `TS_AUTHKEY` (used at the next
start). The identity signing key is a platform secret Homelab makes itself and never lists.

### Who sees secret values

- **Seeing, setting, replacing, making new, deleting or restoring** a value is allowed for
  the household **owner**, and for people the owner gave the **Can see secrets** role
  (`secrets`). Other admins see names, "set / missing", and who changed it and when.
- Only the owner can give or take away the `secrets` role. An admin who tries gets an error.
- **No API key can do any of this**, admin keys and app keys included, whatever their
  scopes. They get 403. Only a signed-in companion session can.
- Platform credentials (Settings) follow the same rules.
- The list answers never hold values. A value comes only from a reveal, and each reveal is
  written to Activity ("viewed ADMIN_TOKEN for prod"), never with the value.
- Homelab keeps the last 5 values of each secret, sealed the same way (each also bound to its
  version number). **History** shows who set each and when, never the values. **Restore**
  makes an old value the newest version, and is written to Activity.

Anyone with root or Docker access on the device can still read secrets: they can read
`master.key` or run `docker exec`. This protects against everyone else.

### Secrets as files

Every deploy writes each secret as its own file in `apps/<app>/<env>/secrets/<NAME>` and
mounts that folder read-only in the app at `/run/secrets/<NAME>`. The folder is 0755 and the
files 0444, so the app's user can read them whoever it is; the environment's own folder stays
0700, which keeps other users on the device out. Files of secrets that are gone are removed.

`secrets_as` in homelab.yaml picks how the app gets them:

- `env` (the default): files, and also environment variables, as before.
- `files`: only files. The secrets are not in `.env`, the compose file or `docker inspect`,
  and are not passed on to every program the app starts. The app reads `/run/secrets/NAME`.

`HOMELAB_TOKEN` and the other `HOMELAB_*` settings stay environment variables in both modes.
Services (such as Jellyfin) are unchanged.

`database: postgres` (or `sqlite`) in homelab.yaml gives each environment its own database and a
`DATABASE_URL`. See [platform-services.md](platform-services.md#databases).

### Logs

Logs shown in the companion or returned by `GET /api/apps/{id}/envs/{env}/logs` have every
value of that app's secrets (and the platform credentials) replaced by `[secret NAME]`. Values
shorter than 6 characters are left alone, and longer values are replaced first.

## API additions

Every change (any method but GET) needs the `X-Homelab-Request: 1` header, as before.

| Method and path | Body | Returns |
|---|---|---|
| `PUT /api/apps/{id}/promotion` | `{"mode":"manual\|auto-dev\|auto"}` | `hub.AppView` |
| `DELETE /api/apps/{id}` | | 204. Stops both environments, removes addresses and secrets, keeps data |
| `GET /api/apps/{id}/secrets` | | `{"envs":[{"env":"dev","secrets":[SecretView]},{"env":"prod",...}],"canSeeValues":bool}`. No values. Admins |

These need a companion session whose person is the owner or has the `secrets` role. An API
key gets 403.

| Method and path | Body | Returns |
|---|---|---|
| `PUT /api/apps/{id}/envs/{env}/secrets/{name}` | `{"value":"..."}` | 204 |
| `DELETE /api/apps/{id}/envs/{env}/secrets/{name}` | | 204 |
| `POST /api/apps/{id}/envs/{env}/secrets/{name}/regenerate` | | 204. Only for `generate_secrets` names |
| `POST /api/apps/{id}/envs/{env}/secrets/{name}/reveal` | | `{"value":"..."}`. Written to Activity |
| `GET /api/apps/{id}/envs/{env}/secrets/{name}/versions` | | `{"versions":[{"version":3,"setBy":"..","setAt":".."}]}`, newest first, up to 5 |
| `POST /api/apps/{id}/envs/{env}/secrets/{name}/versions/{n}/restore` | | 204. Makes a new version |
| `PUT /api/settings/secrets/{name}` | `{"value":"..."}` | 204 |
| `DELETE /api/settings/secrets/{name}` | | 204 |
| `POST /api/settings/secrets/{name}/reveal` | | `{"value":"..."}` |
| `GET /api/settings/secrets/{name}/versions` | | `{"versions":[...]}` |
| `POST /api/settings/secrets/{name}/versions/{n}/restore` | | 204 |

| Method and path | Body | Returns |
|---|---|---|
| `GET /api/settings/secrets` | | `[SettingView]`. No values. Admins |

```ts
// One secret of an app environment.
type SecretView = {
  name: string;
  declared: boolean;   // listed in homelab.yaml; false = stored but no longer used
  set: boolean;
  updatedAt?: string;  // ISO time
  updatedBy?: string;
  version?: number;    // the newest version
  generated?: boolean; // made by Homelab (generate_secrets)
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
