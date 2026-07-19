// Sends a real @sentry/node event to an already-running Trackdown server,
// for manual end-to-end verification against the actual compiled binary
// (not just an in-process test server). Mirrors tools/e2echeck (Go).
// Usage: node check.js <host:port>
const Sentry = require("@sentry/node");

async function main() {
  const host = process.argv[2];
  if (!host) {
    console.error("usage: node check.js <host:port>");
    process.exit(2);
  }

  // @sentry/node's own DSN parser requires the project-id path segment to
  // be numeric -- same constraint as sentry-sdk (Python), confirmed
  // directly against a real "Invalid projectId" error from this SDK. Only
  // sentry-go accepts an arbitrary string there. Trackdown itself treats
  // project IDs as opaque strings, but JS/Python SDK users are constrained
  // to numeric ones.
  const projectID = "737373";
  Sentry.init({
    dsn: `http://public@${host}/${projectID}`,
    defaultIntegrations: false,
    tracesSampleRate: 0,
  });

  const eventId = Sentry.captureException(new Error("manual e2e check from @sentry/node"));
  const flushed = await Sentry.flush(2000);
  if (!flushed) {
    console.error("flush timed out");
    process.exit(1);
  }
  console.log(`sent event_id=${eventId} to project ${projectID} on ${host}`);
}

main();
