# Milestone 6: move to the mini device

> History. The move from the Mac is done, so `homelabd export`, `setup --import`, `init` and
> `service install` are gone. Tailscale is gone too: Homelab uses `addresses: ip` or `local`.

Milestone 6 moves Homelab from the Mac to a Linux device at home, where it runs all the time
as a system service. Everything from milestones 1 to 5 works the same way there.

What it adds:

- **`homelabd setup`**: one command, run with sudo on the device, that installs Homelab.
- **Releases**: every merge to `main` becomes a GitHub release (`build-<n>` at the time; now `vX.Y.Z`, see HANDOVER.md) with the
  program for Linux and the Mac (the Mac build is for developing Homelab).
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

`sudo ./homelabd-linux-amd64 setup [--force]`:

1. Checks Docker runs and installs its compose plugin if missing; installs `git`,
   `smartmontools` and `unattended-upgrades` if missing.
2. Makes the `homelab` system account (no login, no home in `/home`; it joins an existing
   `homelab` group if there is one) and adds it to the `docker` group. People's own accounts don't need Docker access for Homelab.
3. Makes `/var/lib/homelab` (mode 0700, owned by `homelab`). On a first run it writes
   `config.yaml` with `addresses: ip` (see below). An existing config is never changed.
4. Installs the program into `/var/lib/homelab/bin` (see below) and a copy at
   `/usr/local/lib/homelab/homelabd` for the update guard and the device report.
5. Writes `homelab.service`, `homelab-report.service` + `.timer`, and
   `/etc/apt/apt.conf.d/52homelab-unattended-upgrades`; enables and starts them.

Running setup again with a newer program updates it in place and keeps all data. It
refuses to put an older program over a newer one unless you add `--force`. It also ends a
running test version (see below), keeping the data as it is now, so the next start doesn't
put back older data.

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

App data folders on Linux: an app's container usually runs as its own user (the movie app
runs as uid 1000), not as `homelab`. Homelab can't change file owners, so before each deploy
it opens the files it owns in an app's data folder to every user (`internal/sharedir`). The
Homelab folder itself is 0700, so nobody else on the device can reach them.

## Updates

`.github/workflows/release.yml` publishes a release (`build-<n>` at the time) on every push to
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
forks) as a pre-release named `pr-<number>-<commit>` (now `vX.Y.Z-pr.<number>.<commit>`), titled with the pull request's title.
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
deletes the saved copy. A normal update is refused during a test: go back or keep the test version first.

Apps keep running through all of this, and their data and Pocket ID's data
are not copied: only Homelab's own databases go back.

## Plain IP addresses (`addresses: ip`)

`homelabd setup` writes `addresses: ip` in `config.yaml`:

- The companion listens on `0.0.0.0:7080` (unless `listen` says otherwise), so any device
  that reaches the computer opens `http://<its IP>:7080`.
- Passkeys can't work on a bare IP, so other devices sign in with a password. Set it on the
  device: `sudo -u homelab /var/lib/homelab/bin/homelabd password --home /var/lib/homelab`.
  Only a bcrypt hash is kept, in `secrets/companion-password`. It signs in as the owner.
  Tries are limited to 5 per device and 20 in all every 5 minutes. On the computer itself
  (`localhost`) passkeys and the recovery code work as before.
- Invited people get their own password. Invite links start with the address the owner used
  (`http://<IP>:7080/invite/...`). Opened on another device, the invite asks the person to choose
  a password (at least 10 characters) instead of making a passkey, and signs them in.
  `POST /api/invites/{token}/password` takes `{password}`, uses the invite once and answers
  `{token}`, with the same per-device limit as password sign-in. Only a bcrypt hash is kept
  (table `person_passwords`). Removing the person, or resending their invite, deletes it. Opened
  on the computer (`localhost`), the invite still makes a passkey.
- Sign-in on another device takes an email and password. `POST /api/password-sign-in` with
  `{email, password}` checks that person's own password. Without `email` it checks the owner's
  password from `homelabd password`, as before. The owner can also add their email to use that
  password. An unknown email and a wrong password give the same answer.
- Homelab can email the invite. In Settings > Email add a Gmail address and an app password:
  turn on 2-step verification on the Google account, make an app password at
  https://myaccount.google.com/apppasswords, and paste it in. They are stored as the encrypted
  platform secrets `EMAIL_ADDRESS` and `EMAIL_APP_PASSWORD` and read for each message, so no
  restart is needed. Mail goes through `smtp.gmail.com:587` with STARTTLS, as plain text.
  Creating or resending an invite then emails the link and answers
  `{url, emailed, emailError?}`. If the email fails the invite still works and the link is shown.
  "Send test email" (`POST /api/email/test`) writes to the signed-in person's own address.
- Each app environment gets a port from 7100 up on `0.0.0.0`, open to anyone who reaches it,
  without sign-in. Apps get identity headers that name nobody, and Homelab's own cookies are
  dropped before they reach the app. The companion's sign-in is not a cookie, so other ports never receive it. The companion shows app links with the IP it was opened on.
- Plain http isn't encrypted on the home network, so use it only on a network you trust.
  Push notifications need https, so they don't work this way.

## The device card

`GET /api/device` (admins) returns `{system, update, problems}`. `POST /api/device/update`
(admins) starts an update and answers 202 with the version.

Homelab runs without root, so disk health (`smartctl -H`) and the pending-update count
(`/usr/lib/update-notifier/apt-check`) come from `homelabd system-report`, which a root
systemd timer runs every 15 minutes and writes to `/run/homelab/system.json`. A missing or
hour-old report just shows those parts as unknown. The rest (OS, uptime, temperature, disk
use) is read directly.

## Signing in on the device

People open `http://<the device's IP>:7080` and sign in with their email and password (the owner can leave the email empty). The passkey sign-in and `homelabd recover` still work on
`localhost`, which on the device is reached with an SSH tunnel from another computer:

```sh
ssh -L 7080:localhost:7080 -L 7081:localhost:7081 <you>@<device>
sudo -u homelab /var/lib/homelab/bin/homelabd recover --home /var/lib/homelab
# then open the printed http://localhost:7080/recover/... link on that computer
```

## Not in this milestone

- Wi-Fi and network setup, factory reset and first-boot setup: appliance stage (milestone 11).
- A privileged `homelab-system` service with a socket (OS4). The root report covers what the
  device card needs; restarts stay a `sudo reboot`.
- Automatic Homelab updates without a tap (the button is there; a setting can come later).
- Alerts for a failing disk or a needed restart: they show on the card for now.
