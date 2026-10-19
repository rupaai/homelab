# Platform services

Homelab gives apps, and servers outside Homelab, a small API: people (here), and later files, databases and data. Every call uses an **API key**.

## API keys

A key looks like `hlk_` plus 43 characters. Homelab keeps only a SHA-256 of it, so a lost key can't be shown again: make a new one.

There are two kinds.

**App keys.** Every app environment gets one automatically, in two settings:

- `HOMELAB_TOKEN`: the key.
- `HOMELAB_API_URL`: where to use it, `http://host.docker.internal:7085`.

An app key acts for its own app and environment only. It can read the people of its app, and (later) its own database, data and files. It's replaced when the app is removed and added again. It only works at `HOMELAB_API_URL`, not on the companion's address.

**Admin keys.** For a server outside Homelab, such as a photo service on another computer. An admin makes one in the companion under **Settings → API keys**, picks what it may do, and copies it once. It works on the companion's own address. It stops working when it's revoked, when it expires, or when the person who made it is no longer an admin.

### Scopes

| Scope | Lets the key |
|---|---|
| `users:read` | read people |
| `files:<folder>:read` / `:write` | read / save files in a folder (coming) |
| `db:<app>:read` / `:write` | read / change an app's database (coming) |
| `data:<app>:read` / `:write` | read / change an app's data (coming) |

Write includes read.

### The app door

Apps call Homelab on their own door, separate from the companion. The door takes app keys only: no sign-in, no cookies, no companion.

- **Linux:** the door listens on Docker's bridge address (usually `172.17.0.1`). Apps reach it as `host.docker.internal`. The home network can't.
- **Mac:** Docker runs in a small virtual machine that already forwards `host.docker.internal` to the Mac. The door listens on `127.0.0.1:7085`.

Change the port with `app_api_port: 7085` in `config.yaml`.

Apps still can't reach each other: each app environment keeps its own private network.

### Rules for every key call

- Send the key as `Authorization: Bearer <key>`.
- Cookies and `X-Homelab-*` headers on a key call are dropped.
- 20 wrong keys from one address in 10 minutes blocks that address for a while.
- Every key call goes in the activity log. If a key makes the same read again and again, it's logged once every 10 minutes. Every change is always logged.

## Users API

| Call | Gives |
|---|---|
| `GET /api/users/me` | who is calling: a person, an app (`{"kind":"app","app","env"}`), or a key and the person who made it |
| `GET /api/users?app=<id>` | the people with access to an app, with `access`: `admin`, `build` or `use`. Without `app`, everyone (admins and admin keys only) |
| `GET /api/users/stats` | `{"total", "by_role", "active_7d", "apps"}`. `apps` is how many people can use each app |

An app key always gets its own app, and may leave `app` out.

```sh
# From inside an app container
curl -s -H "Authorization: Bearer $HOMELAB_TOKEN" "$HOMELAB_API_URL/api/users"
```
