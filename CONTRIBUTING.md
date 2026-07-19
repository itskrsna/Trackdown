# Contributing to Trackdown

Thanks for your interest in contributing! Trackdown is a small project by design — a single binary, minimal dependencies, and a codebase that stays approachable. Contributions of all sizes are welcome, from typo fixes to new SDK-compatibility work.

## Development setup

Prerequisites:

- [Go](https://go.dev/dl/) 1.22 or later
- Git

Clone and build:

```bash
git clone <repo-url>
cd trackdown
go build ./...
go test -race ./...
```

Or use the provided build helpers, which wrap the same commands (`make` on Linux/macOS, `./build.ps1` on Windows):

```bash
make build          # ./build.ps1          on Windows
make vet             # ./build.ps1 -Vet
make test            # ./build.ps1 -Test
make race-test       # ./build.ps1 -Race
make cross-compile-check   # ./build.ps1 -CrossCheck
make snapshot        # ./build.ps1 -Snapshot   (real binaries via goreleaser, fully local)
```

Run the server locally:

```bash
go run ./cmd/trackdown serve -insecure-no-auth   # -insecure-no-auth for local dev only
```

Trackdown is pure Go with no CGO dependencies, so `go build` works the same way on Windows, macOS, and Linux. If you're on Windows and a shell doesn't see `go` yet after installing it, refresh your session's PATH from the ones set at Machine/User scope, or open a new terminal. **The race detector (`-race`) needs a C compiler even for this pure-Go project** — this is a quirk of how Go's race detector runtime is built, not something Trackdown's own code requires.

## Before you submit a PR

- `go build ./...` and `go vet ./...` must pass
- `go test -race ./...` must pass — the race detector is not optional here
- Add or update tests for any behavior change. For protocol/ingest work, prefer a real Sentry SDK fixture (see `testdata/envelopes/`) over a hand-written mock — the actual SDK output is the source of truth for what a valid envelope looks like
- Update the relevant page under `docs/` if you changed user-facing behavior, configuration, or the API. A feature isn't done until its docs page reflects it
- Keep PRs focused — one logical change per PR is easier to review and merge

## Where to start

Look for issues labeled `good first issue`. Strong first contributions include:

- A new SDK-compatibility fix (something a real Sentry SDK sends that Trackdown doesn't yet parse correctly)
- A new alert notifier (webhook shape for a specific tool, etc.)
- Documentation improvements

## Reporting bugs / requesting features

Please use the issue templates — they help us get the information we need quickly. For a compatibility bug (a Sentry SDK feature that doesn't work against Trackdown), include the SDK name/version and, if possible, the raw envelope payload.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to abide by its terms.
