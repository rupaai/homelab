# Deploys, promote and rollback

How a build of an app gets to dev and prod, and how it goes back. Back to [the map](../map.md).

## In short

- An app's CI builds an image. Homelab picks up each passing build and runs that exact image on **dev**.
- **Prod** gets a build only when someone promotes it, or when the app is on `auto` and dev has stayed healthy for a set time.
- A promote copies prod's data first. If the new version does not start or does not stay healthy, Homelab puts the old version back with its data.
- Rollback starts the version before the current one. You do it by hand, or Homelab does it itself for 10 minutes after a promote.
- An admin can restart an environment. Homelab makes fresh containers from the build that environment runs now, so new secrets and settings are picked up. The build does not change.
- An admin can turn dev off for an app, to save memory. Dev stops and stays stopped, its data is kept, and new builds wait until it is turned on. Prod is never touched.
- You see it on the app page in the companion ([apps/[id]/+page.svelte](../../web/src/routes/apps/%5Bid%5D/+page.svelte)), in the promote sheet ([PromoteSheet.svelte](../../web/src/lib/components/PromoteSheet.svelte)) and in the activity log.

## How it works

1. **Add the app.** `POST /api/apps` takes `owner/name`. Homelab reads `homelab.yaml` from the branch (`AddApp` in [actions.go](../../internal/hub/actions.go)). The file is the app's contract, read by [manifest.go](../../internal/manifest/manifest.go).
2. **Watch the builds.** Every 60 seconds (`BuildsEvery`), `CheckBuilds` asks GitHub for the newest build run on the app's `build.branch` and `build.workflow` ([github.go](../../internal/adapters/github/github.go)). A failed build is logged and never deployed.
3. **Make a release.** For a passing build, Homelab reads `homelab.yaml` at that commit, finds the image digest in the registry (`Registry.Resolve`), and stores the commit, digest and manifest. A commit that changes the app `id` is refused.
4. **Put it on dev.** Unless the app is `manual`, the release becomes dev's wanted version. Homelab then asks for a dev version (see [releases.md](releases.md)).
5. **Reconcile.** Every 15 seconds (`ReconcileEvery`), the reconciler in [hub.go](../../internal/hub/hub.go) (`Reconcile`) looks at Docker once for all containers, then checks each app under its lock (`observeApp`). If an environment runs another digest, has stopped, or a service next to it stopped, Homelab deploys it.
6. **Deploy.** `deploy` builds the spec, then `Prepare` writes the files and pulls the image. Then `Deploy` runs `docker compose -p homelab-<app>-<env> up -d --remove-orphans` in `<home>/apps/<app>/<env>/` ([docker.go](../../internal/adapters/docker/docker.go), [compose.go](../../internal/adapters/docker/compose.go)). Each environment also gets its address (see below).
7. **Check health.** Every 30 seconds (`HealthEvery`), `CheckHealth` calls `http://127.0.0.1:<host port><health path>` (an app on the host network is called on its own port, see [Hardware](hardware.md)) with a 5 second timeout ([health.go](../../internal/hub/health.go), [httphealth.go](../../internal/adapters/httphealth/httphealth.go)). The result is healthy, degraded or down. A change is written to the activity log.
8. **Auto-promote.** On the same 30 second round, `AutoPromote` looks at apps on `auto` ([auto.go](../../internal/hub/auto.go)). It promotes only when all of these are true: dev is healthy for `promote_after` (default 30 minutes), the smoke test passed (if the app has `smoke_test`), prod has all its secrets, and the commit is not held.
9. **Promote.** `promote` in [actions.go](../../internal/hub/actions.go) does this in order:
   - stamps prod's version and prepares the image;
   - checks that the data copy can work, before anything stops;
   - stops prod's app (its extra containers keep running);
   - copies the app's data folders, and dumps Postgres if the app uses it, to `<home>/backups/<app>/prod/<time>-<label>`;
   - sets the wanted version, starts it, and waits up to 3 minutes for healthy.
10. **Undo a failed promote.** If the new version does not start or does not become healthy, `undoPromote` starts the old version with the copy made just before the switch.
11. **Watch after a promote.** For 10 minutes (`WatchFor`), prod is checked. Two unhealthy checks in a row trigger an automatic rollback (`autoRollback` in `auto.go`). The bad commit is held, the data is kept, and an alert is sent.
12. **Roll back by hand.** `Rollback` finds the newest earlier release that is not held (`previous` in `actions.go`) and starts it. It does not copy data back.
13. **Clean up images.** After a healthy start, `removeOldImages` deletes the app's images that no container uses. It keeps what each environment runs now and what is in its recent history (`removeOldImages`, `actions.go`).
14. **Mirror the code.** Every 10 minutes (`MirrorEvery`), `SyncMirrors` (in [manage.go](../../internal/hub/manage.go)) updates a full read-only git copy of each repo ([gitmirror.go](../../internal/adapters/gitmirror/gitmirror.go)).
15. **Restart an environment.** The Restart button on an environment card ([RestartSheet.svelte](../../web/src/lib/components/RestartSheet.svelte)) calls `RestartEnv` ([actions.go](../../internal/hub/actions.go)). It needs the app's lock, an idle environment, and something running there. It deploys the release the environment runs now, with `Recreate` set. Compose then runs `up -d --remove-orphans --force-recreate` (`upArgs` in [docker.go](../../internal/adapters/docker/docker.go)), so the app, its extra services and its database container are all made again. The wanted release does not change, and no data is copied. The wait for health is the same as a promote's. Activity records `restart.started`, then `restart.done`, `restart.failed` or `restart.unhealthy`.
16. **Turn dev off and on.** "Turn off" on the dev card ([TurnOffSheet.svelte](../../web/src/lib/components/TurnOffSheet.svelte)) and "Turn on" call `POST /api/apps/{id}/envs/dev/off` and `.../on` (admins only, `envOff` in [apps.go](../../internal/api/apps.go)). Both go to `SetEnvOff` ([actions.go](../../internal/hub/actions.go)), which sets `envs.off` in the state file (default on), writes `env.turned_off` or `env.turned_on` to the activity log, and wakes the reconciler. Prod returns a plain 400.
    - **Reconcile:** while dev is off, `reconcileEnv` calls `reconcileOff` ([hub.go](../../internal/hub/hub.go)). It stops the whole compose project (the app and services like Jellyfin) with `Stop`, keeps containers, data, secrets and the database, removes dev's address, and never starts it.
    - **New builds:** they are still saved as dev's wanted version (`deploy.waiting` in the log), but nothing starts. Turning dev on starts the newest one.
    - **Health and alerts:** dev is not checked while off, so it is never down and sends no alert (alerts are for prod only). The app view gives `off: true`, state `off` and the detail "Turned off".
    - **Promote:** dev being off does not block a promote. The preview has `devOff: true` and a warning, "Dev is off, so this build wasn't tested in dev." It still blocks when nothing was ever deployed to dev. Auto-promote waits ("Dev is off") and starts counting again once dev is on.
    - **Refused with "Dev is off. Turn it on first.":** restart, rollback, and database copies, restores and imports for dev. Logs and secrets still work.

### Addresses and ports

- Each environment is published on `127.0.0.1` at a port from `hostPort` in `hub.go`: `17000 + slot*10`, plus 1 for prod. Slots are given per app.
- Names: `movies` for prod and `movies-dev` for dev (`AddressName` in [manifest.go](../../internal/manifest/manifest.go)).
- [addresses.go](../../internal/hub/addresses.go) serves those names through a proxy. It checks that the person may use the app before it forwards a call (dev also needs the "projects" grant).
- [internal/adapters/local/local.go](../../internal/adapters/local/local.go) gives each name its own port on the device, and keeps them in `ports.json`. The companion itself is on 7080.

### Who may do what

- Adding an app, promotion mode, removing an app: admins (`ActionManage`, [api.go](../../internal/api/api.go)).
- Deploy, promote, rollback: the "projects" right. Admins have it, and builders have it for the apps they were given the build grant for (`Can` in [access.go](../../internal/hub/access.go)).
- Logs and activity for an app: admins and builders with the build grant.

## Deep dive

**Files**

| File | What it does |
|---|---|
| [internal/manifest/manifest.go](../../internal/manifest/manifest.go) | Reads `homelab.yaml`: image, build, health, promotion, data, env, services, ports, `smoke_test`, `promote_after`, `database`. Sets defaults and checks them |
| [internal/manifest/manifest_test.go](../../internal/manifest/manifest_test.go) | Tests for the manifest rules |
| [internal/hub/hub.go](../../internal/hub/hub.go) | The config defaults, `Run` (the loops), `Reconcile`, `reconcileEnv`, `spec`, `mounts`, `deploy` |
| [internal/hub/actions.go](../../internal/hub/actions.go) | `AddApp`, `CheckBuilds`, `release`, `DeployDev`, `setWantAndApply`, `applyAndWait`, `promote`, `undoPromote`, `Rollback`, `previous`, `removeOldImages` |
| [internal/hub/health.go](../../internal/hub/health.go) | `CheckHealth`, `checkEnv`, `waitHealthy` |
| [internal/hub/auto.go](../../internal/hub/auto.go) | `SetPromotion`, `AutoPromote`, `autoPromoteApp`, `smokeTest`, `watchStep`, `autoRollback` |
| [internal/hub/manage.go](../../internal/hub/manage.go) | `RemoveApp`, `SyncMirrors` (and the secrets the promote check needs) |
| [internal/hub/backupdata.go](../../internal/hub/backupdata.go) | Copies an environment's data folders before a promote, keeps the newest copies |
| [internal/hub/addresses.go](../../internal/hub/addresses.go) | The proxy for app addresses, and the sign-in check |
| [internal/hub/view.go](../../internal/hub/view.go) | The app and environment view the companion reads |
| [internal/adapters/docker/compose.go](../../internal/adapters/docker/compose.go) | Writes the Compose file: the `app` service, extra `services:`, Postgres |
| [internal/adapters/docker/docker.go](../../internal/adapters/docker/docker.go) | `Prepare`, `Deploy`, `StopApp`, `Observe`, `ObserveApp`, `docker compose` calls |
| [internal/adapters/docker/smoke.go](../../internal/adapters/docker/smoke.go) | Runs the smoke test in the app's image against dev |
| [internal/adapters/docker/docker_test.go](../../internal/adapters/docker/docker_test.go) | Tests for the Compose output and the Docker calls |
| [internal/adapters/docker/smoke_test.go](../../internal/adapters/docker/smoke_test.go) | Tests for the smoke test runner |
| [internal/adapters/github/github.go](../../internal/adapters/github/github.go) | Builds, commits, file reads, the registry digest and pull credentials |
| [internal/adapters/gitmirror/gitmirror.go](../../internal/adapters/gitmirror/gitmirror.go) | The read-only repo mirror |
| [internal/adapters/httphealth/httphealth.go](../../internal/adapters/httphealth/httphealth.go) | The HTTP probe used by health checks |
| [internal/adapters/local/local.go](../../internal/adapters/local/local.go) | Local address names and their ports |
| [internal/api/apps.go](../../internal/api/apps.go) | The app handlers (deploy, promote, rollback, logs) |
| [internal/api/api.go](../../internal/api/api.go) | The route table and the action each route needs |
| [internal/hub/access.go](../../internal/hub/access.go) | `Can`: who may do which action |
| [internal/hub/auto_test.go](../../internal/hub/auto_test.go) | Auto-promote, smoke hold, watch and auto rollback tests |
| [internal/hub/hub_test.go](../../internal/hub/hub_test.go), [observe_test.go](../../internal/hub/observe_test.go), [deploydata_test.go](../../internal/hub/deploydata_test.go) | Reconciler, observe and deploy data tests |
| [internal/hub/restart_test.go](../../internal/hub/restart_test.go) | Tests for the environment restart and its refusals |
| [internal/hub/devoff_test.go](../../internal/hub/devoff_test.go) | Tests for turning dev off and on: reconcile, new builds, promote, auto-promote, refusals |
| [web/src/lib/components/TurnOffSheet.svelte](../../web/src/lib/components/TurnOffSheet.svelte) | The confirm sheet for turning dev off |
| [internal/api/restart_test.go](../../internal/api/restart_test.go) | Tests for the environment restart route |
| [web/src/lib/components/RestartSheet.svelte](../../web/src/lib/components/RestartSheet.svelte) | The restart confirm sheet for an environment |
| [web/src/routes/apps/[id]/+page.svelte](../../web/src/routes/apps/%5Bid%5D/+page.svelte) | The app page: environments, deploy, promote, rollback |
| [web/src/lib/components/PromoteSheet.svelte](../../web/src/lib/components/PromoteSheet.svelte) | The promote sheet: what prod runs now, what it will run, the changes |
| [web/src/lib/components/PromotionSheet.svelte](../../web/src/lib/components/PromotionSheet.svelte) | Changes the promotion mode of an app |
| [web/src/lib/components/EnvCard.svelte](../../web/src/lib/components/EnvCard.svelte) | One environment's card on the app page: health and version |
| [docs/services.md](../services.md) | Extra containers (`services:` in `homelab.yaml`) |

**Routes**

- `POST /api/apps`: add an app.
- `GET /api/apps/{id}`: the app view.
- `POST /api/apps/{id}/deploy`: put the latest passing build (or a given commit) on dev.
- `GET` and `POST /api/apps/{id}/promote`: preview, then promote dev's build to prod.
- `GET` and `POST /api/apps/{id}/envs/{env}/rollback`: preview, then roll back an environment.
- `POST /api/apps/{id}/envs/{env}/restart`: restart an environment with fresh containers (admins only).
- `GET /api/apps/{id}/envs/{env}/logs`: the container log of an environment.
- `PUT /api/apps/{id}/promotion`: set `manual`, `auto-dev` or `auto`.
- `DELETE /api/apps/{id}`: remove the app. Both environments stop and its secrets are deleted. Its data folders are kept.
- `GET /api/activity`: the activity log. Use `?app=` for one app.

**Edge cases and rules**

- **One action at a time per app.** Each app has a lock (`appLock` in `hub.go`). A second action gets `ErrBusy`. The reconciler skips an app while an action holds its lock.
- **Build fails:** logged as `build.failed`, not deployed (`CheckBuilds`, `actions.go`).
- **No image yet:** tried again on the next round (`CheckBuilds`).
- **Manifest missing or broken at that commit:** the build is skipped and logged as `build.skipped` (`release`, `actions.go`).
- **Promote checks first:** if the data copy cannot work, prod is not stopped (`promote`, `actions.go`). The message says "Nothing changed".
- **Smoke test failed:** the commit is held, so auto-promote never takes it. Someone can still promote it by hand (`smokeTest`, `auto.go`).
- **Smoke test limits:** the test runs on dev's network with 512 MB of memory, no Linux capabilities, a read-only file system, and a 5 minute limit (`SmokeTimeout`). Container flags are in `smokeArgs` in `smoke.go`.
- **Held commits:** rollback and auto-promote skip them (`previous`, `autoPromoteApp`).
- **Restart refusals:** a restart is refused while the app or environment is busy, and when nothing runs there (`RestartEnv`, `actions.go`). Nothing is stopped first.
- **Restart gap:** the containers are replaced in one `up` call, so the environment is down for a short time.
- **Restart loop guard:** a container that stops again straight after a start waits 1 minute before the next start (`reconcileEnv`).
- **Ports:** `ports:` from `homelab.yaml` are opened for prod only (`compose.go`).
- **Compose names:** the project is `homelab-<app>-<env>` and the app service is `app` (`project` and `serviceName` in `compose.go`).
- **Database copy:** postgres is dumped, SQLite files are copied with the data folders. A copy is made before every promote.
- **Auto-promote bounds:** `promote_after` must be between 1 minute and 7 days (`manifest.go`).

**To change it**

- To change how long dev must stay healthy by default, edit `DefaultPromoteAfter` in [manifest.go](../../internal/manifest/manifest.go). Then run the auto-promote tests.
- To change the health probe, edit `checkEnv` in [health.go](../../internal/hub/health.go). Then run the hub tests.
- To change the Compose file, edit `composeYAML` in [compose.go](../../internal/adapters/docker/compose.go). Then run the docker tests.
- To add a `homelab.yaml` key, add it to the `Manifest` struct and `validate()` in [manifest.go](../../internal/manifest/manifest.go), and add a case to [manifest_test.go](../../internal/manifest/manifest_test.go).
- To change who may promote, edit the route mapping in [api.go](../../internal/api/api.go) or `Can` in [access.go](../../internal/hub/access.go).
- To change what a restart does, edit `RestartEnv` in [actions.go](../../internal/hub/actions.go) and `upArgs` in [docker.go](../../internal/adapters/docker/docker.go). Then run the hub and docker tests.

**Test it**

```sh
make check                              # gofmt, go vet, go test ./... (Go side)
go test ./internal/manifest/... ./internal/hub/... ./internal/api/... ./internal/adapters/docker/... ./internal/adapters/github/... ./internal/adapters/gitmirror/...
make test-docker                        # Docker end-to-end test, needs Docker running
cd web && npm ci && npm run check && npm test
```

Status of this work: [HANDOVER.md](../../HANDOVER.md), section 3 (Deploy loop, Apps with no setup).
