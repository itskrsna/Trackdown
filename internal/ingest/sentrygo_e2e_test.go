package ingest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// TestSentryGoClient_EndToEnd is the strongest form of conformance check:
// rather than hand-building an HTTP request that looks like what sentry-go
// sends (as the other tests in this package do), it drives the actual
// sentry-go client object — Init, CaptureException, Flush — against a real
// running Trackdown handler, over a real loopback TCP socket
// (httptest.NewServer listens on one). If sentry-go's SDK internals ever
// change how it builds requests, this test catches it; the others wouldn't.
func TestSentryGoClient_EndToEnd(t *testing.T) {
	srv, st := newTestServer(t)

	// sentry-go's DSN parser expects the scheme/host/key form and derives the
	// /api/{project_id}/envelope/ path itself — build a proper DSN, not a
	// pre-built ingest URL: http://<key>@<host>/<project_id>.
	dsn := fmt.Sprintf("http://public@%s/e2eproj", stripScheme(srv.URL))

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Transport:        sentry.NewHTTPSyncTransport(),
		AttachStacktrace: true,
	}); err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	defer sentry.Flush(2 * time.Second)

	err := fmt.Errorf("e2e outer: %w", errors.New("e2e inner"))
	eventID := sentry.CaptureException(err)
	if eventID == nil {
		t.Fatal("CaptureException returned a nil event ID")
	}
	if !sentry.Flush(2 * time.Second) {
		t.Fatal("sentry.Flush timed out — event was not delivered")
	}

	events, listErr := st.ListEvents(t.Context(), "e2eproj")
	if listErr != nil {
		t.Fatalf("ListEvents: %v", listErr)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (the real sentry-go client's delivery)", len(events))
	}
	if events[0].EventID != string(*eventID) {
		t.Fatalf("stored EventID = %q, want %q (the ID sentry-go generated)", events[0].EventID, string(*eventID))
	}
	if events[0].Level != "error" {
		t.Fatalf("stored Level = %q, want error", events[0].Level)
	}
}

// stripScheme removes "http://" from a URL so it can be recombined into a
// DSN's authority component.
func stripScheme(url string) string {
	const prefix = "http://"
	if len(url) >= len(prefix) && url[:len(prefix)] == prefix {
		return url[len(prefix):]
	}
	return url
}
