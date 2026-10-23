# Homelab

A self-hosted home platform: push code, see it running in dev, promote the exact same
build to prod, and roll back with a tap. One program, `homelabd`, keeps the desired state
in SQLite and a reconciler makes Docker and GitHub match it.

**Start here:** [HANDOVER.md](HANDOVER.md) says what each part is, where it lives, its state and
what's open. Guides for app developers are in [docs/](docs/); how each milestone was built is
in [docs/history/](docs/history/).

## Build

Needs Go 1.27+, Node 22+ and Docker (OrbStack on a Mac, which is only for developing Homelab).

```sh
make build          # bin/homelabd, with the companion web app inside
make check          # gofmt, go vet, go test
make test-docker    # end-to-end test of the Docker adapter (needs Docker running)
```

## Run

```sh
mkdir -p ~/.homelab && touch ~/.homelab/config.yaml   # an empty config means addresses: local
bin/homelabd run                  # companion at http://localhost:7080
```

The companion starts at `http://localhost:7080`. On first run it asks you to create the
owner and register a passkey. Keep the recovery code it shows once. If you lose access,
`bin/homelabd recover` prints a one-time sign-in link that expires after 15 minutes.

Secrets are set in the companion: platform credentials (the GitHub token) under
**Settings**, and an app's secrets on its page under **Secrets**. They're kept encrypted by
homelab-services, with the key in `~/.homelab/secrets/master.key`; keep a copy
of that key somewhere safe. Secrets from an older Homelab are copied over once at start, and
the old `~/.homelab/secrets/secrets.db` is left where it is. Plain `KEY=VALUE` files from milestone 1 are imported once at
start and renamed to `*.imported`.

Only the owner, and people the owner allows ("Can see secrets" on the People screen), can
show or change a secret's value. Other admins see which secrets are set. API keys never can.
Apps get their secrets as files in `/run/secrets`, and as environment variables too unless
`secrets_as: files` is in `homelab.yaml`.

## On a Linux device

Every merge to `main` publishes a release with `homelabd-linux-amd64`. Copy it to the device
and run `sudo ./homelabd-linux-amd64 setup`. It writes `addresses: ip`, so other devices open
`http://<the device's IP>:7080` and sign in with the password you set with
`sudo -u homelab /var/lib/homelab/bin/homelabd password --home /var/lib/homelab`. After that it
updates itself from the companion's Resources screen. Setup refuses an older program unless you
add `--force`. See [docs/history/milestone6.md](docs/history/milestone6.md).

## Adding an app

An app is a repo with a Dockerfile, a CI workflow that pushes an image tagged with the
commit, and a `homelab.yaml` (its id, name and image default from the repo name; see
[smart-recommender-player's](https://github.com/rupaai/smart-recommender-player/blob/main/homelab.yaml)).
Add it in the companion with its `owner/name`.

## Contributing

New here? Start with [CONTRIBUTING.md](CONTRIBUTING.md). To try Homelab on a Mac, see
[docs/getting-started.md](docs/getting-started.md). To learn how the parts fit, see
[docs/how-it-works.md](docs/how-it-works.md).
