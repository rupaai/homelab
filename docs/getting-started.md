# Getting started

This page runs Homelab on your Mac so you can click around. It is the development setup. The real Homelab runs on a Linux box called mars.

First do the setup in [CONTRIBUTING.md](../CONTRIBUTING.md): git, gh, Go, Node, Rust and OrbStack (Docker).

## 1. Build it

From the `homelab` folder:

```sh
make build
```

This builds the companion web app and the Rust service `homelab-services`. Then it builds `bin/homelabd`. The first build is slow.

## 2. Make a home folder

Homelab keeps everything in one folder, `~/.homelab`. An empty config file is fine. It means `addresses: local`, so everything stays on your Mac.

```sh
mkdir -p ~/.homelab && touch ~/.homelab/config.yaml
```

## 3. Run it

Make sure OrbStack (Docker) is running. Then:

```sh
bin/homelabd run
```

Open http://localhost:7080. This is the companion, the web app you use to manage Homelab.

## 4. First sign-in

On first run the companion asks you to create the owner and register a passkey (a sign-in that uses your Mac's fingerprint or password). It shows a recovery code once. Keep it. If you lose access, run:

```sh
bin/homelabd recover
```

It prints a one-time sign-in link. The link expires after 15 minutes.

Note: the owner of this local Homelab is you. It is not the real Homelab on mars. Secrets you enter here stay on your Mac.

## 5. Look around

In the companion you can:

- See apps, and add one by its GitHub `owner/name`. An app is a repo with a Dockerfile and a `homelab.yaml` file.
- Open **Settings** for platform credentials and API keys.
- Open **Files** and **Resources** to see storage and the device.
- Open **People** to see who can sign in.

To add an app you need a GitHub token in **Settings**. Only Labiba should enter real secrets. Ask Labiba before you add anything real. For your own tests, use a throwaway app and a throwaway token with no secrets in it.

## 6. Stop it

Press Control+C in the terminal where `bin/homelabd run` is running.

## Work on the web app with live reload

The companion source is in `web/`. To see your changes without a full rebuild, keep `bin/homelabd run` going and start the dev server in a second terminal. It passes `/api` calls to port 7080:

```sh
cd web
npm ci
npm run dev
```

## Where to go next

- [docs/how-it-works.md](how-it-works.md): what each part does and where its code is.
- [HANDOVER.md](../HANDOVER.md): current state and open work.
- [CONTRIBUTING.md](../CONTRIBUTING.md): make your first PR.
- [docs/platform-services.md](platform-services.md): API keys, files, secrets and databases for apps.
- [plugins/README.md](../plugins/README.md): small libraries so apps can use Homelab.
