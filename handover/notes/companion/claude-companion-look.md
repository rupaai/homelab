---
feature: companion
status: ready
new: true
---
## Line
- **Companion nav**: 4 tabs for admins (Apps, Activity, Storage, Settings), only Apps for everyone else (no tab bar). A round avatar opens `/me`. Apps is one list at `/` (`/home` redirects), with a summary line (apps healthy, having problems, down) and cards with Dev and Prod rows. Activity has Events and Logs, Storage has Files and Databases. Settings is a list: This device, People, Homelab passwords and keys (`/settings/homelab`), API keys, Your account. Tabs are in `web/src/lib/nav.ts`. The look is one dark theme ("plain dark"), tokens in `web/src/app.css`; design facts are in `PRODUCT.md` and `.impeccable/`. Done.

## What changed
Dark theme on every device (no light palette), new tokens and shared styles, new header and tab bar, new Apps cards with a summary line, calmer danger buttons, dark theme colour in app.html and the manifest.
