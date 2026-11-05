---
feature: deploy-loop
status: ready
---
## Line
- **Deploy loop** (milestone 1): add an app by `owner/name`, builds from GitHub Actions, dev
  and prod, promote, rollback, health checks, logs, and a Restart button on each environment
  (admins). Admins can turn dev off per app to save memory: it stops, keeps its data, and new
  builds wait until it is turned on. Each round asks Docker once per app, not once per app per
  app. Done.

## What changed
Adds a Turn off / Turn on switch for an app's dev (admins). The flag is `envs.off` in the state
file. Off stops dev's containers and keeps everything else, new builds are saved for dev but do
not start, and promote shows a warning instead of a block. Prod is never affected.
