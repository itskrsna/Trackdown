# Configuration

Kept in sync with `cmd/trackdown/main.go`'s actual flag definitions — if this page and the code disagree, the code is right and this page is stale.

## `trackdown serve`

| Flag | Default | Description |
|---|---|---|
| `-addr` | `:8080` | HTTP listen address |
| `-db` | `trackdown.db` | Path to the SQLite database file |
| `-log-format` | `text` | Log output format: `text` (human-readable, good for a terminal or simple log file) or `json` (structured, for log aggregation pipelines) |
| `-insecure-no-auth` | `false` | Disable admin authentication entirely. **Development only — never use this in production.** |
| `-ingest-rate-limit` | `20` | Max envelope-ingest requests per second, per client IP (burst allowance is 3x this value) |
| `-config` | *(unset)* | Path to a JSON file configuring SMTP/webhook alerting. Omit to disable alerting entirely — nothing else in Trackdown requires this file. |
| `-retention-days` | `0` | Delete events older than N days, checked once at startup and then daily. `0` disables retention entirely — Trackdown never silently deletes your data unless you opt in. |

## Data lifecycle: retention and backups

**Retention** deletes old *event* rows (including their JSON payloads) but keeps *issue* rows forever as historical aggregate summaries (title, status, first/last seen, event count) — it's the bulky per-event data that needs a limit in practice, not the small summary. Two ways to run it:

- In-process: pass `-retention-days N` to `trackdown serve` — it runs once immediately at startup and then every 24 hours for as long as the server runs.
- Standalone, for external cron / Windows Task Scheduler: `trackdown gc -db trackdown.db -retention-days N` — opens the database, deletes, prints a count, and exits. Use this if you'd rather manage the schedule yourself than rely on the in-process ticker.

**Backups**: `trackdown backup -db trackdown.db <dest-path>` produces a consistent, complete, independently-openable snapshot via SQLite's `VACUUM INTO` — safe to run while the server is live, with no risk of the torn/inconsistent copy a raw file copy could produce if a write happens mid-copy. See [Self-hosting](self-hosting.md) for backup scheduling guidance.

## Alerting config file

A narrow JSON file — scoped only to SMTP/webhook settings, since those have enough related knobs (host/port/credentials/recipients, one or more webhook URLs) that flags stop being readable. Everything else about Trackdown stays flag/env-var configured; there is no general server config file.

```json
{
  "smtp": {
    "host": "smtp.example.com",
    "port": 587,
    "username": "trackdown",
    "password": "smtp-password",
    "from": "trackdown@example.com",
    "to": ["ops@example.com"],
    "implicit_tls": false
  },
  "webhooks": [
    {"url": "https://example.com/hooks/trackdown", "secret": "a-shared-secret"}
  ]
}
```

Both sections are optional and independent — configure either, both, or neither. `implicit_tls: true` is for port-465-style SMTP (TLS negotiated before any SMTP command); the far more common case (port 587, STARTTLS) is `false`/omitted. A webhook's `secret`, if set, adds an `X-Trackdown-Signature: sha256=<hex>` header (HMAC-SHA256 over the raw JSON body) so receivers can verify the request actually came from this Trackdown instance.

An issue triggers a notification when it's created (a genuinely new bug) or when it regresses (a previously `resolved` issue gets a new occurrence) — not on every single event, and not when an `ignored` issue recurs (that's a deliberate suppression). Delivery is best-effort with a 10-second timeout and no retry queue: a slow or unreachable SMTP server/webhook endpoint never blocks or fails the SDK's ingest request, and failures are logged, not surfaced to the SDK.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `TRACKDOWN_ADMIN_USER` | `admin` | Username for the management API and web UI (HTTP Basic Auth) |
| `TRACKDOWN_ADMIN_PASSWORD` | *(required)* | Password for the management API and web UI. The server refuses to start without this set, unless `-insecure-no-auth` is passed. |

**What's protected and what isn't:** the ingest endpoint (`POST /api/{project_id}/envelope/`) is deliberately never protected by the admin credential — it's authenticated only by the Sentry DSN's public key, matching real Sentry semantics (DSN keys are meant to be embedded in client-side app code, not treated as secrets). Every other endpoint — the issue/event management API and the web UI — requires the admin credential. `GET /healthz` is also unauthenticated, since health checks shouldn't need credentials.

**Rate limiting:** because ingest is unauthenticated by design and the store serializes all writes through a single connection, an unthrottled flood to a guessed or leaked project ID is a real DoS vector — `-ingest-rate-limit` (a per-IP token bucket) exists specifically to close it; exceeding it returns `429` with a `Retry-After` header. Repeated failed admin-login attempts are separately throttled (5 attempts, refilling one every 12 seconds, per IP) — this is not configurable via a flag, since it's a fixed brute-force mitigation rather than a capacity knob. Correct credentials are never throttled, only failed ones.

More flags land here as later phases add them (rate limiting, retention, alerting) — each addition updates this table as part of its own definition of done, not as a separate pass.
