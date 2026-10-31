# Alerts and resources

This page covers two things that go together: the alerts that Homelab sends to phones and browsers (web push), and the Resources screen that shows how busy the device is. It also covers the device card, which shows the computer's health and whether a newer Homelab is out. For the big picture, see [how Homelab works](../how-it-works.md) and [the map](../map.md). For current state, see [HANDOVER.md](../../HANDOVER.md).

## In short

- Homelab sends web push notifications for four things: an app's prod is down, an app is back, an app was rolled back, and a disk is nearly full. A test button sends one more.
- The people who get an app alert are the app's builders and the admins. Disk alerts go to admins only.
- Each problem is told once. Homelab keeps the state in its database, so a restart does not repeat it.
- The Resources screen shows the computer's CPU and memory, its disks, and each app environment's usage.
- The device card shows the computer's system details, pending OS updates, and Homelab's own version. It can also show test versions.

## How it works

1. The companion asks `GET /api/push/key` for Homelab's public VAPID key (the key that identifies Homelab to the browser's push service).
2. On the Me screen (`web/src/routes/me/+page.svelte`), you turn notifications on for a device. The browser asks for permission. `web/src/lib/push.ts` subscribes the device and sends the subscription to `POST /api/push/subscriptions`. Homelab accepts only `https` endpoints without a user name or port (`validPushEndpoint` in `internal/hub/alerts.go`).
3. Homelab makes its VAPID key pair on the first start. `pushKeys` in `cmd/homelabd/run.go` stores the two keys in the secrets store under `HOMELAB_PUSH_PRIVATE_KEY` and `HOMELAB_PUSH_PUBLIC_KEY`.
4. The health loop in `internal/hub/hub.go` runs `checkAlerts` every health check. It looks at each app's prod environment and at the disks.
5. When a problem is new, `internal/hub/alerts.go` picks the people who can act, and `notify` sends the message through `internal/adapters/webpush/webpush.go`. The state of each alert is kept with `OpenAlert`, `MarkAlertSent` and `CloseAlert` in the store, so each problem is sent once.
6. The browser gets the push. `web/src/service-worker.ts` shows it. Tapping it opens the matching screen (for example `/apps/<id>` or `/resources`) in an open window of Homelab, or in a new one. SvelteKit builds the service worker to `/service-worker.js`, the path `push.ts` registers.
7. The Resources screen calls `GET /api/resources`. `Hub.Resources` in `internal/hub/alerts.go` joins the device reading (`sysres`) with the per-app reading. A part that cannot be read is left out, and `problems` says why.
8. The device card calls `GET /api/device`. It joins `sysinfo` (the system) with the update check. `?check=1` asks GitHub for the newest Homelab right now.

The device details come in two parts:

- Things that need no root come from the running program: the OS, uptime, temperature and the disks.
- Things that need root come from a report file, `/run/homelab/system.json` (`ReportPath` in `internal/adapters/sysinfo/sysinfo.go`). A systemd timer that `homelabd setup` installs runs `homelabd system-report` as root every few minutes. The report counts as stale after an hour (`MaxReportAge`). Without it, disk health and the pending-update count show as unknown.

Config keys: none change alerts or resources. The thresholds are constants in `internal/hub/alerts.go`.

## Deep dive

**Files**

| File | What it does |
|---|---|
| `internal/hub/alerts.go` | Finds the problems, picks the audience, and sends. Also `Resources` and the push subscription methods. |
| `internal/hub/alerts_test.go` | Tests the alerts. |
| `internal/hub/hub.go` | Starts the health loop that calls `checkAlerts` (`Run`). Holds the `Resources` adapter. |
| `internal/hub/auto.go` | Calls `alertRollback` after an automatic rollback. |
| `internal/hub/device.go` | The device card, the update, and the test versions. |
| `internal/hub/disks.go` | The list of disks Homelab knows (the data disk, storage disks, and folders). |
| `internal/api/device.go` | HTTP handlers for logs, activity, push, resources, device and test versions. |
| `internal/api/services.go` | The `storage` handler (`GET /api/storage`). |
| `internal/adapters/webpush/webpush.go` | Sends the pushes (VAPID, RFC 8030, 8291 and 8292). Has no test file of its own. |
| `internal/adapters/sysres/sysres.go` | Reads CPU and memory, a disk's space, and each app's container usage from Docker (`docker ps` and `docker stats`). Only Homelab's own containers are read. |
| `internal/adapters/sysres/parse.go` | Reads the `docker stats` lines. |
| `internal/adapters/sysres/sysres_test.go` | Tests the readings. |
| `internal/adapters/sysinfo/sysinfo.go` | The device details, and the root report that `system-report` writes. |
| `internal/adapters/sysinfo/sysinfo_test.go` | Tests the device details. |
| `cmd/homelabd/run.go` | Makes the push keys (`pushKeys`) and passes the notifier and the resources adapter to the hub. |
| `cmd/homelabd/setup.go` | Installs the systemd timer for `system-report`. |
| `web/src/service-worker.ts` | Shows push notifications and opens the screen for a tap. Caches nothing. |
| `web/src/lib/push.ts` | Asks permission, subscribes, unsubscribes, and says why push cannot work on a device. |
| `web/src/routes/resources/+page.svelte` | The Resources screen. |
| `web/src/routes/me/+page.svelte` | The Me screen: turns notifications on or off for this device. |

**Which alerts exist**

| Alert | When it is sent | Who gets it | Tag (used to replace an older one) |
|---|---|---|---|
| "<app> is down" | Prod is `down` or `degraded` for 2 minutes (`downAfter`). Sent once per problem. | Builders of the app and admins | `down:<app>` |
| "<app> is back" | Prod is `healthy` again, and a "down" alert was sent. | Builders of the app and admins | `down:<app>` |
| "<app> was rolled back" | Homelab rolled prod back by itself after a promote turned it unhealthy (`autoRollback` in `auto.go`). | Builders of the app and admins | `rollback:<app>` |
| "A disk is almost full" | A disk is 85% or more full. It closes below 80%. Checked every 5 minutes. | Admins | `disk:full` for the data disk, `disk:full:<name>` for a storage disk |
| "Notifications work on this device." | You press the test button (`POST /api/push/test`). | The person who pressed it | `test` |

Disks that are on the same device as an earlier one are skipped, so one disk is not counted twice (`Shared` in `disks.go`).

**Who may do what**

- The API guard in `internal/api/api.go` decides who can call each route. Routes that are not listed there need the manage right (`ActionManage`, the owner and admins). That covers `/api/resources`, `/api/device`, `/api/device/update`, the test versions and `/api/storage`.
- The push routes (`/api/push/...`) need only a signed-in person (`ActionSelf`). Each person turns notifications on for their own devices.
- App alerts go to the people who can work on the app (`appAudience`, which uses `ActionProjects`). Disk alerts go to the people with the manage right (`adminAudience`).

**Routes**

- `GET /api/resources`: current device and app usage (`internal/api/device.go`, `resources`).
- `GET /api/device` and `GET /api/device?check=1`: the device card. `check=1` asks GitHub now.
- `POST /api/device/update`: updates Homelab to the newest release.
- `GET /api/device/tests`, `POST /api/device/test`, `POST /api/device/test/back`, `POST /api/device/test/keep`: test versions.
- `GET /api/push/key`, `POST /api/push/subscriptions`, `DELETE /api/push/subscriptions`, `POST /api/push/test`: notifications for this device.
- `GET /api/storage`: the disks Homelab knows.

**Edge cases and rules**

- When the push service says a subscription is gone (`core.ErrGone`), Homelab deletes that subscription (`notify` in `alerts.go`). Other push errors are logged and the push is skipped.
- `checkAlerts` does nothing when there is no notifier (for example when the push keys are missing).
- `checkDisk` looks at the disks at most once every 5 minutes (`diskEvery`). It sets the time before it checks, so one slow check does not run twice.
- A disk alert closes only below 80%, so a disk hovering around 85% does not send a new alert each time (`diskAlert`).
- The Resources screen only shows what the server returns. A part that failed is shown with its `problems` text.
- `sysres` reads only Homelab's own containers, never other containers on the same Docker (`sysres.go`). If Docker is down, the app part is left out, with the reason in `problems`.
- Push works only over https, and iPhone needs the app added to the Home Screen first (`pushUnsupported` in `push.ts`).

**To change it**

- To add an alert, write a function in `internal/hub/alerts.go` that uses `h.notify` with the right audience. Use `OpenAlert`, `MarkAlertSent` and `CloseAlert` so it is sent once. Call it from `checkAlerts`. Add a test in `internal/hub/alerts_test.go`.
- To change the disk threshold, change `diskFullAt` and the slack in `diskAlert` (`internal/hub/alerts.go`). To change how long prod must be down, change `downAfter`.
- To change what the Resources screen shows, change `internal/adapters/sysres/sysres.go` (the reading), then `Hub.Resources` in `alerts.go`, then `web/src/routes/resources/+page.svelte`.
- To change the device details, change `internal/adapters/sysinfo/sysinfo.go`. For anything that needs root, add it to `BuildReport` (the root report) too.
- To change the text a person sees, change the `Title` and `Body` strings where `h.notify` is called.

**Test it**

- `go test ./internal/hub/... ./internal/adapters/sysres/... ./internal/adapters/sysinfo/...`: the alerts, the resources and the device details (from the [Makefile](../../Makefile) and [how it works](../how-it-works.md)).
- `go test ./internal/api/...`: the device and push routes.
- `cd web && npm test`: the companion's tests. `make check-web` runs the companion checks.
- `make test` runs everything that CI runs for Go, Rust, the companion and handover notes.
- Push itself is not tested end to end here. Check it on a device with the test button on the "Me" screen.
