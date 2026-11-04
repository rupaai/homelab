---
feature: on-the-device
status: ready
---
## Line
- **On the device** (milestone 6): `homelabd setup` installs it; it updates itself from the
  companion (Settings → Resources: Update, or try a test version then Keep) and goes back on
  its own if a new version doesn't stay up. Admins can also restart it from Resources. Done.
  Versions: `build-N` or `vX.Y.Z` (any `v` is newer than any `build`); test versions
  `pr-N-<commit>` or `vX.Y.Z-pr.N[.<commit>]` (`internal/adapters/selfupdate/version.go`).
  Releases are still named `build-N`. It checks GitHub for a new version every hour; Refresh
  on that screen checks right away.

## What changed
Adds "Restart Homelab" under Resources, for admins. It is shown only where systemd runs
Homelab, so not on a Mac.
