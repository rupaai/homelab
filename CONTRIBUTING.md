# Contributing to Homelab

Welcome. This guide takes you from nothing to your first pull request. A pull request (PR) is a request to add your change to the project. Go in order. Each step is short.

Before you start, know two things:

- For what is going on right now, read [HANDOVER.md](HANDOVER.md) and the waiting notes in `handover/notes/`.
- For how Homelab works, read [docs/how-it-works.md](docs/how-it-works.md).

## 1. GitHub setup, from zero

1. Make a free account at https://github.com if you do not have one.
2. Labiba will invite you to the repo. Open the email from GitHub, or go to https://github.com/rupaai/homelab/invitations, and accept it.
3. Install Homebrew, the Mac package manager, if you do not have it. The command is on https://brew.sh.
4. Install git (saves your work history) and gh (the GitHub command line tool):

```sh
brew install git gh
```

5. Log in to GitHub. Follow the questions it asks. Choose GitHub.com, HTTPS, and log in with the web browser:

```sh
gh auth login
```

6. Tell git who you are. Use the email on your GitHub account:

```sh
git config --global user.name "Your Name"
git config --global user.email "you@example.com"
```

7. Download the project (this is called cloning):

```sh
gh repo clone rupaai/homelab
cd homelab
```

Important: push your branches to this repo. Do not fork it. The AI reviewer cannot run on forks. You do not need a fork because Labiba made you a collaborator.

## 2. Install the tools

Homelab uses four things. You need them to build and test.

| Tool | Version | Why |
|---|---|---|
| Go | 1.27 or newer | the main program, `homelabd` |
| Node | 22 or newer | the companion web app in `web/` |
| Rust | current stable, with `rustfmt` and `clippy` | `homelab-services` in `services/` |
| Docker | OrbStack on a Mac | only for the Docker end-to-end test and for running apps |

On a Mac:

```sh
brew install go node@22
brew install --cask orbstack
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
rustup component add rustfmt clippy
```

Open a new terminal after the Rust install. Check the versions with `go version`, `node --version` and `cargo --version`.

Now run the checks. This is the same set CI runs:

```sh
make test
```

`make test` runs two things:

- `make check`: gofmt (code format), `go vet` (finds common mistakes) and `go test`.
- `make check-services`: the Rust format check, `cargo clippy` (a Rust linter) and `cargo test`.

Other commands you may need:

```sh
make build          # builds bin/homelabd and bin/homelab-services
make test-docker    # Docker end-to-end test. Needs Docker running.
cd web && npm ci && npm run check && npm test    # companion checks
```

If you only changed code in `web/`, also run the companion checks above. If a `gofmt` error appears, run `gofmt -w .`.

## 3. Make your first change

Never work on `main`. Make a branch for each change:

```sh
git checkout main
git pull
git checkout -b my-small-change
```

Make one small change. Add or update a test with it. Then check it:

```sh
make test
```

Save it in git (a commit) and push it:

```sh
git add -A
git commit -m "Say what you changed in one short line"
git push -u origin my-small-change
```

Open the pull request. Fill in the template that appears:

```sh
gh pr create
```

## 4. What happens next

1. CI (automatic checks on GitHub) runs. It runs the Go, Rust, companion and plugin checks. Watch it with `gh pr checks`.
2. Every open PR from this repo also gets a test version of Homelab, built as a pre-release. Labiba can try it.
3. An AI reviewer (Claude) reads your PR and leaves comments. Read them. Fix what is right. Push again, and CI and the review run again.
4. Labiba reviews and merges. Do not merge your own PRs.
5. After a merge to `main`, a new numbered release is built.

## 5. Using Gemini

You will probably use Gemini CLI (Google's AI helper in the terminal). It reads `GEMINI.md` in the repo by itself. That file points to [AGENTS.md](AGENTS.md), which holds the rules for any AI helper. You do not need to paste anything.

Start it inside the `homelab` folder:

```sh
gemini
```

Example prompts:

- "Read HANDOVER.md and docs/how-it-works.md. Then explain Homelab to me in simple words."
- "Where in the code is the self-update done? Show me the files and how to test it."
- "I want to fix a typo in the README. Make a branch, fix it, and tell me how to open a PR."
- "Run make test and explain any error in simple words. Do not change code yet."
- "Write a test for this function first, then fix the bug it finds."

You are in charge, not the AI. Read what it changes before you commit. The rules below apply to the AI too.

## 6. Rules

- Keep the docs current: do not edit the feature blocks in HANDOVER.md. Add a note in `handover/notes/<feature>/` (see [handover/README.md](handover/README.md)), and mark it `ready` when the feature is done. A bot folds it into HANDOVER.md. Update docs/how-it-works.md in the same PR if a block changes.
- Secrets: only Labiba sees app secret values. Never ask for them, print them, log them, or commit them. This applies to you and to any AI helper.
- On mars, Homelab only touches its own containers (homelab-*). Never change Gabriele's own containers (Home Assistant, matterbridge, cloudflared, matter-server, tplink-proxy).
- Never run things on mars or change production without asking Labiba.
- Add tests with every fix. A fix with no test can break again.
- Do not push to `main`. Always use a branch and a PR.
- Do not merge your own PRs.
- Keep PRs small. One idea per PR.
- Fix the cause, not the symptom (the visible sign of the problem).

`mars` is the small Ubuntu box at home where Homelab runs for real. You do not need it to contribute. Your Mac is enough.

## Stuck?

Ask Labiba. A short question early is better than a long fight alone.
