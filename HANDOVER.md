# Homelab handover

Read this first instead of reading the whole repo. It says what Homelab is, where each part
lives, what state it is in, and what is still open. How each milestone was built is in
[docs/history/](docs/history/); look there only when you need that detail.

**Keeping it current (a rule for everyone, people and AI helpers):** don't edit the text
inside the `feature:` and `open:` markers (hidden HTML comments). Instead add a note in
`handover/notes/<feature>/` (see [handover/README.md](handover/README.md)). When the feature
is done, mark the note `ready`. A bot then opens a `docs: fold handover notes` PR that copies
the note into this file. Notes that are still waiting are part of the current state, so read
`handover/notes/` too. Everything outside the markers can be edited directly. Don't add PR
logs or dated changelogs. Keep the section numbers, other docs point to them.

## Open work
<!-- open:databases -->
- **Databases, next (proposed):** a PIN before any change, and Edit / Delete / Add row buttons
  instead of typing SQL, with the SQL box read only. Rewind (point-in-time restore) is a later
  idea, since copies are already made often.
<!-- /open:databases -->
- **Contributor docs and AI reviewer** (being built): `CONTRIBUTING.md`, `AGENTS.md`,
  `docs/getting-started.md`, `docs/how-it-works.md`, and a Claude review on every PR.
<!-- open:files -->
- **Files, next:** expiring and one-time upload links, resumable uploads, image versions.
<!-- /open:files -->
<!-- open:data-api -->
- **Data API:** save and read records with plain web calls. Planned next.
<!-- /open:data-api -->
- **Ideas, not agreed:** logs with search, alerts to phones from apps, scheduled jobs, closing
<!-- new-open-work -->
  ports that aren't needed. Milestones 7 to 11 (home automation, backups, domain, leaving
  GitHub, appliance) are not started. Backups are deferred.
- Workflow: one branch and one PR per piece of work. Labiba merges. A merge to `main` becomes
  a release `vX.Y.Z` unless it is only docs, chore, test, ci, style, refactor or build. PR titles
  must start with `feat:`, `fix:`, `docs:` ... (`feat!:` is breaking); CI checks it.

## 1. What this is
Homelab is a self-hosted home platform, like an OS for developers running on Docker. You push
code to GitHub, Homelab runs it in dev, you promote the same build to prod, and you roll back
with a tap. Apps don't set up their own login, secrets, files or databases: they ask Homelab.
- **Owner:** Labiba (GitHub `rupaai`). Builds it with Gabriele.
- **Device:** `mars` (Ubuntu 24.04, 6 cores, 16 GB, 238 GB system disk plus a 1 TB SSD at
  `/mnt/storage`). Homelab runs there as the `homelab` system service, with its data in
  `/var/lib/homelab`. The companion is at `http://192.168.1.183:7080` on the home network.
- **Mac:** for developing Homelab only. A Mac run keeps its data in `~/.homelab`.
- **First app:** Northern Lights, the movie app in `rupaai/smart-recommender-player`
  (app id `northern-lights`, with Jellyfin and Prowlarr next to it).

## 2. Where things live
- `cmd/homelabd/`: the program. Commands `run`, `setup`, `update-guard`, `system-report`,
  `recover`, `password`, `version`. `config.go` reads `config.yaml`.
- `internal/core/`: shared types and the contracts (interfaces) between the core and adapters.
- `internal/hub/`: the core. Desired state, the reconciler, and the actions (add app, deploy,
  promote, rollback, people, keys, alerts).
- `internal/state/`: desired state and history in one SQLite file, `homelab.db`.
- `internal/api/`: the JSON API the companion uses, the app door for app keys, sign-in, and
  the companion's static files.
- `internal/adapters/`: one folder per outside thing. `docker` (Compose), `github` (source,
  builds, ghcr images), `gitmirror`, `httphealth`, `local` (ports), `pocketid` (people and
  sign-in), `services` (starts and talks to homelab-services), `selfupdate`, `smtp`, `sysinfo`,
  `sysres`, `webpush`, `secretstore` (old secrets store, only imported now), `fake` (for tests).
- `internal/manifest/`: reads an app's `homelab.yaml`. `internal/identity/`: signed headers
  that tell an app who is using it. `internal/sharedir/`: lets containers write to their folders.
- `services/homelab-services/`: the Rust program for platform services (files, secrets,
  databases). homelabd starts it next to itself.
- `web/`: the companion (SvelteKit, built to static files and embedded in homelabd).
- `plugins/`: thin libraries for apps in TypeScript, Python and Rust.
- `docs/`: guides for app developers. `docs/history/`: one doc per milestone.
- `scripts/build-release.sh`: the release build. `.github/workflows/`: CI and releases.
- Plans and decisions outside the repo: the project's shared files, `homehub/` folder
  (`milestones.md`, `blocks/*_decisions.md`, `databases_plan.md`, `os_basics_ideas.md`).

## 3. State of each part
<!-- feature:deploy-loop -->
- **Deploy loop** (milestone 1): add an app by `owner/name`, builds from GitHub Actions, dev
  and prod, promote, rollback, health checks, logs. Done.
<!-- /feature:deploy-loop -->
<!-- feature:apps-no-setup -->
- **Apps with no setup** (milestone 2): promotion modes `manual`, `auto-dev`, `auto` with
  automatic rollback, a mirror of each app's code, signed identity headers, extra containers
  (`services:` in `homelab.yaml`, see [docs/services.md](docs/services.md)). Done.
<!-- /feature:apps-no-setup -->
<!-- feature:people-signin -->
- **People and sign-in** (milestone 3): owner, admins and members, per-app access, invites
  (also by email, from any device), passkeys through Pocket ID, password sign-in, recovery
  code and `homelabd recover`. Done. Labiba found sign-in still feels like a prototype.
<!-- /feature:people-signin -->
<!-- feature:access -->
- **Access from anywhere** (milestone 4): removed. No Tailscale. Homelab uses
  `addresses: local` (Mac) or `addresses: ip` (device).
<!-- /feature:access -->
<!-- feature:know-whats-happening -->
- **Know what's happening** (milestone 5): push notifications, alerts, Resources screen,
  activity. Done.
<!-- /feature:know-whats-happening -->
<!-- feature:on-the-device -->
- **On the device** (milestone 6): `homelabd setup` installs it; it updates itself from the
  companion (Settings → Resources: Update, or try a test version then Keep) and goes back on
  its own if a new version doesn't stay up. Done. Versions: `build-N` or `vX.Y.Z` (any `v` is
  newer than any `build`); test versions `pr-N-<commit>` or `vX.Y.Z-pr.N[.<commit>]`
  (`internal/adapters/selfupdate/version.go`). Releases are still named `build-N`. It checks
  GitHub for a new version every hour; Refresh on that screen checks right away.
<!-- /feature:on-the-device -->
<!-- feature:api-keys -->
- **API keys and users API**: every app environment gets `HOMELAB_TOKEN` and
  `HOMELAB_API_URL` automatically; admins make keys for outside servers. Done. See
  [docs/platform-services.md](docs/platform-services.md).
<!-- /feature:api-keys -->
<!-- feature:files -->
- **Files**: folders, uploads, downloads, photo/video/audio/PDF info read on upload, search by
  it, duplicates skipped, delete an empty folder, Files screen. `storage:` in `config.yaml` puts
  files on a disk (mars: `/mnt/storage/homelab`). Done. OpenAPI: `docs/files.openapi.yaml`.
<!-- /feature:files -->
<!-- feature:secrets -->
- **Secrets**: encrypted in homelab-services, key in `<home>/secrets/master.key`. Set in the
  companion. Only the owner, and people given "Can see secrets", can see values. API keys
  never read secrets. Views are logged, 5 versions kept, hidden in logs. Apps get them as files
  in `/run/secrets` (and as environment variables unless `secrets_as: files`).
  `generate_secrets` makes them automatically. Apps can save their own through the API. Done.
<!-- /feature:secrets -->
<!-- feature:plugins -->
- **Plugins**: files, secrets, databases (read only) and releases in TypeScript, Python and Rust, e.g.
  `Homelab().secrets.get("X")`. Not published to package registries yet. See
  [plugins/README.md](plugins/README.md).
<!-- /feature:plugins -->
<!-- feature:databases -->
- **Databases**: `database: postgres` or `sqlite` in `homelab.yaml` gives each environment its
  own database and `DATABASE_URL` (or point at a SQLite file the app already has). Copied before
  every promote and before SQL changes to prod. Copy now, Copy prod to dev, Restore, Import.
  Connect from the Mac over ssh. Databases screen with tables, rows and a read-only SQL box
  (changes only for Postgres, when switched on). Done. OpenAPI: `docs/db.openapi.yaml`.
<!-- /feature:databases -->
<!-- feature:release-versions -->
- **Release versions**: `homelab-services release <prod|dev|test>` picks the next `vX.Y.Z` (or `-dev.N` / `-pr.N`) from PR titles and makes the GitHub release; works in any repo's CI. Every app gets versions with no repo change (dev `v0.4.0-dev.1`, prod `v0.4.0`), shown in the companion and set as `HOMELAB_VERSION`; GitHub refusing never blocks a deploy. Apps list theirs with `releases:read`. See [docs/platform-services.md](docs/platform-services.md).
<!-- /feature:release-versions -->
<!-- feature:own-releases -->
- **Homelab's own releases**: `release.yml` runs `homelab-services release` for `vX.Y.Z` (first `v0.20.0`), test versions are `vX.Y.Z-pr.N.<commit>`; the companion shows the update's new/fix counts and notes (older ones were `build-N`).
<!-- /feature:own-releases -->
<!-- new-features -->

## 4. Ports and files on the device
- `7080` companion and API. `7081` Pocket ID (container on `7082`). `7085` app door (apps reach
  it as `host.docker.internal:7085`). `7086` homelab-services, loopback only.
- `config.yaml` in the Homelab home: `addresses`, `folders`, `storage`, `max_upload_mb`,
  ports. Unknown settings only warn, so an older version still starts.
- App containers are Compose projects named `homelab-<app>-<env>`.

## 5. Building and testing
- Needs Go, Node 22+, Rust and Docker. `make build` gives `bin/homelabd` (companion inside) and
  `bin/homelab-services`.
- **Before every PR:** `make check` (gofmt, go vet, go test) and `make check-services` (cargo
  fmt, clippy, test). `make test-docker` runs the Docker end-to-end test. In `web/`:
  `npm run check && npm test`. CI runs all of these.
- Run on a Mac: `bin/homelabd run`, companion at `http://localhost:7080`.

## 6. Rules
- **Never touch containers Homelab didn't make.** mars also runs Gabriele's Home Assistant,
  matterbridge, cloudflared, matter-server and tplink-proxy.
- **App secrets are Labiba's.** Nobody sees a value unless Labiba allows it, including AI
  helpers. Never print, log or commit a secret, and never ask for one in chat.
- Keep it simple: the smallest thing that works. No extra layers or setup steps unless asked.
- Every service is a plugin anyone can use: the real work is in Rust (homelab-services), and
  plugins only make the web calls.
- Fix the cause of a bug, not the symptom, and add a test so it can't come back.
- Name it Homelab in code and text ("homehub" was the planning name).
- Write short, plain user-facing text.
- Labiba runs commands on the Mac and mars themselves: give one copy-paste command, not many
  small ones.
