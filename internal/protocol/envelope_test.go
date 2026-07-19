package protocol

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// readFixture loads a captured real-SDK envelope from testdata/envelopes/.
// These files are the conformance oracle for this package: they are exactly
// what an official Sentry SDK sent over the wire (see tools/genfixtures),
// not a hand re-derivation of the spec.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "envelopes", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

func TestParseEnvelope_RealFixtures(t *testing.T) {
	tests := []struct {
		fixture   string
		wantLevel string
	}{
		{"sentry-go-exception.envelope", "error"},
		{"sentry-go-message.envelope", "info"},
		{"sentry-go-panic.envelope", "fatal"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			data := readFixture(t, tt.fixture)

			env, err := ParseEnvelope(data)
			if err != nil {
				t.Fatalf("ParseEnvelope: %v", err)
			}

			if env.Header.SDK == nil || env.Header.SDK.Name != "sentry.go" {
				t.Fatalf("envelope header SDK = %+v, want name sentry.go", env.Header.SDK)
			}
			if env.Header.EventID == "" {
				t.Fatal("envelope header EventID is empty")
			}

			if len(env.Items) != 1 {
				t.Fatalf("len(Items) = %d, want 1", len(env.Items))
			}
			item := env.Items[0]
			if item.Header.Type != "event" {
				t.Fatalf("item type = %q, want event", item.Header.Type)
			}
			if item.Header.Length == nil {
				t.Fatal("item header has no explicit length, expected one from this SDK")
			}
			if int64(len(item.Payload)) != *item.Header.Length {
				t.Fatalf("payload length = %d, header declared %d", len(item.Payload), *item.Header.Length)
			}

			ev, err := ParseEvent(item.Payload)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			if ev.EventID != env.Header.EventID {
				t.Fatalf("event_id mismatch: envelope=%s event=%s", env.Header.EventID, ev.EventID)
			}
			if ev.Level != tt.wantLevel {
				t.Fatalf("Level = %q, want %q", ev.Level, tt.wantLevel)
			}
			if ev.Platform != "go" {
				t.Fatalf("Platform = %q, want go", ev.Platform)
			}
			if ev.Timestamp.IsZero() {
				t.Fatal("Timestamp is zero, expected a parsed time")
			}
		})
	}
}

// TestParseEnvelope_RealNodeFixtures exercises real @sentry/node output,
// which differs from sentry-go's in exactly the ways worth having a
// separate test for: no explicit item "length" at all (implicit-length
// items), and "exception" wrapped as {"values": [...]} rather than a bare
// array. Both were already handled defensively by the parser before this
// fixture existed — this test is what actually proves that, rather than
// just asserting it in a comment.
func TestParseEnvelope_RealNodeFixtures(t *testing.T) {
	tests := []struct {
		fixture   string
		wantLevel string
	}{
		{"sentry-node-exception.envelope", "error"},
		{"sentry-node-message.envelope", "info"},
		{"sentry-node-unhandled.envelope", "error"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			data := readFixture(t, tt.fixture)

			env, err := ParseEnvelope(data)
			if err != nil {
				t.Fatalf("ParseEnvelope: %v", err)
			}
			if env.Header.SDK == nil || env.Header.SDK.Name != "sentry.javascript.node" {
				t.Fatalf("envelope header SDK = %+v, want name sentry.javascript.node", env.Header.SDK)
			}
			if len(env.Items) != 1 {
				t.Fatalf("len(Items) = %d, want 1", len(env.Items))
			}
			item := env.Items[0]
			if item.Header.Length != nil {
				t.Fatalf("item header has explicit length %d, want nil (this SDK uses implicit-length items)", *item.Header.Length)
			}

			ev, err := ParseEvent(item.Payload)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			if ev.EventID != env.Header.EventID {
				t.Fatalf("event_id mismatch: envelope=%s event=%s", env.Header.EventID, ev.EventID)
			}
			if ev.Level != tt.wantLevel {
				t.Fatalf("Level = %q, want %q", ev.Level, tt.wantLevel)
			}
			if ev.Platform != "node" {
				t.Fatalf("Platform = %q, want node", ev.Platform)
			}
			if ev.Timestamp.IsZero() {
				t.Fatal("Timestamp is zero, expected a parsed time")
			}
		})
	}
}

func TestParseEvent_RealNodeException_WrappedValuesForm(t *testing.T) {
	data := readFixture(t, "sentry-node-exception.envelope")
	env, err := ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	ev, err := ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if len(ev.Exception) != 1 {
		t.Fatalf("len(Exception) = %d, want 1 (confirms the {\"values\":[...]} wrapped form parsed)", len(ev.Exception))
	}
	if ev.Exception[0].Type != "Error" || ev.Exception[0].Value != "outer failure" {
		t.Fatalf("Exception[0] = %+v", ev.Exception[0])
	}
	if ev.Exception[0].Stacktrace == nil || len(ev.Exception[0].Stacktrace.Frames) == 0 {
		t.Fatal("expected a non-empty stacktrace")
	}
	sawInApp, sawLib := false, false
	for _, f := range ev.Exception[0].Stacktrace.Frames {
		if f.InApp {
			sawInApp = true
		} else {
			sawLib = true
		}
	}
	if !sawInApp || !sawLib {
		t.Fatalf("expected both in-app and library frames in the real fixture, sawInApp=%v sawLib=%v", sawInApp, sawLib)
	}
}

// TestParseEnvelope_RealPythonFixtures exercises real sentry-sdk (Python)
// output. Note: sentry-sdk gzip-compresses envelope bodies by default (no
// size threshold, confirmed by reading its transport.py) — gzip decoding is
// an HTTP/Content-Encoding transport concern handled in internal/ingest, not
// something this package's ParseEnvelope should know about, so these tests
// use the ".decompressed.envelope" siblings tools/genfixtures-python writes
// alongside the real compressed wire bytes. The compressed originals are
// exercised by internal/ingest's tests, which POST them with a real
// Content-Encoding: gzip header.
func TestParseEnvelope_RealPythonFixtures(t *testing.T) {
	tests := []struct {
		fixture   string
		wantLevel string
	}{
		{"sentry-python-exception.decompressed.envelope", "error"},
		{"sentry-python-message.decompressed.envelope", "info"},
		{"sentry-python-unhandled.decompressed.envelope", "error"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			data := readFixture(t, tt.fixture)

			env, err := ParseEnvelope(data)
			if err != nil {
				t.Fatalf("ParseEnvelope: %v", err)
			}
			// Unlike sentry-go and @sentry/node, sentry-sdk (Python) does NOT
			// put "sdk" in the envelope header — only in the event payload
			// itself (confirmed against the real captured fixture). Assert
			// on the event-level field instead of assuming header parity
			// across SDKs.
			if len(env.Items) != 1 {
				t.Fatalf("len(Items) = %d, want 1", len(env.Items))
			}
			item := env.Items[0]
			if item.Header.Type != "event" {
				t.Fatalf("item type = %q, want event", item.Header.Type)
			}
			if item.Header.Length == nil {
				t.Fatal("item header has no explicit length, expected one from this SDK")
			}

			ev, err := ParseEvent(item.Payload)
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			if ev.EventID != env.Header.EventID {
				t.Fatalf("event_id mismatch: envelope=%s event=%s", env.Header.EventID, ev.EventID)
			}
			if ev.SDK == nil || ev.SDK.Name != "sentry.python" {
				t.Fatalf("event SDK = %+v, want name sentry.python", ev.SDK)
			}
			if ev.Level != tt.wantLevel {
				t.Fatalf("Level = %q, want %q", ev.Level, tt.wantLevel)
			}
			if ev.Platform != "python" {
				t.Fatalf("Platform = %q, want python", ev.Platform)
			}
			// sentry-sdk sends an ISO8601 string timestamp (unlike sentry-go
			// and @sentry/node's numeric unix timestamps) — a third distinct
			// form Timestamp.UnmarshalJSON must handle, now proven by a real
			// fixture rather than only the synthetic case in event_test.go.
			if ev.Timestamp.IsZero() {
				t.Fatal("Timestamp is zero, expected a parsed time")
			}
		})
	}
}

func TestParseEvent_RealPythonException_ChainedCause(t *testing.T) {
	data := readFixture(t, "sentry-python-exception.decompressed.envelope")
	env, err := ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	ev, err := ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if len(ev.Exception) != 2 {
		t.Fatalf("len(Exception) = %d, want 2 (chained: inner cause + outer wrap)", len(ev.Exception))
	}
	inner, outer := ev.Exception[0], ev.Exception[1]
	if inner.Type != "ValueError" || inner.Value != "inner cause" {
		t.Fatalf("Exception[0] = %+v, want the inner ValueError", inner)
	}
	if outer.Type != "RuntimeError" || outer.Value != "outer failure" {
		t.Fatalf("Exception[1] = %+v, want the outer RuntimeError", outer)
	}
}

func TestParseEnvelope_ImplicitLength(t *testing.T) {
	raw := []byte(`{"event_id":"abc"}` + "\n" +
		`{"type":"event"}` + "\n" +
		`{"message":"no length header"}` + "\n")

	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if len(env.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(env.Items))
	}
	item := env.Items[0]
	if item.Header.Length != nil {
		t.Fatalf("Length = %v, want nil (implicit-length item)", *item.Header.Length)
	}
	if string(item.Payload) != `{"message":"no length header"}` {
		t.Fatalf("Payload = %q", item.Payload)
	}
}

func TestParseEnvelope_MultipleItems_MixedLength(t *testing.T) {
	explicitPayload := `{"message":"has length"}`
	raw := []byte(`{"event_id":"abc"}` + "\n" +
		`{"type":"event","length":` + strconv.Itoa(len(explicitPayload)) + `}` + "\n" +
		explicitPayload + "\n" +
		`{"type":"attachment"}` + "\n" +
		`{"message":"no length"}` + "\n")

	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if len(env.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(env.Items))
	}
	if string(env.Items[0].Payload) != explicitPayload {
		t.Fatalf("Items[0].Payload = %q, want %q", env.Items[0].Payload, explicitPayload)
	}
	if env.Items[1].Header.Type != "attachment" {
		t.Fatalf("Items[1].Header.Type = %q", env.Items[1].Header.Type)
	}
	if string(env.Items[1].Payload) != `{"message":"no length"}` {
		t.Fatalf("Items[1].Payload = %q", env.Items[1].Payload)
	}
}

func TestParseEnvelope_NoItems(t *testing.T) {
	raw := []byte(`{"event_id":"abc"}` + "\n")

	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if len(env.Items) != 0 {
		t.Fatalf("len(Items) = %d, want 0", len(env.Items))
	}
	if env.Header.EventID != "abc" {
		t.Fatalf("Header.EventID = %q, want abc", env.Header.EventID)
	}
}

func TestParseEnvelope_EmptyHeader(t *testing.T) {
	raw := []byte("{}\n" +
		`{"type":"event"}` + "\n" +
		`{}`)

	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Header.EventID != "" || env.Header.DSN != "" {
		t.Fatalf("expected empty header, got %+v", env.Header)
	}
	if len(env.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(env.Items))
	}
}

func TestParseEnvelope_NoTrailingNewlineAfterExplicitLengthPayload(t *testing.T) {
	payload := `{"a":1}`
	raw := []byte(`{}` + "\n" +
		`{"type":"event","length":` + strconv.Itoa(len(payload)) + `}` + "\n" +
		payload) // no trailing newline at all

	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if len(env.Items) != 1 || string(env.Items[0].Payload) != payload {
		t.Fatalf("Items = %+v", env.Items)
	}
}
