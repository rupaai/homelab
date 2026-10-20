# Milestone 5: know what's happening

Milestone 5 adds push notifications from the companion, alert rules, a resources screen,
and activity for builders. Health checks, logs, the activity log and auto-rollback came in
earlier milestones and are unchanged.

## New packages and contracts

| Package | Job |
|---|---|
| `internal/adapters/webpush` | `core.Notifier` with web push (VAPID). Pushes to a device the push service no longer knows return `core.ErrGone`, and Homelab forgets that device |
| `internal/adapters/sysres` | `core.Resources`: device CPU, memory and disk (gopsutil), and each app environment's CPU and memory (`docker stats`, matched by the `homelab.app` and `homelab.env` labels) |
| `internal/hub/alerts.go` | Alert rules, the push audience, subscriptions and the resources view |
| `web/src/service-worker.ts` | Shows notifications and opens the screen they link to. Caches nothing |

Homelab makes its VAPID key pair on first start and keeps it in the encrypted secrets store
(`HOMELAB_PUSH_PRIVATE_KEY`, `HOMELAB_PUSH_PUBLIC_KEY`, never listed in Settings).

## Alerts

| Alert | When | Who |
|---|---|---|
| *App* is down | prod down or degraded for 2 minutes in a row | builders of the app, admins |
| *App* is back | the same prod is healthy again, after a down alert went out | the same people |
| *App* was rolled back | an automatic rollback after a promote | builders of the app, admins |
| The disk is almost full | the disk holding `~/.homelab` is over 85% (checked every 5 minutes; clears below 80%) | admins |

Each incident is stored in the `alerts` table, so it's sent once, even across restarts.
Only push services' https addresses (Google, Apple, Mozilla, Microsoft) are accepted as
subscription endpoints, so Homelab can't be made to post to its own network.

## Notifications on a phone

Web push needs a secure origin. On the Mac, `http://localhost:7080` counts. On a phone, the
companion needs an https address. With `addresses: ip` it is plain http, so notifications
don't work there. On
iPhone, first add it to the Home Screen (Share, then Add to Home Screen) and open it from
there. Then go to **Me → Notifications → Turn on for this device**, then **Send a test**.

## State

Migration 6 adds `push_subscriptions` (endpoint, person, keys, device label; deleted with the
person) and `alerts` (open incident key, start, when it was sent).

## API additions

| Method and path | Body | Returns | Who |
|---|---|---|---|
| `GET /api/push/key` | | `{publicKey}` | anyone signed in |
| `POST /api/push/subscriptions` | `{endpoint, p256dh, auth, device}` | 204 | anyone signed in, for themselves |
| `DELETE /api/push/subscriptions` | `{endpoint}` | 204 | anyone signed in, their own devices |
| `POST /api/push/test` | | `{sent}` | anyone signed in |
| `GET /api/resources` | | `{device, apps[], problems[]}` | admins |
| `GET /api/activity?app=<id>` | | events | now also builders of that app |

## Moved to milestone 6

Self-update with automatic switch-back (O7) and automatic OS security updates (O9). Both need
Homelab running as a service from release builds, which starts on the mini device. On the Mac,
update with `git pull && make build`.
