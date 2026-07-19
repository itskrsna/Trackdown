"""Sends a real sentry-sdk (Python) event to an already-running Trackdown
server, for manual end-to-end verification against the actual compiled
binary. Mirrors tools/e2echeck (Go). Usage: python check.py <host:port>
"""
import sys

import sentry_sdk


def main():
    if len(sys.argv) < 2:
        print("usage: python check.py <host:port>", file=sys.stderr)
        sys.exit(2)
    host = sys.argv[1]

    # sentry-sdk's own DSN parser requires the project-id path segment to be
    # numeric (str(int(...))) -- unlike sentry-go and @sentry/node, which
    # accept an arbitrary string there. Confirmed directly against a real
    # BadDsn error from this SDK. Trackdown itself treats project IDs as
    # opaque strings, but Python SDK users are constrained to numeric ones.
    sentry_sdk.init(
        dsn=f"http://public@{host}/424242",
        default_integrations=False,
        traces_sample_rate=0,
    )

    try:
        raise RuntimeError("manual e2e check from sentry-sdk (python)")
    except RuntimeError as e:
        event_id = sentry_sdk.capture_exception(e)

    sentry_sdk.get_client().flush(timeout=2.0)
    print(f"sent event_id={event_id} to project 424242 on {host}")


if __name__ == "__main__":
    main()
