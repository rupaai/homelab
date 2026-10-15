# Milestone 3: people and sign-in

Milestone 3 adds household accounts, passkey sign-in, app-specific access, invitations,
recovery and person management. Existing app state and the encrypted secrets database remain
in place. On upgrade, opening the companion prompts the Mac user to create the first owner.
Loopback now identifies a local request for Host checks; it no longer grants owner access.
Installs configured for Tailscale addresses cannot sign in through this milestone's localhost-only
flow; use local mode until milestone 4 adds HTTPS sign-in for Tailscale hosts.

## New packages and contracts

| Package | Job |
|---|---|
| `internal/adapters/pocketid` | Pocket ID implementation of `core.Identity`: users, groups, OIDC client and one-time access links |
| `internal/adapters/noop` | Optional email adapter; links remain the invite path. Resend and SMTP are TODOs. |
| `internal/adapters/fake` | In-memory identity and no-op email fakes for hub tests |
| `internal/hub/access.go` | Shared role and app-grant permission table |

`core.Identity` provides `Ensure`, `Disable`, `LoginLink` and `Observe`. `core.Email` provides
`Send`; the runtime implementation intentionally does nothing. Adapters stay outside the hub.
The reconciler restores stored people and group membership to Pocket ID and writes an activity
event when an observed account has drifted.

## State tables

Migration 3 adds household state; migration 4 adds app-bound authorization codes and sessions.
Both are appended to the migration list. Earlier migrations are unchanged.

| Table | Contents |
|---|---|
| `people` | Name, optional email, provider id, status and last sign-in time |
| `person_roles` | Extensible role set; a unique index enforces one owner |
| `person_grants` | One `use` or `build` grant per person and app |
| `invites` | Hashed, single-use invitation values with seven-day expiry |
| `sessions` | Hashed companion session ids, seven-day idle and 30-day absolute expiry |
| `recovery` | Bcrypt recovery-code hash and used time |
| `one_time_codes` | Hashed, single-use recovery links and expiry |
| `app_auth_codes` | Hashed, single-use app exchange codes with 60-second expiry and return path |
| `app_sessions` | Hashed per-app browser sessions, app/environment binding and expiry |

## Access rules

| Action | Owner/admin | Builder | Member |
|---|---|---|---|
| App list | All apps | Apps with a grant | Apps with a grant |
| Production app | All apps | `use` or `build` grant | `use` or `build` grant |
| Development and projects | All apps | `build` grant only | `build` grant only |
| Logs | All apps | `build` grant only | `build` grant only |
| People, settings, secrets and app management | Allowed | Denied | Denied |

App access is independent of role: a `build` grant also permits production. The API checks
every request; the companion only hides controls. A member receives production-only app
views. Builders receive full app/project views only for apps they can build. Apps receive
signed identity groups such as `role:member` and `app:movies`.
Builders can deploy and promote their granted apps, but setting or deleting app secrets remains
admin-only in this milestone.

## API additions

All `/api` routes except bootstrap, setup, recovery and logout need a stored signed-in session.
Changes need `X-Homelab-Request: 1` and JSON. Host checks remain in place. People/settings
routes require owner or admin; app routes are checked against the requested app and action.

| Method and path | Body | Returns | Who may call |
|---|---|---|---|
| `GET /api/bootstrap` | — | Whether first setup is needed | Local computer |
| `POST /api/setup` | `{name,email}` | Pocket ID passkey link and one-time recovery code | Local computer, before an owner exists |
| `GET /auth/start` | Optional safe relative `return` | Redirect to Pocket ID authorization with PKCE, state and nonce | Local computer |
| `GET /auth/callback` | OIDC callback query | Creates a server-side session and redirects back | Local computer |
| `GET /api/me` | — | Name, email and roles | Signed-in person |
| `POST /api/me/passkey-link` | `{}` | Fresh Pocket ID passkey link | Any signed-in person |
| `POST /api/logout` | — | 204 and expires the companion cookie | Local computer; CSRF header |
| `POST /api/recover` | `{code}` | 204 and one-time owner session | Local computer; rate-limited |
| `GET /recover/{token}` | — | One-time owner session from a 15-minute link | Local computer |
| `GET /api/people` | — | People, roles, grants and status | Owner or admin |
| `POST /api/people` | Name, optional email, roles and grants | Invite URL (201) | Owner or admin |
| `PUT /api/people/{id}` | Updated name, roles and grants | Updated person | Owner or admin |
| `POST /api/people/{id}/invite` | — | Replacement invite URL; revokes the old link | Owner or admin |
| `DELETE /api/people/{id}` | — | Removal checklist | Owner or admin; owner cannot be removed |
| `GET /invite/{token}` | — | Consumes invite and redirects to Pocket ID | Invite holder, local computer |
| `GET /auth/app` | `app`, `env`, absolute `return` | Redirect to app callback with a 60-second code | Signed-in person with the app grant |
| `GET /api/apps` | — | Role-filtered app list | Signed-in person |
| `GET /api/projects` | — | Apps with build access | Owner/admin or builder |
| `GET /api/apps/{id}` | — | App view filtered by its grant | Signed-in person with access to that app |
| Existing app action routes | Existing milestone 1/2 request bodies | Deploy, promote, rollback, logs and secrets | Owner/admin or builder with `build` grant, as applicable |

On an app’s first visit, Homelab sends the browser through `/auth/app`. The app proxy
exchanges the code once, sets an HttpOnly `homelab_app_<app>_<env>` cookie, and redirects to
the original relative path. The proxy never forwards any `homelab_*` cookie or client-sent
`X-Homelab-*` header.

## Pocket ID API checked

Pocket ID is pinned to `ghcr.io/pocket-id/pocket-id:v2.18.0`. Homelab starts a hidden Compose
project, publishes it only on `127.0.0.1:7081`, stores data under
`~/.homelab/services/pocketid`, and generates its API key, encryption key and OIDC client
secret into the encrypted platform secret store. Compose receives those values only in the
child process environment; they are not written to a service `.env` file. Pocket ID account
editing is disabled so Homelab remains the source of truth for names and email addresses.

The adapter uses the documented user create/update, user-group list/create, user-group
membership, user one-time access token, OIDC client create/update, client secret list/create,
and health endpoints. API calls use `X-API-KEY`. Pocket ID does not document a way to set the
one-time access-token lifetime in the token request, so Homelab invite expiry is enforced by
its own seven-day hashed invite. The API also does not document a way to terminate existing
Pocket ID browser sessions. Removal disables provider sign-in and immediately deletes Homelab
and app sessions. The checklist notes that Pocket ID may retain its own browser session; the
disabled account cannot start another sign-in and the removed Homelab account has no app access.
The API returns the canonical user id on creation, which Homelab stores and uses for later
Pocket ID operations.

References checked against Pocket ID 2.18.0 on 15 October 2026: [API endpoints](https://pocket-id.org/docs/api/endpoints), [environment variables](https://pocket-id.org/docs/configuration/environment-variables), [installation](https://pocket-id.org/docs/setup/installation).

## Localhost limit

Open the companion at `http://localhost:7080`. Pocket ID uses `http://localhost:7081`, and
local app addresses use `localhost` as well. WebAuthn accepts localhost as a relying-party
host; it does not accept `127.0.0.1`. This milestone supports sign-in on the Mac only. Other
devices need the Tailscale HTTPS addresses planned for milestone 4.
The companion starts before Pocket ID is ready. It creates and configures Pocket ID in the
background with retries; `/healthz` and the local recovery flows remain available while the
sign-in service is unavailable. The sign-in page explains when setup is still starting.

## Try it on your Mac

These commands build and start a fresh local install using OrbStack. For an existing Homelab
install, skip `homelabd init`; the additive migration keeps its apps and secrets.

```sh
cd ~/codes/homelab
git switch milestone-3-people
docker context use orbstack
make build
bin/homelabd init
bin/homelabd run
```

Open `http://localhost:7080`, create the owner, register the passkey in Pocket ID, then sign
in with that passkey. To recover from the Mac terminal, run `bin/homelabd recover` and open
the printed link on the same computer within 15 minutes. Keep Homelab running while you try
the companion; the recovery link is stored in `homelab.db` and is one use.
