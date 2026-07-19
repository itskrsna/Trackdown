#!/usr/bin/env bash
# Downloads the Sentry Java SDK core jar into lib/ (gitignored — a build
# dependency, not shipped/committed source, same convention as
# node_modules/.venv for the Node/Python fixture-capture tools). The core
# `io.sentry:sentry` artifact has zero mandatory runtime dependencies of its
# own (confirmed by reading its POM on Maven Central — no <dependencies>
# block at all; it implements its own minimal JSON codec and uses
# java.net.HttpURLConnection for transport specifically to avoid needing
# GSON/OkHttp), so this one jar is the entire dependency set.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p lib
VERSION="8.49.0"
curl -sSL -o "lib/sentry-${VERSION}.jar" \
  "https://repo1.maven.org/maven2/io/sentry/sentry/${VERSION}/sentry-${VERSION}.jar"
echo "downloaded lib/sentry-${VERSION}.jar"
