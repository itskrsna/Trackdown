// Captures real envelope bytes emitted by @sentry/node and writes them to
// every Go package's testdata/envelopes/ that needs them (mirrors
// tools/genfixtures' behavior for sentry-go). Run with `node capture.js`
// after `npm install` in this directory. Not part of the shipped product —
// dev tooling only, rerun after any @sentry/node upgrade to catch wire
// format drift.
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const Sentry = require("@sentry/node");

const outDirs = [
  path.join("..", "..", "internal", "protocol", "testdata", "envelopes"),
  path.join("..", "..", "internal", "store", "testdata", "envelopes"),
  path.join("..", "..", "internal", "ingest", "testdata", "envelopes"),
  path.join("..", "..", "internal", "grouping", "testdata", "envelopes"),
];

for (const dir of outDirs) {
  fs.mkdirSync(dir, { recursive: true });
}

function writeFixture(name, body) {
  for (const dir of outDirs) {
    const outPath = path.join(dir, name);
    fs.writeFileSync(outPath, body);
    console.log(`wrote ${outPath} (${body.length} bytes)`);
  }
}

function capture(name, emit) {
  return new Promise((resolve, reject) => {
    let body = null;
    const server = http.createServer((req, res) => {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => {
        if (body === null) body = Buffer.concat(chunks);
        res.writeHead(200);
        res.end();
      });
    });

    server.listen(0, "127.0.0.1", async () => {
      const port = server.address().port;
      const client = Sentry.init({
        dsn: `http://public@127.0.0.1:${port}/1`,
        // @sentry/node auto-detects integrations (http, console, etc.) that
        // are irrelevant noise for a fixture-capture script and slow
        // shutdown down; keep this minimal and deterministic.
        defaultIntegrations: false,
        tracesSampleRate: 0,
      });

      try {
        await emit();
        await Sentry.flush(2000);
      } catch (err) {
        server.close();
        reject(err);
        return;
      }

      server.close(() => {
        if (!body) {
          reject(new Error(`no envelope captured for ${name}`));
          return;
        }
        writeFixture(name, body);
        resolve();
      });
    });
  });
}

async function main() {
  await capture("sentry-node-exception.envelope", () => {
    const inner = new Error("inner cause");
    const outer = new Error("outer failure", { cause: inner });
    Sentry.captureException(outer);
  });

  await capture("sentry-node-message.envelope", () => {
    Sentry.captureMessage("hello from trackdown fixture generator");
  });

  await capture("sentry-node-unhandled.envelope", () => {
    Sentry.captureException(new TypeError("simulated unhandled-style error"));
  });
}

main().catch((err) => {
  console.error("genfixtures-node:", err);
  process.exit(1);
});
