---
feature: deploy-loop
status: ready
---
## Line
- **Deploy loop** (milestone 1): add an app by `owner/name`, builds from GitHub Actions, dev
  and prod, promote, rollback, health checks, logs, and a Restart button on each environment
  (admins). Each round asks Docker once per app, not once per app per app. Done.

## What changed
Adds the Restart button on each app environment. It makes fresh containers from the build the
environment runs now. The wanted build does not change.
