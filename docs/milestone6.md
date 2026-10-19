# Milestone 6: move to the mini device

Milestone 6 moves Homelab from the Mac to a Linux device at home, where it runs all the time
as a system service. Everything from milestones 1 to 5 works the same way there.

What it adds:

- **`homelabd setup`**: one command, run with sudo on the device, that installs Homelab.
- **`homelabd export`**: packs up the Mac's Homelab (people, apps, settings, secrets, app data)
  into one file that `setup --import` loads on the device.
- **Releases**: every merge to `main` becomes a numbered GitHub release (`build-<n>`) with the
  program for Linux and the Mac.
- **Self-update** from the companion, with an automatic return to the previous version if a
  new one doesn't stay up.
- **Automatic OS security updates** (Ubuntu's unattended-upgrades; it never reboots on its own).
- **A device card** on the Resources screen: the OS, uptime, temperature, every disk with its
  health, pending OS updates, whether a restart is needed, and Homelab's version.

It targets Ubuntu and Debian with systemd and Docker already installed. The device keeps its
existing disk layout: everything Homelab keeps is in one folder, `/var/lib/homelab`, so it can
move to its own disk later without other changes. (This replaces OS6's partition split,
which would have meant reinstalling a device that already runs other things.)

## What setup does

`sudo ./homelabd-linux-amd64 setup [--import FILE]`:

1. Checks Docker runs and installs its compose plugin if missing; installs `git`,
   `smartmontools` and `unattended-upgrades` if missing.
2. Makes the `homelab` system account (no login, no home in `/home`; it joins an existing
   `homelab` group if there is one) and adds it to the `docker` group. People's own accounts don't need Docker access for Homelab.
3. Makes `/var/lib/homelab` (mode 0700, owned by `homelab`). With `--import`, unpacks the
   move file there (refusing if a Homelab is already there) and switches it to Tailscale
   addresses.
4. Installs the program into `/var/lib/homelab/bin` (see below) and a copy at
   `/usr/local/lib/homelab/homelabd` for the update guard and the device report.
5. Writes `homelab.service`, `homelab-report.service` + `.timer`, and
   `/etc/apt/apt.conf.d/52homelab-unattended-upgrades`; enables and starts them.

Running setup again with a newer program updates it in place and keeps all data.

Other containers on the device (Home Assistant and friends) are left alone. Homelab only
touches containers with its own labels and the `homelab-pocketid` project.

### Files

| Path | What |
|---|---|
| `/var/lib/homelab/` | Homelab's folder (what `~/.homelab` is on the Mac) |
| `/var/lib/homelab/bin/homelabd` | symlink to the version that runs |
| `/var/lib/homelab/bin/homelabd-<version>` | installed versions (the last three are kept) |
| `/usr/local/lib/homelab/homelabd` | copy used by the update guard and the report (root-owned) |
| `/run/homelab/system.json` | the root report: disk health, pending updates, restart needed |
| `/etc/systemd/system/homelab*.{service,timer}` | the units |

### Logs and control

```sh
sudo journalctl -u homelab -f     # follow the log
sudo systemctl restart homelab    # restart
sudo systemctl stop homelab       # stop (it starts again at boot)
```

## Moving from the Mac

1. On the Mac, stop Homelab, then run `bin/homelabd export`. It writes
   `~/homelab-move-<date>.tar.gz` (mode 0600; it holds every secret and the master key), then:
   - removes the Mac's Homelab machines from the tailnet (with the OAuth client set; otherwise
     it says to delete them in the admin console), so the device can take the same names;
   - stops and removes Homelab's containers on the Mac (their data stays in `~/.homelab`);
   - writes `~/.homelab/MOVED`, after which `homelabd run` refuses to start there, so two
     copies never run at once. Delete that file to go back.
2. Copy the file and the Linux program to the device and run setup with `--import`.

The move leaves behind what the device makes again: Tailscale machine state, code mirrors,
logs and local addresses.

App data folders on Linux: an app's container usually runs as its own user (the movie app
runs as uid 1000), not as `homelab`. Homelab can't change file owners, so before each deploy
it opens the files it owns in an app's data folder to every user (`internal/sharedir`). The
Homelab folder itself is 0700, so nobody else on the device can reach them.

## Updates

`.github/workflows/release.yml` publishes `build-<n>` on every push to
`main` with `homelabd-linux-amd64`, `homelabd-linux-arm64`, `homelabd-darwin-arm64` and
`checksums.txt`. The repo is private, so Homelab reads releases with the `GITHUB_TOKEN` from
Settings (it needs the `repo` scope, which it already has for private app repos).

Resources shows the running and newest version (checked every 6 hours). **Update** (admins):

1. downloads the program for this device and checks its SHA-256 against `checksums.txt`;
2. runs its `version` command to make sure it runs here;
3. points `bin/homelabd` at it, remembers the old one as `bin/previous`, writes
   `bin/update-pending.json`, and stops; systemd starts the new version.

Before every start, systemd runs `update-guard`, which counts starts of an unproven version.
A version that keeps running for 2 minutes is confirmed (`bin/update-result.json`, and an
activity entry). If it fails to start 3 times, the guard points `bin/homelabd` back at the
previous version, and Resources says what happened.

On the Mac nothing changes: update with `git pull && make build`.

### Test versions

`.github/workflows/test-build.yml` builds every open pull request (from this repo, not
forks) as a pre-release named `pr-<number>-<commit>`, titled with the pull request's title.
A new push replaces it; closing the pull request deletes it. Pre-releases never count as the
newest version, so normal updates ignore them. Both workflows build with
`scripts/build-release.sh`.

Resources → Homelab → **Test versions** (admins) lists them. **Try**:

1. downloads and checks the program, as an update does;
2. copies Homelab's own data (`homelab.db` and `secrets/secrets.db`, with `VACUUM INTO`)
   into `bin/test-saved/` and writes `bin/test.json`;
3. switches to it like an update (so the update guard still goes back if it won't start).

While a test runs the card offers **Go back** and **Keep this version**. Go back points
`bin/homelabd` at the version from before and restarts. When that version starts, before it
opens any data, it puts the saved copy back (`selfupdate.FinishTest`). The same happens when
the update guard goes back on its own. Keep (once the test version has run 2 minutes)
deletes the saved copy. A normal update during a test also ends it, keeping the data.

Apps keep running through all of this, and their data, Pocket ID's data and Tailscale's
state are not copied: only Homelab's own databases go back.

## Plain IP addresses (`addresses: ip`)

For a home that doesn't want Tailscale in the way, `config.yaml` can say `addresses: ip`:

- The companion listens on `0.0.0.0:7080` (unless `listen` says otherwise), so any device
  that reaches the computer opens `http://<its IP>:7080`.
- Passkeys can't work on a bare IP, so other devices sign in with a password. Set it on the
  device: `sudo -u homelab /var/lib/homelab/bin/homelabd password --home /var/lib/homelab`.
  Only a bcrypt hash is kept, in `secrets/companion-password`. It signs in as the owner.
  Tries are limited to 5 per device and 20 in all every 5 minutes. On the computer itself
  (`localhost`) passkeys and the recovery code work as before.
- Each app environment gets a port from 7100 up on `0.0.0.0`, open to anyone who reaches it,
  without sign-in. Apps get identity headers that name nobody, and Homelab's own cookies are
  dropped before they reach the app. The companion shows app links with the IP it was opened on.
- Plain http isn't encrypted on the home network. Over Tailscale (for example the device
  shared into your tailnet) it is.

## The device card

`GET /api/device` (admins) returns `{system, update, problems}`. `POST /api/device/update`
(admins) starts an update and answers 202 with the version.

Homelab runs without root, so disk health (`smartctl -H`) and the pending-update count
(`/usr/lib/update-notifier/apt-check`) come from `homelabd system-report`, which a root
systemd timer runs every 15 minutes and writes to `/run/homelab/system.json`. A missing or
hour-old report just shows those parts as unknown. The rest (OS, uptime, temperature, disk
use) is read directly.

## Signing in on the device

People use the companion at `https://homelab.<tailnet>.ts.net`; their Tailscale login signs
them in (milestone 4). The passkey sign-in and `homelabd recover` still work on
`localhost`, which on the device is reached with an SSH tunnel from the Mac:

```sh
ssh -L 7080:localhost:7080 -L 7081:localhost:7081 <you>@<device>
sudo -u homelab /var/lib/homelab/bin/homelabd recover --home /var/lib/homelab
# then open the printed http://localhost:7080/recover/... link on the Mac
```

## Not in this milestone

- Wi-Fi and network setup, factory reset and first-boot setup: appliance stage (milestone 11).
- A privileged `homelab-system` service with a socket (OS4). The root report covers what the
  device card needs; restarts stay a `sudo reboot`.
- Automatic Homelab updates without a tap (the button is there; a setting can come later).
- Alerts for a failing disk or a needed restart: they show on the card for now.
