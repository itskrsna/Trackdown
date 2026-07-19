"""Captures real envelope bytes emitted by sentry-sdk (Python) and writes
them to every Go package's testdata/envelopes/ that needs them (mirrors
tools/genfixtures for sentry-go and tools/genfixtures-node for @sentry/node).

Run with:
    python -m venv .venv && .venv\\Scripts\\activate && pip install -r requirements.txt
    python capture.py

Not part of the shipped product -- dev tooling only, rerun after any
sentry-sdk upgrade to catch wire format drift.
"""
import gzip
import http.server
import os
import threading
import time

import sentry_sdk

OUT_DIRS = [
    os.path.join("..", "..", "internal", "protocol", "testdata", "envelopes"),
    os.path.join("..", "..", "internal", "store", "testdata", "envelopes"),
    os.path.join("..", "..", "internal", "ingest", "testdata", "envelopes"),
    os.path.join("..", "..", "internal", "grouping", "testdata", "envelopes"),
]

for d in OUT_DIRS:
    os.makedirs(d, exist_ok=True)


def write_fixture(name, body, content_encoding):
    # Write the exact wire bytes as captured -- this is what real ingest-layer
    # tests POST (with the matching Content-Encoding header) to prove
    # Trackdown's HTTP layer actually decompresses it. sentry-sdk's Python
    # transport gzips every envelope by default (compresslevel=9, no size
    # threshold -- confirmed by reading transport.py directly), so this is
    # the common case, not an edge case.
    for d in OUT_DIRS:
        out_path = os.path.join(d, name)
        with open(out_path, "wb") as f:
            f.write(body)
        print(f"wrote {out_path} ({len(body)} bytes, content-encoding={content_encoding})")

    # Also write a decompressed sibling so internal/protocol's parser-level
    # tests can exercise real envelope bytes directly, without needing to
    # know about HTTP transport concerns like gzip -- decompression belongs
    # at the ingest/HTTP layer, not in the envelope parser.
    if content_encoding == "gzip":
        decompressed = gzip.decompress(body)
        decompressed_name = name.replace(".envelope", ".decompressed.envelope")
        for d in OUT_DIRS:
            out_path = os.path.join(d, decompressed_name)
            with open(out_path, "wb") as f:
                f.write(decompressed)
            print(f"wrote {out_path} ({len(decompressed)} bytes, decompressed)")


class _CaptureState:
    body = None
    content_encoding = None


def _make_handler(state):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length)
            if state.body is None:
                state.body = body
                state.content_encoding = self.headers.get("Content-Encoding")
            self.send_response(200)
            self.end_headers()

        def log_message(self, format, *args):
            pass  # keep captured stdout limited to our own prints

    return Handler


def capture(name, emit):
    state = _CaptureState()
    server = http.server.HTTPServer(("127.0.0.1", 0), _make_handler(state))
    port = server.server_address[1]

    thread = threading.Thread(target=server.serve_forever)
    thread.daemon = True
    thread.start()

    try:
        sentry_sdk.init(
            dsn=f"http://public@127.0.0.1:{port}/1",
            default_integrations=False,
            traces_sample_rate=0,
        )
        emit()
        client = sentry_sdk.get_client()
        client.flush(timeout=2.0)
        client.close()

        # flush() is synchronous for the HTTP transport used here, but give
        # the background worker thread a brief grace window in case the
        # transport hands off asynchronously in a future sentry-sdk version.
        deadline = time.time() + 2.0
        while state.body is None and time.time() < deadline:
            time.sleep(0.05)

        if state.body is None:
            raise RuntimeError(f"no envelope captured for {name}")
        write_fixture(name, state.body, state.content_encoding)
    finally:
        server.shutdown()
        thread.join(timeout=2.0)


def main():
    def emit_exception():
        try:
            try:
                raise ValueError("inner cause")
            except ValueError as inner:
                raise RuntimeError("outer failure") from inner
        except RuntimeError as e:
            sentry_sdk.capture_exception(e)

    capture("sentry-python-exception.envelope", emit_exception)

    capture(
        "sentry-python-message.envelope",
        lambda: sentry_sdk.capture_message("hello from trackdown fixture generator"),
    )

    def emit_unhandled():
        try:
            raise TypeError("simulated unhandled-style error")
        except TypeError as e:
            sentry_sdk.capture_exception(e)

    capture("sentry-python-unhandled.envelope", emit_unhandled)


if __name__ == "__main__":
    main()
