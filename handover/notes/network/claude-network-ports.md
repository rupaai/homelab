---
feature: network
status: ready
new: true
---
## Line
- **Network** (Settings → Network, admins): lists every port Homelab and its apps open on the device (app, env, service, port, tcp/udp) and who can reach it: the home network or this device only. Read only, from `GET /api/network`. A hint shows when an extra service's port is open but the app already reaches the service inside its own network. Done.

## Open work
none

## What changed
New screen and route that only read. The rows come from the stored manifests through `PortRows` (`internal/core/network.go`), which uses the same spec as the compose file. Turned-off dev and undeployed envs are left out. An app on the host network has one row.
