# Rules for AI coding helpers

These rules are for any AI helper: Gemini, Claude, Codex or others. A person (often a beginner) is steering you. Use plain words and short answers.

## Start here

1. Read HANDOVER.md first. It has the current state and open work. Its section 6 ("Rules") applies to you too.
2. Then read docs/how-it-works.md. It says what each part does and where its code is.
3. For the human steps (GitHub, PRs), see CONTRIBUTING.md.

## Build and test

- `make test`: all Go and Rust checks. Run it before every push. CI runs the same.
- `make check`: gofmt, `go vet`, `go test`.
- `make check-services`: Rust format, clippy and tests (in `services/`).
- `make build`: builds `bin/homelabd` and `bin/homelab-services`.
- `make test-docker`: Docker end-to-end test. Needs Docker running.
- Companion (`web/`): `cd web && npm ci && npm run check && npm test`.
- Plugins (`plugins/`): see each folder's README and the `plugins` job in `.github/workflows/ci.yml`.
- If gofmt fails, run `gofmt -w .`.
- Needs Go 1.27+, Node 22+ and Rust (with rustfmt and clippy).

## How to work

- Make small, focused PRs. One idea each.
- Fix the cause, not the symptom. Find out why it broke first.
- Add a test with every fix and every new feature.
- Keep the docs current: every PR that changes how something works must update HANDOVER.md (and docs/how-it-works.md if a block changes) in the same PR.
- Do not edit the old milestone docs. They are history.
- Do not invent features, commands or paths. Check that they exist in the repo.
- Write short, plain sentences in docs. No em-dashes. Explain jargon in one line.

## Git

- Never push to `main`. Work on a branch and open a PR with `gh pr create`.
- Push branches to this repo. Do not fork. The AI reviewer cannot run on forks.
- Never merge a PR. Labiba reviews and merges.
- Do not commit or push unless the person asks you to.

## Secrets

Only Labiba sees app secret values. Never ask for them, print them, log them, or commit them. This applies to you and to any AI helper. Do not put secrets in code, tests, logs, screenshots or PR text.

## Mars (the real box)

- On mars, Homelab only touches its own containers (homelab-*). Never change Gabriele's own containers (Home Assistant, matterbridge, cloudflared, matter-server, tplink-proxy).
- Never run things on mars or change production without asking Labiba first.
- Ask before running anything that is not a local build or test.
