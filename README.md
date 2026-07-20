# Trackdown

Sentry-compatible error tracking. One binary. Actually open source.

Trackdown is a single-binary, self-hosted error tracking server that speaks the Sentry wire protocol. Point any existing Sentry SDK — JavaScript, Python, Go, Java, .NET, and more — at Trackdown's DSN, and it just works. No SDK changes, no vendor lock-in, no 16-container deployment.

> **Status:** [v0.1.0](https://github.com/itskrsna/Trackdown/releases/latest) — functionally complete and self-hostable today: ingest verified against real Go, Node.js, Python, .NET, and Java SDK clients, plus auth, alerting, data retention/backup, and a web UI all built in. Still early days for real-world usage, though — no production deployments outside development/testing yet, so expect rough edges.

## Why

Sentry relicensed its core to the non-OSI [Functional Source License](https://sentry.io/), and self-hosting real Sentry means running a stack of roughly 16 containers (Kafka, ClickHouse, Redis, Postgres, and more). That's out of reach for small teams and solo developers who just want to know when their app breaks.

Open alternatives exist ([GlitchTip](https://glitchtip.com/), [Bugsink](https://www.bugsink.com/)) — but there's still no option that is:

- A single compiled binary — no Docker, no container orchestration
- Its own storage engine — no external database to run and maintain
- Under a real OSI-approved license — Apache-2.0, no relicensing risk
- Trivial to self-host on Windows, macOS, or Linux

That's the gap Trackdown fills.

## Features

- Sentry envelope wire protocol, implemented from scratch and verified against real Go, Node.js, Python, .NET, and Java SDK clients
- Automatic issue grouping via its own stack-trace fingerprinting algorithm
- A built-in web UI — issue list, event detail with exception chains, breadcrumbs, and tags — no separate frontend, no JS framework
- Email and webhook alerting on new issues and regressions, with durable retry on transient failures
- HTTP Basic Auth and per-IP rate limiting on the management API and ingest
- Data retention and point-in-time backup built in (`trackdown gc`, `trackdown backup`)
- Native Windows Service support (`trackdown service install`), plus documented systemd/launchd setups for Linux/macOS
- Display-only JS sourcemap symbolication for minified stack traces

## How it works

Because Trackdown implements Sentry's documented envelope protocol, every existing Sentry SDK is compatible on day one. If your app already uses `Sentry.init()`, switching to Trackdown is a one-line DSN change.

```
Any Sentry SDK ──(DSN)──> Trackdown (single binary) ──> your own storage, your own server
```

## Getting started

Download a prebuilt binary for Windows, macOS, or Linux from the [latest release](https://github.com/itskrsna/Trackdown/releases/latest), or build from source:

```bash
go run ./cmd/trackdown serve -insecure-no-auth   # local dev only -- see docs/getting-started.md for setting a real admin password
```

Then point a Sentry SDK at `http://localhost:8080/<project-id>` (see [`docs/sdk-setup/`](docs/sdk-setup/) for per-language instructions). Full setup — including authentication, self-hosting as a service, and configuring alerts — is in [`docs/getting-started.md`](docs/getting-started.md).

## Documentation

- [Getting started](docs/getting-started.md)
- [Self-hosting](docs/self-hosting.md)
- [SDK setup by language](docs/sdk-setup/)
- [Compatibility matrix](docs/compatibility.md) — which Sentry SDK features are supported today
- [Architecture](docs/architecture/) — for contributors

## Contributing

Contributions are very welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for how to get set up and find something to work on. New provider integrations, notifier channels, and SDK-compatibility fixes are great first contributions.

Please also read our [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Apache License 2.0 — see [LICENSE](LICENSE).