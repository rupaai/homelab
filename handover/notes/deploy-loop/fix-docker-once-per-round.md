---
feature: deploy-loop
status: ready
---
## Line
- **Deploy loop** (milestone 1): add an app by `owner/name`, builds from GitHub Actions, dev
  and prod, promote, rollback, health checks, logs. Each round asks Docker once per app, not once per app per app. Done.

## What changed
Added `ObserveApp` to the Deployer contract. The Docker adapter filters the container list by the `homelab.app` label, so a round no longer lists and inspects every container for each app.
