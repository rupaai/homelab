# Hardware for one trusted app

How one app can use real hardware (a USB stick, Bluetooth, the device's own network), such as Home Assistant. Back to [the map](../map.md).

## In short

- The device owner decides. Two settings in `config.yaml` name the hardware and the apps that may use it. The app only asks.
- It works in **prod only**. Dev ignores `network`, `devices` and `bluetooth` and runs as a normal sandbox: its own network, no devices, no Bluetooth. So dev can never touch real hardware.
- No `privileged` and no full `/dev`. The app gets only the devices named, at the same path inside the container.

## Set it up

In `config.yaml` (restart Homelab after a change):

```yaml
devices:
  zigbee: /dev/serial/by-id/usb-SONOFF_SONOFF_Dongle_Max_MG24_xxxx-if00-port0
hardware_apps: [home-assistant]   # app ids allowed to use network: host, devices, bluetooth
```

In the app's `homelab.yaml`:

```yaml
port: 8123        # the port the app listens on; with network: host it is the device's port
network: host     # only "host" or empty
devices: [zigbee] # names from config.yaml
bluetooth: true   # mounts /run/dbus:/run/dbus:ro and adds NET_ADMIN and NET_RAW
```

## Rules

- `network: host` gives the app the device's own network, which discovery (mDNS, HomeKit, cast) needs. The app is then not published on `127.0.0.1:<host port>`. Homelab reaches it at `127.0.0.1:<port>` from `homelab.yaml`.
- `network: host` can't be used with `services:`, `database: postgres` or `ports:`. Those need Homelab's own network. `homelab.yaml` is refused with a message.
- With `network: host` the port must not be in 7000-7999 (kept for Homelab), and two apps on the host network can't use the same port.
- If the app is not in `hardware_apps`, or names a device that is not in `config.yaml`, the prod deploy stops with a plain message in the activity log. The same happens when the device is not on the host, for example not plugged in.
- Data copies, promote and rollback work as for any app.

## How it works

1. `manifest.go` reads and checks `network`, `devices` and `bluetooth`.
2. `spec` in `internal/hub/hub.go` calls `hardware` for prod only. It checks `hardware_apps`, the device names and paths, and the port. It sets `HostNetwork`, `Devices` and `Bluetooth` in the `EnvSpec`.
3. `composeYAML` in `internal/adapters/docker/compose.go` writes `network_mode: host`, the `devices` list, the dbus mount and `cap_add`. There are no published ports.
4. The health check, the address proxy and the other callers ask `reachPort` in `hub.go` for the port. It gives the app's own port for prod on the host network, and the published port in every other case.

Test with `go test ./internal/manifest/... ./internal/hub/... ./internal/adapters/docker/... ./cmd/...`.
