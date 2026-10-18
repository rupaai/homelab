# Milestone 4: access from anywhere

Milestone 4 lets household people reach Homelab and their apps from any device, anywhere,
through Tailscale. A request that arrives over the tailnet carries the sender's Tailscale
login, and Homelab matches that login to a household person. Being signed in to Tailscale is
being signed in to Homelab: no passkey and no Pocket ID page. Pocket ID stays for the Mac
itself (localhost), owner setup and recovery.

It only applies with `addresses: tailscale` in `~/.homelab/config.yaml` and a Tailscale OAuth
client in Settings. With local addresses nothing changes.

## Sharing a tailnet

Homelab can run on a tailnet that people already use. It writes only its **own part** of the
policy file and keeps the rest, comments included:

- Homelab's part is every group named `group:hl-*`, every tag named `tag:hl-*`, and every grant
  whose sources are only `group:hl-*` or `autogroup:admin` and whose destinations are only
  `tag:hl-*`. Keep the `hl-` names for Homelab; don't use them in your own rules.
- `tag:homelab` (the OAuth client's tag) is added if it's missing and left alone if it's there.
- Everything else (other groups, tags, grants, `acls`, `ssh`, `tests`, postures) is not touched.
- Every 15 minutes Homelab checks its part again and fixes it if someone edited it by hand.
  The file is only saved when Homelab's part actually changed.
- Homelab invites people as plain **members**. Your own rules decide what members can reach
  outside Homelab's apps, so check them: a rule like `"src": ["*"]` lets every invited person
  reach every device.
- Removing a person only removes Tailscale users with the member role, and their untagged
  devices. The owner and admins are never removed.

## New packages and contracts

| Package | Job |
|---|---|
| `internal/adapters/tsapi` | Tailscale REST API client for `core.RemoteAccess`: OAuth client credentials, user invites, users, devices, tagged auth keys and Homelab's part of the policy file (with ETag, retried once on a concurrent change) |
| `internal/adapters/fake` (`Remote`) | In-memory tailnet for hub tests |
| `internal/hub/remote.go` | Tailnet sign-in, the access policy, invites, removal and device status |

`core.RemoteAccess`: `Invite`, `CancelInvite`, `SetPolicy`, `AuthKey`, `Observe`,
`RemoveDevices`, `RemoveUser`. The hub never sees the policy file format; `tsapi.MergePolicy`
merges Homelab's part into the tailnet's current file (read and written as HuJSON).

## Who reaches what

Each address joins the tailnet as its own node with its own tag, minted through the OAuth
client: `tag:hl-homelab` (the companion), `tag:hl-<app>` (prod) and `tag:hl-<app>-dev`.
`tag:homelab` (the OAuth client's tag) owns them all.

| Group | Members | Reaches |
|---|---|---|
| `hl-everyone` | everyone with a Tailscale login in Homelab | the companion |
| `hl-app-<app>` | `use` or `build` grant on the app | the app's prod address |
| `hl-build-<app>` | `build` grant on the app | the app's prod and dev addresses |
| `hl-admins` | owners and admins | every Homelab address |
| `autogroup:admin` | the tailnet's own admins | every Homelab address |

Nothing else is allowed. The network rules and Homelab's own checks are independent: the API
and the app proxy still check `hub.Can` on every request, so a wrong rule never opens People,
Settings or another app.

## Sign-in over the tailnet

- The API and the app proxy take the viewer's Tailscale login (from `WhoIs`) and look up
  `people.tailscale_login`. A tagged node (a server) is never a person.
- A person's first visit counts as their sign-in (status `signed_in`) and retires their
  Homelab invite link.
- A login nobody has gets a plain "you're not in this household yet" page, never a redirect
  to localhost.
- Sign-out: there is no Homelab session over the tailnet. Signing out of the Tailscale app,
  or removing the person, ends access.

The Tailscale login defaults to the invite email. When someone joins with a different account
(say GitHub), copy their login from the admin console's Users page into their **Tailscale
login** field under People.

## People

- **Invite** also sends a Tailscale invite and returns its link (shown with a QR code). The
  Homelab link for this computer is still there. If Tailscale refuses, the invite still works
  locally and the reason is shown.
- **Status** adds "Hasn't joined Tailscale yet" and "Connected through Tailscale" (one of
  their devices was seen on the tailnet; checked every minute).
- **Remove** adds: cancel the Tailscale invite, remove their devices, remove them from the
  tailnet, and update the access rules. Each is a checklist step; a failed one says to finish
  it in the admin console.

## State

Migration 5 adds to `people`: `tailscale_login` (unique, case-insensitive; filled from the
email for existing people), `remote_invite_id`, `remote_invite_url` and `connected_at`.

## API changes

| Method and path | Change |
|---|---|
| `POST /api/people` | Returns `{id, url, tailscaleUrl?, tailscaleError?}` instead of `{url}` |
| `PUT /api/people/{id}` | Accepts `tailscaleLogin` |
| `GET /api/people` | Each person adds `tailscaleLogin`, `remoteInviteUrl`, `connectedAt` |
| `GET /api/bootstrap` | Over the tailnet: `{remote: true, login, known}` |
| `GET /api/me` | Adds `tailscale: true` when signed in through Tailscale |

Settings adds `TS_OAUTH_CLIENT_ID` and `TS_OAUTH_CLIENT_SECRET` (used after a restart).
`TS_AUTHKEY` still works without them, but then nobody can be invited and the rules aren't
managed.

## Not verified yet

- Tailscale documents reading and deleting user invites but not creating them. `tsapi.Invite`
  uses `POST /api/v2/tailnet/-/user-invites` with `[{"role":"member","email":...}]`. If it
  fails, invite people from the admin console and set their Tailscale login under People;
  everything else works the same.
- How many users and tagged devices the free plan allows.

## Try it on your Mac

See the try-out file in the project: create the tailnet, the `tag:homelab` tag and the OAuth
client, paste the client into Settings, set `addresses: tailscale`, restart, then invite a test
person and open an app from a phone on mobile data.
