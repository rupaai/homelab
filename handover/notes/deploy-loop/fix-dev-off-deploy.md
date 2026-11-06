---
feature: deploy-loop
status: ready
---
## Line
- **Deploy loop** (milestone 1): add an app by `owner/name`, builds from GitHub Actions, dev
  and prod, promote, rollback, health checks, logs, and a Restart button on each environment
  (admins). Admins can turn dev off per app to save memory: it stops, keeps its data, and new
  builds wait until it is turned on. For manual apps, "Get latest build" on the dev card saves
  a build for dev while it is off, so Promote still works. Each round asks Docker once per app,
  not once per app per app. Done.

## What changed
While dev is off, the dev card shows "Get latest build". It saves the newest passing build for
dev and starts nothing. Promote then sends it to prod.
