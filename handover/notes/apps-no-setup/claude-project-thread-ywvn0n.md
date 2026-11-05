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
  [docs/topics/hardware.md](docs/topics/hardware.md)). Done.

## What changed
Prod apps can use the host network, named USB devices and Bluetooth, only when the device owner allows the app. Also fixed the rule about which mars containers are whose.
