---
feature: secrets
status: ready
---
## Line
- **Secrets**: encrypted in homelab-services, key in `<home>/secrets/master.key` (homelabd makes it; the old Go store code is gone and only its data is imported once). Set in the
  companion. Only the owner, and people given "Can see secrets", can see values. API keys
  never read secrets. Views are logged, 5 versions kept, hidden in logs. Apps get them as files
  in `/run/secrets` (and as environment variables unless `secrets_as: files`).
  `generate_secrets` makes them automatically. Apps can save their own through the API. Done.

## What changed
Removed the unused old Go secrets store code and a few other unused functions.
