# Alerting

Implemented in `internal/alert` (notifiers) and `internal/config` (the narrow JSON loader that builds them), wired into `internal/ingest.Handler.Notifier`.

## Trigger logic

`store.Store.SaveEvent` already computed everything needed to decide when to alert — it returns `(issueID int64, isNew, isRegression bool, err error)`. `ingest.ServeEnvelope` fires a notification when `isNew || isRegression`, and never otherwise:

- **New issue**: first occurrence of a fingerprint. Always worth a notification.
- **Regression**: the issue's prior status was `resolved` and a new event just flipped it back to `unresolved`. Worth surfacing — a fix that didn't hold matters.
- **Neither**: a routine repeat occurrence of an already-unresolved issue, or a recurrence of an `ignored` issue (ignoring is a deliberate "don't tell me again" signal). No notification — this is what keeps alerting from becoming noise.
- **Duplicate delivery** (an SDK retry of the same `event_id`): `SaveEvent` returns `isNew=false, isRegression=false` unconditionally on this path, so a retried delivery can never trigger a second alert for something already reported.

## Delivery: async, bounded, best-effort

```go
if h.Notifier != nil && (isNew || isRegression) {
    h.notifyAsync(alert.NotifyEvent{...})
}
```

`notifyAsync` fires the notification in a **background goroutine** with a **fresh `context.Background()`** (not the request's context, which is canceled the instant the HTTP response is written) and a **10-second timeout**. This is a deliberate tradeoff: a slow or unreachable SMTP server or webhook endpoint must never block or fail the SDK's ingest request — the alert is a side effect, not part of the critical path. Delivery failures are logged (`h.logger().Error(...)`), never surfaced to the SDK. There is no retry queue — a missed alert due to a transient failure is not retried. This is an honest v1 tradeoff, not an oversight: a persistent retry/outbox system is real infrastructure work, disproportionate to what a solo-operator self-hosted tool needs for its first alerting pass.

## Notifiers

`alert.Notifier` is a one-method interface (`Notify(ctx, NotifyEvent) error`). `alert.MultiNotifier` (a `[]Notifier`) fans a single event out to every configured target, always attempting all of them even if one fails (an unreachable SMTP server must not silently suppress a working webhook), combining errors via `errors.Join`.

**`SMTPNotifier`** uses only stdlib `net/smtp` — no external mail library. `smtp.SendMail` handles the common case (STARTTLS, typically port 587) automatically. Implicit TLS (port 465 — the TLS handshake happens *before* any SMTP command, which `smtp.SendMail` cannot do) is handled by a small hand-built client using `crypto/tls.Dial` + `smtp.NewClient` directly (see `SMTPNotifier.sendImplicitTLS`).

**`WebhookNotifier`** POSTs a JSON body; if `Secret` is set, adds `X-Trackdown-Signature: sha256=<hex HMAC>` over the raw body (the GitHub/Stripe convention) via stdlib `crypto/hmac`.

## Why `internal/ingest` imports `internal/alert` directly (not a duplicated local interface)

`internal/ingest` already has a pattern of small local interfaces to avoid compile-time dependencies on sibling packages — `RateLimiter` (satisfied by `internal/ratelimit.Limiter`) and `auth.FailedAttemptLimiter` both work this way, since their shape is trivial (`Allow(string) bool`, built only from primitive types). **This doesn't work for `Notifier`**: `NotifyEvent` is a struct, and Go interface satisfaction requires an exact method signature match — a duplicated `ingest.NotifyEvent` struct, even if field-for-field identical to `alert.NotifyEvent`, would be a different type, and an `*alert.SMTPNotifier` would NOT satisfy a hypothetical `ingest.Notifier` interface expecting `ingest.NotifyEvent`. Rather than fight the type system, `internal/ingest` imports `internal/alert` directly — consistent with how it already imports `internal/grouping` and `internal/store` directly for the same kind of core, non-swappable composition.

## Testing

`internal/alert`'s tests exercise `SMTPNotifier` against a **real TCP connection** to a hand-rolled fake SMTP responder (the same technique Go's own `net/smtp` test suite uses) — not a mocked `Dial`. `WebhookNotifier`'s tests use `httptest.NewServer`. `internal/ingest/alert_test.go` proves the trigger logic end-to-end with a `recordingNotifier` that captures calls on a channel (since delivery is asynchronous, tests must wait for a delivery rather than assume it already happened).
