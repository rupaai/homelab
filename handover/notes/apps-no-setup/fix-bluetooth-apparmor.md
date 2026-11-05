---
feature: apps-no-setup
status: ready
---
## Line
- **Apps with no setup** (milestone 2): promotion modes `manual`, `auto-dev`, `auto` with
  automatic rollback, a mirror of each app's code, signed identity headers, extra containers
  (`services:` in `homelab.yaml`, see [docs/services.md](docs/services.md)). One trusted app
  can use real hardware in prod: `network: host`, `devices` and `bluetooth` in `homelab.yaml`,
  allowed by `devices` and `hardware_apps` in `config.yaml` (dev ignores them, see
  [docs/topics/hardware.md](docs/topics/hardware.md)). `bluetooth` also turns AppArmor off for
  that app so D-Bus works on Ubuntu. Done.

## What changed
Bluetooth apps now get `security_opt: ["apparmor:unconfined"]`. Before, Docker's AppArmor profile blocked D-Bus on mars and Home Assistant Bluetooth failed.
