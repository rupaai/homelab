# Milestone 1: how the pieces fit

This is the build contract for milestone 1 (the deploy loop on the Mac). The design
reasons live in the project's design docs; this file is what the code follows.

## Layout of the code

| Package | Job |
|---|---|
| `internal/core` | Shared types and the contracts (interfaces) every adapter implements |
| `internal/manifest` | Reads an app's `homelab.yaml` |
| `internal/state` | Desired state, deployment history and activity log, in SQLite |
| `internal/hub` | The core: reconciler, health checks, build checks, add app, promote, rollback |
| `internal/adapters/fake` | In-memory adapters for tests |
| `internal/adapters/github` | Source, Builder and Registry for GitHub and ghcr.io |
| `internal/adapters/docker` | Deployer, using Docker Compose (OrbStack on the Mac) |
| `internal/adapters/tailscale` | Addresses as Tailscale nodes (`tsnet`), one per app environment |
| `internal/adapters/local` | Addresses on 127.0.0.1 ports, for trying things without Tailscale |
| `internal/adapters/httphealth` | Health checks over HTTP |
| `internal/api` | The HTTP JSON API the companion uses, and serving the companion |
| `internal/companion` | The built companion, embedded into the binary |
| `web/` | Companion source (SvelteKit, static output) |
| `cmd/homelabd` | The program: config, wiring, `init`, `run`, `service install` |

The hub only depends on `core`, `manifest` and `state`. Adapters only depend on `core`.
Nothing outside `cmd/homelabd` knows which adapters are in use.

## What lives on the computer

Everything is under one folder, `~/.homelab` by default (`--home` to change it):

```
~/.homelab/
  config.yaml              settings (below)
  secrets/
    platform.env           GITHUB_TOKEN=..., TS_AUTHKEY=...   (mode 0600)
    movies.dev.env         TMDB_API_KEY=...                   (mode 0600)
    movies.prod.env
  homelab.db               state (SQLite)
  apps/<app>/<env>/
    compose.yaml           written by the docker adapter
    .env                   settings and secrets for the containers (mode 0600)
    data/<name>/           the app's data folders, one copy per environment
  backups/<app>/prod/<time>-<commit>/   the app's own data copied before each promote (newest 5 kept)
  tailscale/<node>/        tsnet state per address
  local/ports.json         local adapter's port assignments
  logs/homelabd.log        when run as a service
```

## config.yaml

```yaml
# Who may use the companion over Tailscale (their Tailscale login names).
# On this computer itself (127.0.0.1) the companion is always open.
owners: [labiba@example.com]
# tailscale: each environment gets its own Tailscale address (needs TS_AUTHKEY).
# local: addresses are http://127.0.0.1:<port>, for trying things out.
addresses: local
tailscale:
  tags: [tag:homelab]
listen: 127.0.0.1:7080   # the companion on this computer
```

## Secrets files (milestone 1)

Plain `KEY=VALUE` lines, `#` comments, blank lines ignored, optional surrounding quotes
removed. `homelabd` refuses to read a secrets file other users can read (mode must not
grant group or other access) and says how to fix it (`chmod 600 <file>`). The encrypted
store replaces these files in milestone 2 behind the same `core.Secrets` contract.

## Ports

App environments publish on `127.0.0.1` only: `17000 + slot*10` for dev and `+1` for
prod, where `slot` is the app's number in the registry. Addresses forward to those ports.

## API

All JSON. Errors are `{"error": "<message for people>"}` with status 400 for a
`hub.UserError`, 404 for an unknown app, 409 for `hub.ErrBusy`, 500 otherwise (with a
generic message; details go to the log). Every request must come from an owner (see
config) or from this computer, otherwise 403. Changes are POSTs and need the header
`X-Homelab-Request: 1` (a cheap CSRF guard; browsers can't send it cross-site without
a preflight).

| Method and path | Calls | Returns |
|---|---|---|
| `GET /api/me` | | `{"login","name","local"}` |
| `GET /api/apps` | `hub.Overview` | `[]hub.AppView` |
| `POST /api/apps` `{"repo","branch"}` | `hub.AddApp` | `hub.AppView` (201) |
| `GET /api/apps/{id}` | `hub.App` | `hub.AppView` |
| `POST /api/apps/{id}/deploy` `{"commit"}` (commit optional) | `hub.DeployDev` | 202 |
| `GET /api/apps/{id}/promote` | `hub.PreviewPromote` | `hub.PromotePreview` |
| `POST /api/apps/{id}/promote` | `hub.Promote` | 202 |
| `GET /api/apps/{id}/envs/{env}/rollback` | `hub.PreviewRollback` | `hub.RollbackPreview` |
| `POST /api/apps/{id}/envs/{env}/rollback` | `hub.Rollback` | 202 |
| `GET /api/apps/{id}/envs/{env}/logs?lines=200` | `hub.Logs` | `{"lines": "<text>"}` |
| `GET /api/activity?app=&before=&limit=` | `hub.Events` | `[]state.Event` |
| `GET /healthz` | | `{"ok":true,"version":"..."}`, no auth |

Everything else serves the companion's static files, falling back to `index.html`.
