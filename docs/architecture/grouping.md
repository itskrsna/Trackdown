# Grouping (fingerprinting)

Implemented in `internal/grouping/fingerprint.go`. This is Trackdown's own algorithm — not a port of Sentry's considerably more elaborate one — tuned for the common case of "the same underlying bug crashed twice."

## The goal

Two events that represent the same bug should collapse into one **issue** with an incrementing count, rather than flooding storage with one row per crash. `Fingerprint(ev *protocol.Event) string` returns a stable identifier: same fingerprint ⇒ same issue.

## The algorithm

1. **If the event has an exception** (`ev.Exception`), use the **outermost** entry — `Exception[len(Exception)-1]`. sentry-go (and the general chained-error convention) appends the root cause first and the user-visible error last; the outermost one is what a person actually sees and is the most stable anchor. Grouping keys off its `Type` plus its normalized stack trace (below). Changing an *inner* cause's type/value does not change the fingerprint — different root causes producing the same visible failure are still "the same issue" from a user's perspective.
2. **Else if the event has threads with a stacktrace** (`ev.Threads` — how sentry-go represents `CaptureMessage` and recovered panics, which have no Go `error` value to attach an exception to), use the stacktrace of the thread marked `Current`, combined with the message text.
3. **Else**, fall back to the message text alone.

## Stack trace normalization

Given a set of frames, three things are normalized before they become part of the fingerprint:

- **In-app filtering**: if any frame has `in_app: true`, only those frames are used. Library/runtime frames are shared by many unrelated bugs and are pure noise for grouping purposes.
- **Line/column stripping**: only `(module, function)` is kept per frame — line and column numbers shift across otherwise-identical builds (an unrelated code change two lines above a bug moves its line number) and would otherwise fragment one issue into many.
- **Recursion collapsing**: consecutive frames with the same `(module, function)` collapse to one. A stack overflow from 4 levels of recursion and one from 40 levels fingerprint identically — recursion depth is an accident of the specific run, not a distinguishing feature of the bug.

## Title

`Title(ev *protocol.Event) string` produces the human-readable issue title, using the same exception-then-message precedence as `Fingerprint` — the title always describes what was actually grouped on. Format: `"<Type>: <Value>"`, or just `<Type>` if there's no value, or the message text, or `"(no message)"` if there's nothing at all.

## What this does NOT do (yet)

- No custom user-supplied fingerprints (Sentry SDKs can send a `fingerprint` field to override grouping — not read yet)
- No "generated code" / minified-frame detection
- No frame-similarity heuristics for near-miss matching — grouping is exact-match on the normalized components, not fuzzy

## Storage integration

`internal/ingest` computes `Fingerprint`/`Title` right before calling `store.SaveEvent`, which upserts the `issues` table (see `internal/store/store.go`): first occurrence creates the issue (`event_count = 1`), subsequent occurrences with the same `(project_id, fingerprint)` increment `event_count` and bump `last_seen`. A resolved issue that gets a new occurrence automatically regresses to `unresolved`; an ignored issue stays ignored (a deliberate suppression, not something a new occurrence should silently override).
