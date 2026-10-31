# Self-update

How Homelab on the device installs a newer version of itself, tries test versions, and goes back when a new version does not stay up. Back to [the map](../map.md).

## In short

- On the device, Homelab can update itself from its GitHub releases. The update is in the companion: Resources, then "Update to ...".
- Each open pull request also has a test version. You can try one on the device, then keep it or go back to the old version.
- A new version has two minutes to prove it is running. If it does not stay up after three starts, Homelab goes back to the version before it, on its own.
- `homelab-services` is installed next to `homelabd` at the same version, and the two are updated together.
- Homelab checks GitHub for a newer version when the Resources screen asks, at most once an hour. Refresh asks at once.

## How it works

1. **Install.** `homelabd setup` (run as root) copies the program into `<home>/bin` as `homelabd-<version>`, makes `<home>/bin/homelabd` a symlink to it, and writes the systemd unit `homelab.service` ([setup.go](../../cmd/homelabd/setup.go)). The unit runs `update-guard` before each start and then `bin/homelabd run`. Homelab's home is `/var/lib/homelab` on the device.
2. **Ask for a version.** The Resources screen calls `GET /api/device`. `Status` in [selfupdate.go](../../internal/adapters/selfupdate/selfupdate.go) asks GitHub for the latest release, unless it already has an answer from the last hour. `?check=1` (Refresh) asks now. The answer says whether a newer version exists and what changed (`ParseNotes` in [notes.go](../../internal/adapters/selfupdate/notes.go)).
3. **Update.** `POST /api/device/update` runs `UpdateHomelab` ([device.go](../../internal/hub/device.go)). It refuses while any app action is running, or while a test version is on.
4. **Download and check.** `install` downloads `homelabd-<os>-<arch>` and `homelab-services-<os>-<arch>` from the release, checks the SHA-256 against `checksums.txt`, and writes each to `<home>/bin/<name>-<version>`. Then `checkProgram` runs `version` on the new program and checks that the version matches.
5. **Switch.** `Switch` ([layout.go](../../internal/adapters/selfupdate/layout.go)) points `previous` at the running version, writes `update-pending.json`, and points `homelabd` at the new version.
6. **Restart.** One second later, Homelab restarts (systemd starts the new program). Shutdown waits for running actions first (`WaitIdle` in [run.go](../../cmd/homelabd/run.go)).
7. **Guard.** Before each start, `update-guard` (`Guard`, [layout.go](../../internal/adapters/selfupdate/layout.go)) counts the tries of a pending version. Up to 3 tries are allowed. After that, it points `homelabd` back at the previous version and records the result as failed.
8. **Confirm.** When the new version has run for `ProveAfter` (2 minutes), `confirmUpdate` in [run.go](../../cmd/homelabd/run.go) calls `Confirm`. That writes `update-result.json` as OK, removes the pending marker, and logs "Homelab is now on ...". Older installs are pruned to the last 3 versions (`keepVersions`).
9. **Test versions.** `Try` (`POST /api/device/test`) checks the version is a test version on GitHub, saves Homelab's own databases (`homelab.db` and `secrets/secrets.db`) into `<home>/bin/test-saved`, installs the test build and restarts on it ([selfupdate.go](../../internal/adapters/selfupdate/selfupdate.go), [test.go](../../internal/adapters/selfupdate/test.go)). The test is recorded in `test.json`.
10. **Keep or go back.** `POST /api/device/test/keep` removes the saved copy, so the test data stays. `POST /api/device/test/back` points `homelabd` at the version from before the test. The old data comes back when that version starts (`FinishTest`, called from `run.go` at start).
11. **Apps are not touched.** Apps and Pocket ID keep their own data. A test does not copy or change it (`dataFiles` in `test.go`).

## Deep dive

**Files**

| File | What it does |
|---|---|
| [internal/adapters/selfupdate/selfupdate.go](../../internal/adapters/selfupdate/selfupdate.go) | The `Updater`: `Status`, `check`, `Apply`, `Tests`, `Try`, `GoBack`, `Keep`, `install`, `FetchServices`, `download`, `restart` |
| [internal/adapters/selfupdate/selfupdate_test.go](../../internal/adapters/selfupdate/selfupdate_test.go) | Tests for the updater, the switch and the guard |
| [internal/adapters/selfupdate/layout.go](../../internal/adapters/selfupdate/layout.go) | The `bin` folder: `Install`, `InstallServices`, `Switch`, `Guard`, `Confirm`, `Pending`, `LastResult`, `Activate`, `prune`, `Managed` |
| [internal/adapters/selfupdate/test.go](../../internal/adapters/selfupdate/test.go) | Test versions: `StartTest`, `GoBack`, `Keep`, `FinishTest`, `SaveData` |
| [internal/adapters/selfupdate/test_test.go](../../internal/adapters/selfupdate/test_test.go) | Tests for test versions |
| [internal/adapters/selfupdate/version.go](../../internal/adapters/selfupdate/version.go) | Reads `build-N` and `vX.Y.Z`, compares them, and reads test versions (`TestPR`) |
| [internal/adapters/selfupdate/version_test.go](../../internal/adapters/selfupdate/version_test.go) | Tests for version parsing |
| [internal/adapters/selfupdate/notes.go](../../internal/adapters/selfupdate/notes.go) | Reads a release's notes into new and fix counts |
| [internal/adapters/selfupdate/notes_test.go](../../internal/adapters/selfupdate/notes_test.go) | Tests for the notes reader |
| [cmd/homelabd/setup.go](../../cmd/homelabd/setup.go) | `homelabd setup`: installs the program and the systemd unit, `update-guard`, `system-report` |
| [cmd/homelabd/setup_test.go](../../cmd/homelabd/setup_test.go) | Tests for setup, including the guard |
| [cmd/homelabd/main.go](../../cmd/homelabd/main.go) | The commands: `run`, `setup`, `update-guard`, `system-report`, `recover`, `password`, `version` |
| [cmd/homelabd/run.go](../../cmd/homelabd/run.go) | Wiring: `FinishTest` at start, `confirmUpdate`, the restart function, the shutdown wait |
| [cmd/homelabd/homelabd_test.go](../../cmd/homelabd/homelabd_test.go) | Tests in the command package |
| [internal/adapters/services/services.go](../../internal/adapters/services/services.go) | `Program`: finds `homelab-services` next to `homelabd`, with the same version ending |
| [internal/hub/device.go](../../internal/hub/device.go) | `Device`, `UpdateHomelab`, `RecordUpdate`, `TestVersions`, `TryTestVersion`, `EndTest`, and the busy check |
| [internal/api/device.go](../../internal/api/device.go) | The device and update routes |
| [internal/core/contracts.go](../../internal/core/contracts.go) | The `Updater` interface that the hub uses |
| [scripts/build-release.sh](../../scripts/build-release.sh) | Builds the programs for the release: `homelabd` for linux/amd64, linux/arm64 and darwin/arm64; `homelab-services` for linux amd64 and arm64; `checksums.txt` |
| [.github/workflows/release.yml](../../.github/workflows/release.yml) | Makes a release for `main` and uploads the programs |
| [.github/workflows/test-build.yml](../../.github/workflows/test-build.yml) | Makes a test version for each open PR |
| [web/src/routes/resources/+page.svelte](../../web/src/routes/resources/+page.svelte) | The Resources screen: "Update to ...", Refresh, Try, "Keep this version" and "Go back to ..." |
| [web/src/lib/api.ts](../../web/src/lib/api.ts) | The update and test calls the screen makes |

**Routes**

- `GET /api/device`: the device view, with the update status. `?check=1` asks GitHub now.
- `POST /api/device/update`: update to the newest release.
- `GET /api/device/tests`: the test versions that have a program for this computer.
- `POST /api/device/test`: try a test version, body `{"version": "..."}`.
- `POST /api/device/test/keep` and `POST /api/device/test/back`: end the test.

**Versions and files on the device**

- Version names: `build-<n>` (the old numbering) and `v<major>.<minor>.<patch>`. Every `v` version is newer than every build (`version.go`).
- Test version names: `pr-<number>-<commit>` (older) and `vX.Y.Z-pr.<number>.<commit>` (`TestPR` in `version.go`).
- `<home>/bin/homelabd`, `<home>/bin/previous`: symlinks to the running and the earlier version.
- `<home>/bin/update-pending.json`: the version being proved, and its tries.
- `<home>/bin/update-result.json`: how the last update went. Shown on the Resources screen.
- `<home>/bin/test.json`, `<home>/bin/test-saved/`: the running test and the saved data.
- `/usr/local/lib/homelab/homelabd`: a copy of the program that the guard and the report use (`helperDir` in `setup.go`).
- A Mac is not managed: `Managed` is true only on Linux with the symlink in place. On a Mac, update with `git pull && make build`.

**States of an update**

| State | Meaning | Where it shows |
|---|---|---|
| Pending | A new version has started, and has not run for 2 minutes yet | `update-pending.json`, and the note on the Resources screen |
| Good | The new version ran for 2 minutes | `update-result.json` with `ok: true`, and the activity log |
| Rolled back | Three starts failed, so the previous version is back | `update-result.json` with `ok: false` |
| Testing | A test version runs, with the saved data kept aside | `test.json`, and the "Testing" line on the Resources screen |

**Edge cases and rules**

- **No downgrade by setup.** Running `setup` with an older program is refused unless `--force` is given (`checkNotOlder`, `setup.go`).
- **Test blocks update.** `Apply` refuses while a test version runs, so the saved data is not lost (`Apply`, `selfupdate.go`).
- **Busy blocks update.** `UpdateHomelab` refuses while an app action runs (`refuseIfBusy`, `device.go`).
- **Atomic writes.** A program is written to a `.tmp` file and renamed into place (`install`, `layout.go`). A half-written update never shows up as a version.
- **Setup starts Homelab.** `setup` enables and restarts the `homelab` unit (`setup.go`).
- **Checksum.** A download that does not match `checksums.txt` is refused (`download`, `checksumFor`).
- **Wrong version.** A program that does not print its own version is refused (`checkProgram`).
- **Tries.** Three starts of an unproven version, then back to the previous one (`maxTries`, `Guard`). The guard never runs on a version the operator switched by hand. It drops the marker instead.
- **Failed update result.** Written to `update-result.json` with OK false, and shown on the Resources screen.
- **Test data.** Only `homelab.db` and `secrets/secrets.db` are saved (`dataFiles`). Apps' data is not in a test.
- **Shutdown.** A restart waits for running actions up to `actionWait` (`run.go`).
- **Private repos.** The GitHub token (`GITHUB_TOKEN`, a platform setting) is used for the download, if the repo is private (`Token` in `run.go`).
- **Not on a Mac or without systemd.** `Managed` is false, and the Resources screen says so.

**To change it**

- To change how long a new version must run, edit `ProveAfter` in [layout.go](../../internal/adapters/selfupdate/layout.go). Then run the selfupdate tests.
- To change the tries before going back, edit `maxTries` in [layout.go](../../internal/adapters/selfupdate/layout.go).
- To change how often GitHub is asked, edit `checkEvery` in [selfupdate.go](../../internal/adapters/selfupdate/selfupdate.go).
- To change which data a test saves, edit `dataFiles` in [test.go](../../internal/adapters/selfupdate/test.go). Check the list still covers Homelab's own databases.
- To change the unit file, edit `homelabUnit` in [setup.go](../../cmd/homelabd/setup.go). Then run the setup tests.
- To change the release files, edit [build-release.sh](../../scripts/build-release.sh). Check with a PR's test build.

**Test it**

```sh
make check                                # Go: gofmt, go vet, go test ./...
go test ./internal/adapters/selfupdate/... ./cmd/...
make check-services                       # Rust side, which ships with the update
```

On a real device, check the update on the Resources screen after a PR's test build. Do not run anything on the device without asking Labiba first (see [AGENTS.md](../../AGENTS.md), Mars and production).

Status of this work: [HANDOVER.md](../../HANDOVER.md), section 3 (On the device).
