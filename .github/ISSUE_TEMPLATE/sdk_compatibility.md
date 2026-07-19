---
name: SDK compatibility issue
about: A Sentry SDK feature doesn't work correctly against Trackdown
title: "[compat] "
labels: compatibility
---

**SDK**
Name and version (e.g. `@sentry/node` 8.x, `sentry-go` v0.28, `sentry-sdk` (Python) 2.x).

**What the SDK sent**
If possible, attach or paste the raw envelope payload (you can capture this with a local proxy, or Trackdown's own request logging).

**What Trackdown did instead**
Describe the incorrect behavior — dropped event, wrong grouping, missing field, error, etc.

**Expected behavior per the Sentry protocol**
Link to the relevant section of https://develop.sentry.dev/sdk/data-model/ or SDK docs if known.
