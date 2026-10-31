# People and sign-in

What this page covers: who can use Homelab, how they sign in, how invites work, and how apps learn who is calling. Back to [the map](../map.md).

## In short

- Roles: **owner** (one person, set up first), **admin**, **builder** and **member**. Apps also have a per-app grant: **use** or **build**.
- Each person signs in with a passkey, through Pocket ID. On another device on the home network, the owner can also sign in with a password (only when `addresses: ip` is set).
- New people get an invite link, which the owner can also send by email if email is set up.
- Apps get the signed name and groups of the person, so they know who is using them.
- The owner can get back in with `homelabd recover`, which prints a one-time sign-in link.

Status and open work: [HANDOVER.md](../../HANDOVER.md). Companion screen: `web/src/routes/people/+page.svelte`.

## How it works

1. **Set up the owner.** The first run calls `POST /api/setup` (`setupOwner` in [api.go](../../internal/api/api.go)). It makes the owner, and returns a passkey link and a recovery code, shown once. It works only once: after that the app says "Homelab is already set up" (`SetupOwner` in [people.go](../../internal/hub/people.go)).
2. **Invite someone.** An admin adds a person with a name, email and roles, and per-app grants. Homelab makes an invite link that works for 7 days (`invitePerson` in [people.go](../../internal/hub/people.go)). If email is set up, it also emails the link ([invite_email.go](../../internal/hub/invite_email.go)).
3. **Redeem.** The person opens `/invite/<token>` (see `web/src/routes/invite/[token]/`). Redeeming calls `POST /api/invites/{token}/redeem`, which makes a Pocket ID login link for a passkey, valid for an hour. The person can instead set a password with `POST /api/invites/{token}/password`.
4. **Sign in with a passkey.** The companion goes to `/auth/start`, Pocket ID checks the passkey, and `/auth/callback` checks the ID token and makes a session ([oidc.go](../../internal/api/oidc.go)).
5. **Sign in with a password.** Only the owner can use the owner password, and only from another device when Homelab serves addresses (`addresses: ip`). The owner sets it with `homelabd password`. Invited people can sign in with their email and password (`CheckPersonPassword` in [invite_email.go](../../internal/hub/invite_email.go)). Both go through `POST /api/password-sign-in` ([password.go](../../internal/api/password.go)).
6. **Session.** A session token starts with `hls_` and is stored only as a hash. It is sent as a cookie (`homelab_session`), or as a `Bearer` token for the API. It lasts 30 days, and ends after 7 days without use (`issueSession` and `SessionPerson`).
7. **Check every request.** The rules are in one place: `Can` in [access.go](../../internal/hub/access.go). Each route names an action (apps, logs, secrets, people, settings), and `Can` says yes or no for that person and app.
8. **Tell apps who it is.** When a person opens an app, Homelab adds signed headers (`X-Homelab-User`, `X-Homelab-Name`, `X-Homelab-Groups`, `X-Homelab-Time`, `X-Homelab-Signature`). The app checks them with the public key in `HOMELAB_IDENTITY_KEY` ([identity.go](../../internal/identity/identity.go)). The signing key is kept as a platform secret ([secrets.md](secrets.md)).
9. **Remove someone.** `RemovePerson` turns off their sign-in in Pocket ID, ends their sessions, and removes their local access ([people.go](../../internal/hub/people.go)).
10. **Recover.** `homelabd recover` prints a link for the owner, valid for 15 minutes, that signs them in once ([recover.go](../../cmd/homelabd/recover.go)). The owner can also use the recovery code from setup, on the Homelab computer (`POST /api/recover`, [setup.go](../../internal/api/setup.go)).

Pocket ID runs as a container next to Homelab, from the image pinned in [run.go](../../cmd/homelabd/run.go) (`pocketIDImage`). It listens only on `127.0.0.1`. The adapter is [internal/adapters/pocketid](../../internal/adapters/pocketid/pocketid.go).

## Deep dive

### Roles and access

| Who | Roles | What they can do (from `Can` in `access.go`) |
|---|---|---|
| Owner | `owner`, `admin` | Everything. Only the owner can give or take the `secrets` role. Nobody can make another owner from the screen, and the owner cannot be changed or removed. |
| Admin | `admin` | Manage people, settings, all apps and secrets (not the values, unless also given the `secrets` role). |
| Builder | `builder` | Create projects, and use or build apps they are granted. |
| Member | `member` | Use the apps they are granted. |
| Secrets | `secrets` (extra) | See, set and restore secret values. Only the owner can give it. |

Per-app grants (`Grants` on each person): `use` lets a person open the app, and `build` also lets them deploy it, promote it and see its logs. Only apps that exist can be granted (`validatePersonAccess`).

Roles are sent to apps as `X-Homelab-Groups`, for example `role:owner` (`personGroups`).

### Files

| File | What it does |
|---|---|
| `internal/hub/access.go` | The `Can` rules, one function for every check. |
| `internal/hub/access_test.go` | Tests for the rules. |
| `internal/hub/people.go` | Setup of the owner, invites, redeem, update, remove, roles and grants. |
| `internal/hub/people_test.go` | Tests for people. |
| `internal/hub/invite_email.go` | Sends invite email, checks a person's password, sets an invite password. |
| `internal/hub/manage.go` | The email address and app password settings (`EMAIL_ADDRESS`, `EMAIL_APP_PASSWORD`) and the email test. |
| `internal/state/people.go` | Stores people, invites, sessions and one-time codes. |
| `internal/state/people_test.go` | Tests for the store. |
| `internal/api/people.go` | Routes for people, invites and the recovery link. |
| `internal/api/people_test.go`, `invite_test.go` | Tests for these routes. |
| `internal/api/oidc.go` | Passkey sign-in: start, callback, session. |
| `internal/api/oidc_test.go` | Tests for sign-in. |
| `internal/api/password.go` | Password sign-in from other devices. |
| `internal/api/password_test.go` | Tests for password sign-in. |
| `internal/api/appauth.go` | The app sign-in hand-off: after the person signs in, it gives a 60-second code if they may use the app. |
| `internal/api/setup.go` | First-run owner setup and the recovery code. |
| `internal/identity/identity.go` | Signs the identity headers apps get, and checks them. |
| `internal/identity/identity_test.go` | Tests for signing. |
| `internal/adapters/pocketid/pocketid.go` | Talks to Pocket ID: make and disable users, groups, login links. |
| `internal/adapters/pocketid/proxy.go` | Serves Pocket ID's own pages through Homelab. |
| `internal/adapters/smtp/smtp.go` | Sends email. Reads the address and app password from settings for each message. |
| `cmd/homelabd/recover.go` | `homelabd recover`: prints the one-time owner link. |
| `cmd/homelabd/password.go` | `homelabd password`: sets the owner password for other devices. |
| `cmd/homelabd/main.go` | The commands (`run`, `setup`, `recover`, `password`, `version`). |
| `cmd/homelabd/run.go` | Wires Pocket ID, the mailer and the identity key. |
| `web/src/routes/people/+page.svelte` | The people screen. |
| `web/src/routes/invite/[token]/` | The invite page. |
| `web/src/lib/components/PasskeyRegistration.svelte` | The passkey set-up step. |

### Routes

Sign-in and setup, in `internal/api/api.go`:

- `POST /api/setup`, `POST /api/setup/passkey-link`: first-run owner setup.
- `GET /auth/start`, `GET /auth/callback`: passkey sign-in.
- `GET /auth/app`: app sign-in hand-off (checks the person may use the app, then gives a 60-second code).
- `POST /api/password-sign-in`: password sign-in, other devices only.
- `POST /api/recover`, `GET /recover/{token}`: owner recovery.
- `GET /api/me`, `POST /api/me/passkey-link`: the person signed in, and a new passkey link for them.
- `POST /api/logout`: ends the session.

People (admins), in `internal/api/api.go`:

- `GET /api/people`, `POST /api/people` (invite), `PUT /api/people/{id}`, `DELETE /api/people/{id}`.
- `POST /api/people/{id}/invite`: send the invite again.
- `POST /api/invites/{token}/redeem`, `POST /api/invites/{token}/password`: used by the invite page.

App keys (see [api-keys.md](api-keys.md)): `GET /api/users/me` and `GET /api/users?app=<id>`.

### Edge cases and rules

- **Owner setup works once.** After the owner exists, `POST /api/setup` is refused.
- **Invites last 7 days**, and each is used once. A used or expired invite says "ask the owner to send a new one".
- **Redeeming an invite** and the recovery link work only from the Homelab computer itself (`onThisComputer`). Other devices get `403`.
- **Login links last an hour** (passkey links), and a recovery link 15 minutes.
- **Password sign-in is limited** to 5 tries per 5 minutes per device. The limit is per device, so one device cannot lock out the owner.
- **Recovery code tries** are limited the same way.
- **Passkeys do not work on a bare IP address.** That is why password sign-in exists for `addresses: ip`.
- **Roles.** At least one role is needed. Unknown roles are refused. Only the owner can change the `secrets` role.
- **The owner is protected.** The owner cannot be removed or changed from the screen, and no one else can become owner.
- **Email is optional.** If email is not set up, the invite still works. The owner gets the link to send by hand, with the message "Email isn't set up, so send the link yourself."
- **Sessions** end 30 days after they start, and 7 days after they were last used.
- **Activity.** Invites, removals, resent invites and recovery are written to the activity log.
- **Apps trust the signature, not the headers alone.** A header without a valid signature, or one older than 5 minutes, is not trusted (`MaxSkew` in `identity.go`).

### To change it

- To add a role or action, edit `internal/hub/access.go` (`Can`) and `validatePersonAccess` in `internal/hub/people.go`, then the screen in `web/src/routes/people/+page.svelte`.
- To change invite length, edit the 7-day value in `invitePerson` in `internal/hub/people.go`, and the text in `internal/hub/invite_email.go`.
- To change the session lengths, edit `issueSession` in `internal/api/oidc.go`, and the idle time passed to `SessionPerson`.
- To change what apps get, edit `personGroups` in `internal/hub/people.go` and the header code in `internal/identity/identity.go`.
- To change the owner recovery, edit `cmd/homelabd/recover.go` (and `internal/api/setup.go` for the code).

### Test it

```sh
go test ./internal/identity/... ./internal/hub/... ./internal/api/... ./internal/adapters/pocketid/... ./internal/adapters/smtp/... ./cmd/...
make check
```

`make check` runs gofmt, `go vet` and `go test ./...`. The people screen is checked by `make check-web`.
