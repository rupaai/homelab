---
feature: own-releases
status: ready
---
## Line
- **Homelab's own releases**: `release.yml` runs `homelab-services release` for `vX.Y.Z` (first `v0.20.0`) after CI passes on main (no checks of its own); both it and `test-build.yml` build through `.github/actions/build-dist`; test versions are `vX.Y.Z-pr.N.<commit>`; the companion shows the update's new/fix counts and notes (older ones were `build-N`).

## What changed
Release waits for CI (`workflow_run`) instead of rerunning checks. Shared build steps moved to a composite action. New `make check-web`.
