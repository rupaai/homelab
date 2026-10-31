# Companion web app

This page covers the companion, the website where you manage Homelab. It covers the code in `web/`, how the built files get into `homelabd`, and how to work on a screen without a running Homelab. For the big picture, see [how Homelab works](../how-it-works.md) and [the map](../map.md).

## In short

- The companion is a SvelteKit app in `web/`. It is built to static files and copied into the `homelabd` binary.
- You open it at `http://localhost:7080` on the device, or at the device's address on your home network.
- It talks to `homelabd` only through the JSON API under `/api/`. The server checks every call.
- Each screen is a folder under `web/src/routes/`, for example Apps, Files, Databases, Logs and Resources.
- For a screen without Homelab running, start the dev server with `?mock` in the address. The screen shows fake data.

## How it works

1. `npm run build` in `web/` runs Vite, which builds the app into `web/build/`. Then `web/scripts/embed.mjs` copies that folder to `internal/companion/build/`.
2. Go embeds that folder (`//go:embed all:build` in `internal/companion/companion.go`). If the folder has no `index.html`, Go serves a short placeholder page instead.
3. `homelabd` gives the embedded files to the API server (`cmd/homelabd/run.go`, `companion.Site()`). `internal/api/static.go` serves them. Any path that is not a file gets `index.html`, so the app's own router handles the address.
4. When the page loads, `web/src/routes/+layout.svelte` calls `GET /api/bootstrap`. That decides the first screen: first-time setup, password sign-in, or the app.
5. After sign-in, the sign-in token is kept in `localStorage` under the key `homelab_session` (`web/src/lib/token.ts`). Every call sends it as a `Bearer` header.
6. Calls that change something also send the header `X-Homelab-Request: 1`. The server uses it to block cross-site form posts (`web/src/lib/api.ts`, `authHeaders`).
7. `/api/me` tells the page who you are (name, roles, and `canSeeSecrets`). The server still checks each call.
8. Screens that show live data reload it with `startPolling` in `web/src/lib/poll.ts`. It pauses while the tab is hidden and catches up when you return. The Home and Logs screens use it.
9. Push notifications are handled by `web/src/service-worker.ts` and `web/src/lib/push.ts`. The service worker caches nothing. It only shows notifications and opens the right screen when one is tapped.

Config keys that change the companion:

- `listen` in `config.yaml` sets the address the companion listens on. The default is `127.0.0.1:7080` (`cmd/homelabd/config.go`). Set `addresses` to `ip` so other devices on the home network can reach it.
- `services_port` changes the internal port of `homelab-services`. It does not change the companion (see [services program](services-program.md)).

Who may do what: the server decides. The owner, admins, builders and users see different things. The companion shows what the server returns.

## Deep dive

**Files**

| File | What it does |
|---|---|
| `web/src/routes/` | One folder per screen. See the list below. |
| `web/src/routes/+layout.svelte` | The shell: first-time setup, sign-in, the top bar and tab bar, and the session check. |
| `web/src/routes/+layout.ts` | Turns off server rendering and prerendering (`ssr = false`, `prerender = false`). |
| `web/src/lib/api.ts` | The one API client. Chooses the real calls or the mock (`useMock`). Adds the auth and CSRF headers. |
| `web/src/lib/api.test.ts` | Tests the real client against fake responses. |
| `web/src/lib/types.ts` | The types of every API answer, and the `Api` interface both clients follow. |
| `web/src/lib/mock.ts` | Fake data and a fake `mockApi` for dev mode. Dropped from production builds. |
| `web/src/lib/mock.test.ts` | Checks the mock data. |
| `web/src/lib/poll.ts` | `startPolling`: repeat a call on a timer, pause while hidden. |
| `web/src/lib/token.ts` | Keeps the sign-in token in `localStorage` (and in memory if storage is blocked). |
| `web/src/lib/session.ts` | Tells the layout when the API says the session is gone, so it can show sign-in. |
| `web/src/lib/push.ts` | Asks permission, subscribes the device, and checks that push can work here. |
| `web/src/service-worker.ts` | Shows push notifications and opens the matching screen on tap. |
| `web/src/app.html` | The page shell. |
| `web/static/` | Icons and the web app manifest, copied as they are. |
| `web/vite.config.ts` | Vite and SvelteKit setup. Sends `/api` and `/healthz` to `127.0.0.1:7080` in dev. Adds the Vitest test pattern. |
| `web/package.json` | The scripts: `dev`, `build`, `embed`, `check`, `test`. |
| `web/scripts/embed.mjs` | Copies `web/build` to `internal/companion/build`. |
| `web/scripts/make-icons.mjs` | Draws the home-screen icons as PNG files into `web/static/`. |
| `internal/companion/companion.go` | Embeds the built files and falls back to the placeholder. |
| `internal/companion/placeholder.html` | The page shown when the app was not built. |
| `internal/companion/build/.keep` | The only file of that folder in Git. Built files are not committed. |
| `internal/api/static.go` | Serves the files, adds the Content-Security-Policy and cache headers, and falls back to `index.html`. |
| `internal/api/api.go` | Builds the API routes, and mounts the static files on `/`. |

Screens (folders under `web/src/routes/`):

- `home`, `apps` (with `apps/[id]`, its logs, and its secrets), `databases` (per app and environment), `files` (per folder), `logs`, `activity`, `people`, `resources`, `settings` (with `settings/keys`), `me`, and `invite/[token]`.
- `web/src/routes/+page.svelte` is the page at `/`. It loads your projects and your account (`api.projects()` and `api.me()`).

**Routes**

The companion calls these API routes. The full list is in `internal/api/api.go`. The request and response shapes are in [platform-services.md](../platform-services.md) and the OpenAPI files in `docs/`.

- `GET /api/bootstrap`, `GET /api/me`, `POST /api/logout`: setup and sign-in state.
- `GET /api/apps`, `GET /api/projects`, `GET /api/apps/{id}`: the apps list and details.
- `GET /api/resources`, `GET /api/device`, `POST /api/device/update`: the Resources screen and updates.
- `POST /api/push/subscriptions`, `GET /api/push/key`, `POST /api/push/test`: notifications for this device.
- `/api/files`, `/api/db`, `/api/data`, `/api/logs`: forwarded to `homelab-services`. See [services program](services-program.md).
- `GET /` and every other page path: the static files (`internal/api/static.go`).

**Edge cases and rules**

- Dev mode only: `useMock` is `import.meta.env.DEV` plus the `?mock` flag or `VITE_MOCK=1`. A production build never sees mock data, because the import is inside the dev check (`web/src/lib/api.ts`).
- The mock is loaded with a dynamic `import('./mock')`. The import is only evaluated in dev.
- Push needs a secure connection (https). Over plain http the page says so. On iPhone, the app must be added to the Home Screen first. `pushUnsupported()` in `web/src/lib/push.ts` returns the reason.
- A 403 with the sign-in message clears the token and sends you to sign-in (`failure()` in `web/src/lib/api.ts`).
- If the browser blocks storage, the token is kept in memory for the page. It is lost on reload.
- `index.html` is never cached. Files under `_app/immutable/` are cached for a year (`internal/api/static.go`).
- The Content-Security-Policy allows scripts only from the site itself, and the one inline bootstrap script by its hash. Do not add inline scripts to `index.html` without a reason. `static.go` computes the hash from the file.
- Non-GET and non-HEAD requests to the static files get `405`.

**To change it**

- To add a screen, add a folder with `+page.svelte` under `web/src/routes/`, then add a link in the menu in `+layout.svelte`. Test with `make check-web`.
- To add a call to the API, add the method to the `Api` type in `web/src/lib/types.ts`, the real version in `web/src/lib/api.ts`, and a fake in `web/src/lib/mock.ts`. Add a test in `web/src/lib/api.test.ts`.
- To add a new route on the server, add it in `internal/api/api.go` (or `device.go`, `apps.go`, and so on, with the handler). Add a Go test in `internal/api/`.
- To change the headers or the Content-Security-Policy, edit `internal/api/static.go`, then test with `go test ./internal/api/...`.
- To change how often a screen reloads, change the `interval` function passed to `startPolling` on that screen.
- After any change in `web/`, rebuild `homelabd` with `make build`. The companion files are embedded when the Go program is built, so a running `homelabd` keeps the old files until it is rebuilt and restarted.
- To change the icons, edit `web/scripts/make-icons.mjs`, then run it with `node web/scripts/make-icons.mjs`. It writes into `web/static/`.

**Test it**

- `make check-web`: installs with `npm ci`, then runs `npm run check` (svelte-check, with the types) and `npm test` (Vitest, `src/**/*.test.ts`). CI runs the same (`companion` job in `.github/workflows/ci.yml`).
- `cd web && npm run build`: builds and embeds the app. CI also runs this.
- `make web`: the same build, with `npm ci` first.
- `go test ./internal/api/...`: the API and the static file server.
- `make test` runs `check`, `check-services`, `check-web` and `check-handover` (see the `test:` line in the [Makefile](../../Makefile)). Run it before you say the work is done.

## Work on a screen without Homelab running

Use mock mode. You do not need `homelabd`, Docker or a device.

1. Install the packages once: `cd web && npm ci`.
2. Start the dev server: `npm run dev`. Vite prints its address when it starts (5173 by default, since `vite.config.ts` sets no port).
3. Open the address with `?mock` at the end, for example `http://localhost:5173/?mock`. Or start with `VITE_MOCK=1 npm run dev` and open the address without `?mock`.
4. You are signed in as `Rupa`, the owner. The fake apps are Movie Night, Recipe Box and Garden Log (`web/src/lib/mock.ts`). Each fake call waits about 120 ms.
5. The mock keeps its changes in memory (`web/src/lib/mock.ts`). A page reload resets the mock data.

Without `?mock`, the dev server sends `/api` and `/healthz` to `http://127.0.0.1:7080`. If `homelabd` is not running there, those calls fail with a network error. To run it against a test folder, start `homelabd run --home <a test folder>` (see `cmd/homelabd/run.go`). Do not point it at the real device's home folder.

Mock mode does not check the server's rules. A screen that works in mock can still fail against the real server. Check it once against a running `homelabd`.
