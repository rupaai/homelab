# Network: which ports are open

Settings → Network lists every port Homelab and its apps open on the device, and who can reach each one. Back to [the map](../map.md).

## In short

- It only shows. No button changes anything. To close a port, remove it from `ports:` in the app's `homelab.yaml`.
- It lists each port's app, environment, service, port, `tcp` or `udp`, and who can reach it.
- **Home network** means any device at home can reach it. **This device only** means only this device can (`127.0.0.1`).
- Only admins see it (`GET /api/network`). The reference is in [network.openapi.yaml](../network.openapi.yaml).

## What is listed

- **The app.** Its own port is on `127.0.0.1` only. Homelab's address for the app is the way in.
- **Extra ports** from `ports:` in `homelab.yaml`, for the app and for each service in `services:`. They are prod only. With `addresses: ip` they are open to the home network. With `addresses: local` they are this device only.
- **The database.** Postgres is always this device only.
- **The web address** Homelab serves for each environment (port 7100 and up). With `addresses: ip` it is open to the home network, and anyone who reaches it can use the app without signing in. The row says so.
- **Homelab itself:** the companion (7080), the app door (7085) and the services program (7086). The companion is open to the home network only with `addresses: ip`. The other two are this device only.
- **Host network.** An app with `network: host` (Home Assistant) gets one row. It uses the device's network, so every port it listens on is open to the home network. Homelab cannot list those ports.
- A dev environment opens no extra ports, so it shows only its own port, its database and its address.
- A turned-off environment, and one with nothing deployed, opens nothing. It is not listed.

Homelab reads this from the manifests it stores, not from Docker. So it never names a container that Homelab did not make.

## Hints

A row gets a short hint when a port looks unneeded. This is the case when an extra service's port is open to the home network, and one of the app's settings already points at that service inside the app's own network, such as `PROWLARR_URL: http://prowlarr:9696`. The hint reads "The app reaches prowlarr inside its own network. This port may not need to be open."

A hint is only a hint. Homelab never changes a port by itself. It only looks at the app's settings in `homelab.yaml`, not at secrets, and never at port ranges.

## How it works

1. `PortRows` in `internal/core/network.go` turns one environment's deploy spec into rows, and adds the hints. The compose file is made from the same spec, and `internal/adapters/docker/network_test.go` checks that both agree.
2. `Network` in `internal/hub/network.go` builds a spec for every deployed, turned-on environment from the stored manifest (it needs no secrets), adds the address rows, and adds Homelab's own ports from `ownPorts` in `cmd/homelabd/config.go`.
3. `GET /api/network` in `internal/api/device.go` returns `{"ports": [...]}`. Like any route without its own rule, it is for admins only.
4. The screen is `web/src/routes/settings/network/+page.svelte`, with `web/src/lib/network.ts` for the wording and grouping.

Test with `go test ./internal/core/... ./internal/hub/... ./internal/api/... ./internal/adapters/docker/... ./cmd/...` and `make check-web`.
