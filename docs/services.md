# Apps with more than one container

An app can run more containers next to it, such as a media server. Each environment (dev
and prod) gets its own copy. The app reaches them by name, e.g. `http://jellyfin:8096`.

```yaml
# homelab.yaml
folders:                      # shared folders from the device (see below)
  - {name: media, path: /media}
ports: ["6881-6891:6881-6891", "6881-6891:6881-6891/udp"]   # opened on the device, prod only

services:
  - name: jellyfin
    image: jellyfin/jellyfin:10.11.11       # needs a fixed tag, not latest
    data: [{name: jellyfin-config, path: /config}]
    folders: [{name: media, path: /media}]
    ports: ["8096:8096"]
    gpu: true                               # /dev/dri, for video transcoding
    memory: 4g
  - name: prowlarr
    image: lscr.io/linuxserver/prowlarr:version-2.6.5.5623
    env: {PUID: "1000", PGID: "1000"}
    data: [{name: prowlarr-config, path: /config}]
    ports: ["9696:9696"]
```

- `data` works like the app's: a folder per environment under
  `apps/<app>/<env>/data/<name>`, copied before every promote. A service may use the same
  name as the app to share a folder.
- `folders` are folders on the device that several apps or containers share. The device
  owner names them in `config.yaml`, so an app can't reach anything else:

  ```yaml
  folders:
    media: /mnt/storage
  ```

  Homelab reads `config.yaml` when it starts, so restart it after adding one.
- `ports` are opened only for prod, because a port number can be used once per device.
  With `addresses: ip` they are open to the network; otherwise on 127.0.0.1 only. Host
  ports 7000-7999 are kept for Homelab.
- Service images come from public registries and are downloaded once per tag.
- Only the app has a health check, a web address and a version; services follow the app's
  deploys. If a service stops or never starts, Homelab starts the environment again and
  shows the service next to the app's health. The app is not marked down because of it.
