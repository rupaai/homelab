# Release versions

How versions are picked and made, for Homelab itself and for the apps it runs. Back to [the map](../map.md).

## In short

- **Homelab's own versions** (`vX.Y.Z`) are made by `.github/workflows/release.yml` after CI passes on `main`. Each open pull request also gets a test version.
- **App versions** are made by Homelab for each app, with no change in the app's repo. Dev builds get `vX.Y.Z-dev.N`, and a promote makes `vX.Y.Z` for prod.
- The number comes from the **PR titles**. `feat:` raises the middle number, `fix:` the last one, and `feat!:` or `BREAKING CHANGE` raises the first one.
- Apps read their own version from `HOMELAB_VERSION`, and can list their releases with the `releases:read` scope.
- A version can never block a deploy. If GitHub says no, the app is deployed without one.

## How it works

1. **Titles.** Every PR title must start with a type (`feat:`, `fix:`, `docs:`, `chore:`, `test:`, `ci:`, `style:`, `refactor:` or `build:`, with `!` for breaking). [pr-title.yml](../../.github/workflows/pr-title.yml) fails the rest.
2. **Pick the version.** `homelab-services release <prod|dev|test>` ([releases.rs](../../services/homelab-services/src/releases.rs), `plan`) finds the newest `vX.Y.Z` tag. It reads the commits since that tag, and works out the change: `classify` reads each title, and `biggest` takes the largest kind. `bump` gives the next number.
3. **Channel.** `prod` makes `vX.Y.Z`. `dev` makes `vX.Y.Z-dev.N`, with N one higher than the newest dev tag for the same base. `test` makes `vX.Y.Z-pr.N` for pull request N.
4. **Reuse and conflicts.** If the commit already has a version for that channel, the same one is returned. If the tag exists on another commit, the command stops with an error.
5. **Make the release.** It creates a GitHub release. Dev and test are pre-releases. Notes are written from the changes (`notes`, `summary`).
6. **Print.** The version goes to stdout, the notes to stderr. With `--dry-run` nothing is created.
7. **Homelab's own release.** `release.yml` runs the command with `--first v0.20.0` and `--dry-run`, builds the programs with `.github/actions/build-dist`, then runs `gh release create` with the files from `scripts/build-release.sh`.
8. **Test versions.** `test-build.yml` runs on each pull request. It makes `vX.Y.Z-pr.N`, adds the commit's first 7 characters (`vX.Y.Z-pr.N.<sha7>`), and builds the programs. A new push replaces it. Closing the PR deletes it.
9. **Apps.** Before a build goes on dev, Homelab asks `homelab-services` for a dev version through the internal route `/internal/releases/publish` (`Publish` in [services/versions.go](../../internal/adapters/services/versions.go)), using the app's repo and the GitHub token (`stampVersion` and `makeVersion` in [versions.go](../../internal/hub/versions.go)). A promote asks for the prod version of the same commit.
10. **Apps that already run.** If an app has no `vX.Y.Z` release and prod runs a commit, Homelab makes the first one once. It retries every 10 minutes if that fails (`bootstrapVersions`, `versionRetry`).
11. **Give it to the app.** The release is stored on the deploy record. The app's container gets it as `HOMELAB_VERSION` (set in `spec` in [hub.go](../../internal/hub/hub.go)). Only the app's container gets it, not its extra containers.
12. **Apps list their releases.** An app calls `GET /api/releases` on `HOMELAB_API_URL`. homelabd checks the app's `releases:read` scope and forwards the call with the repo and token. The token never reaches the app.

## Deep dive

**Files**

| File | What it does |
|---|---|
| [services/homelab-services/src/releases.rs](../../services/homelab-services/src/releases.rs) | Version type, `classify`, `bump`, `biggest`, notes, `plan`, `publish`, `create`, the `release` command and its flags |
| [services/homelab-services/src/releases_github.rs](../../services/homelab-services/src/releases_github.rs) | The GitHub calls: resolve a commit, list tags, compare commits, read a file, create and list releases |
| [services/homelab-services/src/releases_api.rs](../../services/homelab-services/src/releases_api.rs) | `GET /api/releases` for apps, and the `/internal/releases/publish` and `/internal/releases/list` routes for homelabd |
| [services/homelab-services/src/releases_tests.rs](../../services/homelab-services/src/releases_tests.rs) | Tests for the version rules, the planner and the CLI flags |
| [services/homelab-services/src/lib.rs](../../services/homelab-services/src/lib.rs) | `VERSION`, read from `HOMELAB_VERSION` when the program is built |
| [services/homelab-services/src/main.rs](../../services/homelab-services/src/main.rs) | Sends `release ...` to the release command |
| [internal/hub/versions.go](../../internal/hub/versions.go) | Asks for versions on dev and prod, keeps the first-release retry, stores the notes, and reports problems to the companion |
| [internal/hub/versions_test.go](../../internal/hub/versions_test.go) | Tests for the Homelab side of versions |
| [internal/adapters/services/versions.go](../../internal/adapters/services/versions.go) | Calls homelab-services for publish and list |
| [internal/adapters/services/versions_test.go](../../internal/adapters/services/versions_test.go) | Tests for that adapter |
| [internal/api/services.go](../../internal/api/services.go) | Forwards `/api/releases` to homelab-services, for app keys only |
| [internal/api/releases_service_test.go](../../internal/api/releases_service_test.go) | Tests for the release route through the API |
| [internal/hub/keys.go](../../internal/hub/keys.go) | The `releases:read` scope constant, and the scopes an app key gets |
| [.github/workflows/release.yml](../../.github/workflows/release.yml) | Makes Homelab's own release after CI passes on `main` |
| [.github/workflows/test-build.yml](../../.github/workflows/test-build.yml) | Makes and replaces a PR's test version, and deletes it when the PR closes |
| [.github/workflows/pr-title.yml](../../.github/workflows/pr-title.yml) | Checks PR titles |
| [.github/workflows/ci.yml](../../.github/workflows/ci.yml) | The checks; the release waits for its `CI` run to pass |
| [.github/actions/build-dist/action.yml](../../.github/actions/build-dist/action.yml) | Builds the companion and the programs for a release |
| [scripts/build-release.sh](../../scripts/build-release.sh) | Builds `homelabd` and `homelab-services` into `dist/` with `checksums.txt` |
| [plugins/typescript/src/releases.ts](../../plugins/typescript/src/releases.ts) | `homelab.version()` and `releases.list()` in TypeScript |
| [plugins/python/src/homelab/releases.py](../../plugins/python/src/homelab/releases.py) | The same in Python |
| [plugins/rust/src/releases.rs](../../plugins/rust/src/releases.rs) | The same in Rust |
| [docs/platform-services.md](../platform-services.md#release-versions) | The API reference for releases and the CLI |

**Routes**

- `GET /api/releases?limit=`: an app lists its releases (needs `releases:read`). Newest first. Dev builds are pre-releases.
- `POST /internal/releases/publish` and `POST /internal/releases/list`: homelabd only. The `system` caller is the only one that may call them.

**CLI**

```sh
homelab-services release <prod|dev|test> --repo owner/name --commit <sha> \
  [--pr N --title "..."] [--first vX.Y.Z] [--always] [--dry-run]
```

- `GITHUB_TOKEN` is needed. It must be able to write releases. `GITHUB_API_URL` is optional.
- `--first` is the first release, when no `vX.Y.Z` tag exists yet. Without it, Homelab reads the version from `pyproject.toml`, `package.json` or `Cargo.toml` at that commit, else `v0.1.0`.
- `--always` makes a release even when no change needs one. It counts as a fix.
- `test` needs `--pr` and `--title`.

**Edge cases and rules**

- **Which titles release.** `feat` gives a new minor version. `fix`, and any title with no known type, gives a patch. `docs`, `chore`, `test`, `ci`, `style`, `refactor` and `build` give nothing (`classify`, `releases.rs`).
- **Breaking.** `feat!:`, any `type!:` or `BREAKING CHANGE` in the body. Before 1.0 it raises the middle number, after that the first one (`bump`).
- **Always on for apps and PRs.** Homelab asks for app versions with `always` on, and `test-build.yml` passes `--always`. So a build with no titles that count still gets the next version, as a fix. Every PR gets a test version.
- **Same commit, same release.** Running the command twice for a commit gives the same result (`plan`).
- **Tag on another commit.** Stops with "the tag ... already exists on another commit" (`plan`).
- **A new test push** on the same PR replaces the old test release, and deletes its tag (`test-build.yml`).
- **Dev reuse.** A commit that already has a dev version keeps it (`plan`).
- **GitHub refusal.** Homelab deploys the app without a version and shows "Can't make versions on GitHub" on the app page (`versions.go`).
- **Notes size.** Notes kept on a release are cut at 8 KB (`versionNotesMax`, `capNotes`).
- **Token secrecy.** The token is never printed or stored in a release. It goes to homelab-services only inside the call (`releases_api.rs`).
- **Scopes.** `releases:read` is in every app key's scopes (`appKeyScopes`, `keys.go`).
- **Build-time version.** `HOMELAB_VERSION` at compile time sets the version of the programs. Makefile and `build-release.sh` set it. This is a different thing from the app's `HOMELAB_VERSION` at run time.
- **Release of Homelab itself.** The first release is `v0.20.0`. Older releases were named `build-N` ([self-update.md](self-update.md)).

**Examples** (last prod release, titles merged since it, next prod version)

| Last release | Titles since then | Next prod version |
|---|---|---|
| `v0.4.0` | `fix: typo` | `v0.4.1` |
| `v0.4.0` | `feat: new page`, `fix: typo` | `v0.5.0` (the biggest change wins) |
| `v0.4.0` | `feat!: new login` | `v0.5.0` (before 1.0, breaking raises the middle number) |
| `v1.2.3` | `feat!: new login` | `v2.0.0` |
| `v0.4.0` | `docs: readme only` | no release (`v0.4.1` with `--always`) |

A dev build uses the same base as the release it would become. With a `feat:` since `v0.4.0`, dev builds are `v0.5.0-dev.1`, `v0.5.0-dev.2`, and so on, until prod takes `v0.5.0`.

**To change it**

- To change how titles map to version numbers, edit `classify` or `bump` in [releases.rs](../../services/homelab-services/src/releases.rs), then add a case to `releases_tests.rs`.
- To change what a dev or test tag looks like, edit `plan` in [releases.rs](../../services/homelab-services/src/releases.rs), and check the workflow patterns in [test-build.yml](../../.github/workflows/test-build.yml) and [self-update's version rules](../../internal/adapters/selfupdate/version.go) still match.
- To change which apps get versions, or when, edit `stampVersion`, `bootstrapVersions` or `versionChannel` in [versions.go](../../internal/hub/versions.go).
- To change what an app sees, edit `releases_api.rs` (app route) and the plugins in `plugins/*/src/releases.*`.
- To change Homelab's own release steps, edit [release.yml](../../.github/workflows/release.yml) and [build-dist](../../.github/actions/build-dist/action.yml), then check with a PR's test build.

**Test it**

```sh
make check                    # Go: gofmt, go vet, go test ./...
make check-services           # Rust: cargo fmt, clippy, cargo test (includes releases_tests.rs)
go test ./internal/hub/... ./internal/adapters/services/... ./internal/api/...
pytest plugins/python                     # Python plugin
cd plugins/typescript && npm ci && npm test
```

Status of this work: [HANDOVER.md](../../HANDOVER.md), section 3 (Release versions, Homelab's own releases).
