# Homelab

A self-hosted home platform: push code, see it running in dev, promote the exact same
build to prod, and roll back with a tap. One program, `homelabd`, keeps the desired state
in SQLite and a reconciler makes Docker, GitHub and Tailscale match it.

Milestones so far: 1, the deploy loop on a Mac ([docs/milestone1.md](docs/milestone1.md)), and
2, apps with no setup: encrypted secrets set in the companion, auto-promote with automatic
rollback, signed identity headers and a mirror of every app's code
([docs/milestone2.md](docs/milestone2.md)).

## Build

Needs Go 1.27+, Node 22+ and Docker (OrbStack on the Mac).

```sh
make build          # bin/homelabd, with the companion web app inside
make check          # gofmt, go vet, go test
make test-docker    # end-to-end test of the Docker adapter (needs Docker running)
```

## Run

```sh
bin/homelabd init                 # creates ~/.homelab with config.yaml and secrets/
bin/homelabd run                  # companion at http://127.0.0.1:7080
bin/homelabd service install      # optional: start at login (prints one launchctl command)
```

Secrets are set in the companion: platform credentials (the GitHub token) under
**Settings**, and an app's secrets on its page under **Secrets**. They're kept encrypted in
`~/.homelab/secrets/secrets.db` with the key in `~/.homelab/secrets/master.key`; keep a copy
of that key somewhere safe. Plain `KEY=VALUE` files from milestone 1 are imported once at
start and renamed to `*.imported`.

## Adding an app

An app is a repo with a Dockerfile, a CI workflow that pushes an image tagged with the
commit, and a `homelab.yaml` (its id, name and image default from the repo name; see
[smart-recommender-player's](https://github.com/rupaai/smart-recommender-player/blob/main/homelab.yaml)).
Add it in the companion with its `owner/name`.
