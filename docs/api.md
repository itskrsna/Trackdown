# API reference

Endpoints implemented in `internal/ingest/ingest.go` (`Handler.Register`). This page is kept in sync with that file — if they disagree, the code is right and this page is stale.

## Ingest

### `POST /api/{project_id}/envelope/`

Accepts a Sentry envelope (the modern ingest format used by current Sentry SDKs). Requires authentication via either:

- an `X-Sentry-Auth` header, e.g. `Sentry sentry_version=7, sentry_client=sentry.go/0.48.0, sentry_key=<public_key>` (what official SDKs send), or
- a `sentry_key` query parameter (a fallback some transports use)

The project is created automatically on first use, with whatever public key was used to authenticate — there's no separate provisioning step. Only envelope items of `type: "event"` are parsed and stored; other item types are accepted but currently dropped.

Request bodies may be gzip-compressed (`Content-Encoding: gzip`) — required for `sentry-sdk` (Python), which compresses every envelope by default. `Content-Encoding: br` (Brotli) is not supported and returns `400`.

Every stored event is grouped into an **issue** by its fingerprint (see [architecture/grouping.md](architecture/grouping.md)) — repeated occurrences of the same bug increment one issue's count rather than each becoming a separate thing to look at.

Returns `200` with `{"id": "<event_id>"}` on success, `401` if no key is present, `400` if the envelope can't be parsed.

### `POST /api/{project_id}/store/`

Accepts a legacy single-event JSON payload (used by older Sentry SDKs). Planned; not yet implemented.

## Inspection (temporary — a real management API replaces this later)

### `GET /api/{project_id}/events/`

Returns a JSON array of every stored event for the project, most recently received first.

### `GET /api/{project_id}/events/{event_id}`

Returns a single stored event by its Sentry `event_id`, or `404` if not found.

### `GET /api/{project_id}/issues/`

Returns a JSON array of every issue for the project, most recently active first. Each issue has `ID`, `Fingerprint`, `Title`, `Status` (`unresolved` | `resolved` | `ignored`), `FirstSeen`, `LastSeen`, `EventCount`.

### `GET /api/{project_id}/issues/{issue_id}`

Returns a single issue by its numeric ID, or `404` if not found. `400` if `issue_id` isn't a number.

### `GET /api/{project_id}/issues/{issue_id}/events`

Returns every event linked to that issue, most recently received first.

### `POST /api/{project_id}/issues/{issue_id}/resolve`

### `POST /api/{project_id}/issues/{issue_id}/ignore`

### `POST /api/{project_id}/issues/{issue_id}/reopen`

Transition an issue's status. Return `204` on success, `404` if the issue doesn't exist. A new event arriving for a `resolved` issue automatically reopens it (a regression); one arriving for an `ignored` issue does not change its status.

## Operations

### `GET /healthz`

Unauthenticated. Returns `200` with body `ok` if the store is reachable, `503` otherwise. Suitable for load balancer / container orchestrator / uptime-monitor health checks.
