# JS sourcemap symbolication

Implemented in `internal/symbolicate` (the Source Map v3 parser/resolver), `internal/store` (the `releases`/`artifacts` tables), `internal/ingest` (the release/artifact upload endpoints), and `internal/server` (wiring resolved positions into the event-detail page). This is display-only — see "Why grouping is unaffected" below before assuming a gap here is a bug.

## The problem

Real Sentry SDKs for JavaScript send stack frames pointing at *minified* production code (`abs_path`, `lineno`, `colno` referring to a bundled/minified file, e.g. `app.min.js:1:48213`). Without symbolication, that's all Trackdown can show — accurate, but useless for actually finding the bug in source. A Source Map (`.map` file, produced by every JS bundler — webpack, esbuild, Rollup, etc.) records how every position in the generated file maps back to the original source. Symbolication is the process of using that map to resolve a minified frame back to its original file/line/column/function name.

## `internal/symbolicate`: the Source Map v3 parser

A from-scratch implementation — no third-party sourcemap library — matching this project's convention of implementing wire/file formats itself rather than depending on one more package for something this self-contained. The interesting part is decoding the `"mappings"` field: a string of semicolon-separated *lines* (one entry per generated line), each containing comma-separated *segments* (one entry per meaningfully-distinct generated column on that line), where each segment is 1, 4, or 5 fields encoded as **Base64-VLQ** (a variable-length quantity encoding borrowed from the Closure Compiler — 5 bits of magnitude per base64 digit, a continuation bit, and a sign bit on the final assembled value).

The fields, if present, are: generated-column delta, source-file-index delta, source-line delta, source-column delta, and name-index delta — all deltas relative to the *previous* value of that same field (generated-column resets to 0 at the start of each new line; the other four are cumulative across the entire mappings string, not per-line). `Resolve(genLine, genCol int) (Position, bool)` finds the segment whose generated column is the largest one `<=` the query (a binary search, since segments are emitted in non-decreasing column order per the spec) and returns its original position.

**Convention note**: `Resolve`'s inputs and outputs are 0-based, matching the Source Map spec's own internal encoding exactly — not the 1-based line-numbering convention JS stack traces (and this project's `protocol.Frame.Lineno`) use. `internal/server`'s calling code is the one place that converts: subtract 1 from `Frame.Lineno`/`Frame.Colno` before calling `Resolve`, add 1 back to the resolved line for display. Keeping the package itself spec-literal, rather than guessing at a "friendlier" convention, keeps the one place that actually needs the conversion to be right the only place doing any conversion at all.

**Testing**: the decoder is checked against ground truth it did not produce itself — a hand-built mappings string decoded independently by Node's `source-map` reference library (the library the wider JS ecosystem relies on), and a real sourcemap produced by actually running `esbuild --minify --sourcemap` on a real multi-function JS file, again cross-checked against the same reference library. This is the same "real tool as conformance oracle" philosophy used for Sentry SDK wire-format fixtures, applied here to the bundler/sourcemap side (`internal/symbolicate/sourcemap_test.go`).

## Release/artifact model

Real Sentry handles sourcemaps via a separate Release/Artifact API — envelope ingest was never the right channel for uploading a `.map` file, and Trackdown follows the same shape:

- `POST /api/{project_id}/releases` — body `{"version": "..."}`. Idempotent: re-registering an already-known version (a CI pipeline re-running the same release) succeeds and returns the existing release's id rather than erroring.
- `POST /api/{project_id}/releases/{version}/artifacts?abs_path=<url-encoded-abs-path>` — the request body is the raw sourcemap file content itself (not wrapped in JSON), keyed by the exact `abs_path` a frame's `AbsPath` field will reference. The release doesn't need a prior explicit `CreateRelease` call — like project auto-provisioning on first envelope ingest, referencing a release here creates it if needed, so a CI pipeline can upload in one step. Re-uploading the same `(release, abs_path)` replaces the stored content (a rebuild producing a new map for the same file/version).

Storage: two tables, `releases` (`project_id, version` — unique together) and `artifacts` (`release_id, abs_path, sourcemap_content` — unique together), both plain columns rather than JSON blobs, consistent with the rest of the schema.

## Resolution at render time

`internal/server`'s event-detail handler resolves symbolication **lazily, at render time**, not at ingest time — stored events are never mutated with resolved data. For each exception frame with a non-empty `AbsPath` and a positive `Lineno`: look up `FindArtifact(projectID, event.Release, frame.AbsPath)`; if found, parse it and call `Resolve`. Both steps fail *silently* into "render the raw frame" — a missing artifact (`sql.ErrNoRows`, the common case for any event ingested before its sourcemaps were uploaded, or one with no `release` set at all) or an unparseable sourcemap must never turn into a 500. Sourcemap lookups+parses are cached per `abs_path` for the duration of a single render, since one stack trace commonly has several frames from the same bundle file.

The event-detail template shows a `symbolicated` badge and the resolved `file:line:column[ in function]` alongside the raw minified location when resolution succeeds — never replacing the raw frame, only augmenting it.

## Why grouping is unaffected

`internal/grouping`'s fingerprinting still operates on raw, unsymbolicated `(module, function)` pairs — a deliberate scope line for this version, not an oversight (see `docs/architecture/grouping.md`). Feeding resolved original names into the fingerprint algorithm would retroactively change fingerprints for issues that were already grouped before their sourcemaps existed, which is a data-migration-shaped problem (existing issue rows would need re-keying, or old and new events would silently stop grouping together) rather than a simple feature addition. Symbolication in this version is display-only.
