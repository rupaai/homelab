---
feature: companion
status: ready
new: true
---
## Line
- **Companion nav**: 4 tabs for admins (Apps, Activity, Storage, Settings), only Apps for everyone else (no tab bar). A round avatar opens `/me`. Apps is one list at `/` (`/home` redirects). Activity has Events and Logs, Storage has Files and Databases. Settings is a list: This device, People, Homelab passwords and keys (`/settings/homelab`), API keys, Your account. Tabs are in `web/src/lib/nav.ts`. Done.

## What changed
New nav, Settings list, view switch, wider desktop page. Small fixes: email input style, quiet Sign out, More menu on app environments.
