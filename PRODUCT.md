# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users
- Labiba (owner, admin): uses the companion most, mostly on an iPhone. Checks that apps are healthy, deploys, promotes, rolls back, restarts, and looks at logs, data and the device.
- Gabriele (partner): does some of the same admin and builder work.
- Family members: only open the apps shared with them (one tap "Open").

## Product Purpose
The companion is the web app (installable on the phone Home Screen) for Homelab, a self-hosted platform on a small home server called mars. It shows each app's dev and prod state and lets people deploy, promote and manage apps, files, databases, secrets, people and the device. Success: in one glance you know whether everything is fine, and the common actions take one or two taps.

## Positioning
Homelab is like an OS for developers on one home box: apps are deployed from GitHub repos, each with dev and prod, plus built-in services (files, databases, secrets, logs). The companion is its control panel, made for one household, not a cloud console.

## Operating Context
- Phone first (iPhone, Home Screen app), desktop second.
- Navigation: Apps, Activity (Events, Logs), Storage (Files, Databases), Settings. Family members see only Apps.
- The household's own apps (for example Northern Lights, a dark movie app) open from the companion.

## Capabilities and Constraints
- SvelteKit + Svelte 5, built into the Go binary. Design tokens live in `web/src/app.css`. Mock mode: `cd web && npm run dev`, open with `?mock`.
- No external font or asset hosts needed at runtime; the box may be offline from the internet.

## Brand Commitments
- Name "Homelab" in the header, the house icon, a blue accent.
- Labiba (2026-11-05): wants a "techie minimal" look with a tech lab feeling. Dark theme is welcome and should sit well next to the household's apps. Not too much colour.

## Product Principles
1. One glance tells you if something is wrong; healthy is quiet, problems stand out.
2. Fewest steps for common work; rare and risky actions sit one level deeper.
3. Simple, plain words. No jargon a beginner would not get.
4. Built for one household: personal, calm, never like an enterprise console.

## Accessibility & Inclusion
WCAG AA contrast in both themes, 44px touch targets, status never shown by colour alone (the status dot also has a shape and a word).
