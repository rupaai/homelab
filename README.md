# Homehub

A self-hosted home platform: push code, see it running in dev, promote the exact same
build to prod, and roll back with a tap. One program, `homehubd`, keeps the desired state
in SQLite and a reconciler makes Docker, GitHub and Tailscale match it.

This is milestone 1: the deploy loop, on a Mac. See [docs/milestone1.md](docs/milestone1.md)
for how the pieces fit.

## Build

Needs Go 1.27+, Node 22+ and Docker (OrbStack on the Mac).

```sh
make build          # bin/homehubd, with the companion web app inside
make check          # gofmt, go vet, go test
make test-docker    # end-to-end test of the Docker adapter (needs Docker running)
```

## Run

```sh
bin/homehubd init                 # creates ~/.homehub with config.yaml and secrets/
bin/homehubd run                  # companion at http://127.0.0.1:7080
bin/homehubd service install      # optional: start at login (prints one launchctl command)
```

Secrets live in `~/.homehub/secrets/` as `KEY=VALUE` files that only you can read
(`chmod 600`). Platform credentials go in `platform.env` (`GITHUB_TOKEN`, `TS_AUTHKEY`);
an app's secrets go in `<app>.<env>.env`, for example `movies.prod.env`. Restart
`homehubd` after changing `platform.env`; app secrets are read at each deploy.

## Adding an app

An app is a repo with a Dockerfile, a CI workflow that pushes an image tagged with the
commit, and a `homehub.yaml` (see
[smart-recommender-player's](https://github.com/rupaai/smart-recommender-player/blob/main/homehub.yaml)).
Add it in the companion with its `owner/name`.
