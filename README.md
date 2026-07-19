# Trackdown

Sentry-compatible error tracking. One binary. Actually open source.

Trackdown is a single-binary, self-hosted error tracking server that speaks the Sentry wire protocol. Point any existing Sentry SDK — JavaScript, Python, Go, Java, .NET, and more — at Trackdown's DSN, and it just works. No SDK changes, no vendor lock-in, no 16-container deployment.

> **Status:** early development, pre-release. Not yet ready for production use.

## Why

Sentry relicensed its core to the non-OSI [Functional Source License](https://sentry.io/), and self-hosting real Sentry means running a stack of roughly 16 containers (Kafka, ClickHouse, Redis, Postgres, and more). That's out of reach for small teams and solo developers who just want to know when their app breaks.

Open alternatives exist ([GlitchTip](https://glitchtip.com/), [Bugsink](https://www.bugsink.com/)) — but there's still no option that is:

- A single compiled binary — no Docker, no container orchestration
- Its own storage engine — no external database to run and maintain
- Under a real OSI-approved license — Apache-2.0, no relicensing risk
- Trivial to self-host on Windows, macOS, or Linux

That's the gap Trackdown fills.

## How it works

Because Trackdown implements Sentry's documented envelope protocol, every existing Sentry SDK is compatible on day one. If your app already uses `Sentry.init()`, switching to Trackdown is a one-line DSN change.

```
Any Sentry SDK ──(DSN)──> Trackdown (single binary) ──> your own storage, your own server
```

## Getting started

See [`docs/getting-started.md`](docs/getting-started.md).

```bash
go run ./cmd/trackdown serve
```

Then point a Sentry SDK at `http://localhost:8080/<project-id>` (see [`docs/sdk-setup/`](docs/sdk-setup/) for per-language instructions).

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