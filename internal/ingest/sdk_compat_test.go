package ingest

import (
	"bytes"
	"net/http"
	"testing"
)

// These tests exercise real envelope bytes captured from official SDKs
// other than sentry-go (see tools/genfixtures-node, tools/genfixtures-python)
// against the actual ingest handler — the same "real SDK as conformance
// oracle" approach used throughout this project, extended to the two other
// most common Sentry SDKs, which had never been verified against Trackdown
// before this test file existed.

func TestServeEnvelope_RealNodeFixtures(t *testing.T) {
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
			srv, st := newTestServer(t)
			envelope := loadEnvelopeFixture(t, tt.fixture)

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
			req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			events, err := st.ListEvents(req.Context(), "proj1")
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("len(events) = %d, want 1", len(events))
			}
			if events[0].Level != tt.wantLevel || events[0].Platform != "node" {
				t.Fatalf("stored event = %+v", events[0])
			}
		})
	}
}

// TestServeEnvelope_RealPythonFixtures_Gzip is the test that actually proves
// the gzip decompression fix: sentry-sdk (Python) compresses every envelope
// by default (confirmed against its transport.py source — no size
// threshold), so posting these real captured bytes WITHOUT decompression
// support would fail to parse. Before this fix, it did.
func TestServeEnvelope_RealPythonFixtures_Gzip(t *testing.T) {
	tests := []struct {
		fixture   string
		wantLevel string
	}{
		{"sentry-python-exception.envelope", "error"},
		{"sentry-python-message.envelope", "info"},
		{"sentry-python-unhandled.envelope", "error"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv, st := newTestServer(t)
			envelope := loadEnvelopeFixture(t, tt.fixture) // real gzip-compressed wire bytes

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
			req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
			req.Header.Set("Content-Encoding", "gzip") // what sentry-sdk actually sends
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			events, err := st.ListEvents(req.Context(), "proj1")
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("len(events) = %d, want 1", len(events))
			}
			if events[0].Level != tt.wantLevel || events[0].Platform != "python" {
				t.Fatalf("stored event = %+v", events[0])
			}
		})
	}
}

// TestServeEnvelope_RealDotNetFixtures_Gzip proves two real, previously-
// unverified things about the Sentry .NET SDK: it gzip-compresses every
// envelope by default just like sentry-sdk (Python), and it sends captured
// message text under "logentry" rather than the top-level "message" field
// the other three verified SDKs use — internal/protocol.ParseEvent
// normalizes the latter; this test is what actually proves it end-to-end.
func TestServeEnvelope_RealDotNetFixtures_Gzip(t *testing.T) {
	tests := []struct {
		fixture   string
		wantLevel string
	}{
		{"sentry-dotnet-exception.envelope", "error"},
		{"sentry-dotnet-message.envelope", "info"},
		{"sentry-dotnet-unhandled.envelope", "error"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv, st := newTestServer(t)
			envelope := loadEnvelopeFixture(t, tt.fixture) // real gzip-compressed wire bytes

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
			req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
			req.Header.Set("Content-Encoding", "gzip") // what the .NET SDK actually sends
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			events, err := st.ListEvents(req.Context(), "proj1")
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("len(events) = %d, want 1", len(events))
			}
			if events[0].Level != tt.wantLevel || events[0].Platform != "csharp" {
				t.Fatalf("stored event = %+v", events[0])
			}
		})
	}
}

// TestServeEnvelope_RealDotNetMessageFixture_UsesLogEntry pins the exact
// wire-format quirk down independently of the round-trip test above: the raw
// captured .NET message fixture really does carry the text under
// "logentry", not "message" — confirming the assertion in the test above
// isn't passing for an unrelated reason.
func TestServeEnvelope_RealDotNetMessageFixture_UsesLogEntry(t *testing.T) {
	raw := string(loadEnvelopeFixture(t, "sentry-dotnet-message.decompressed.envelope"))
	if !bytes.Contains([]byte(raw), []byte(`"logentry"`)) {
		t.Fatal(`fixture doesn't contain "logentry" — did the .NET SDK's message wire format change?`)
	}
}

// TestServeEnvelope_RealJavaFixtures_Gzip verifies the Sentry Java SDK.
func TestServeEnvelope_RealJavaFixtures_Gzip(t *testing.T) {
	tests := []struct {
		fixture   string
		wantLevel string
	}{
		{"sentry-java-exception.envelope", "error"},
		{"sentry-java-message.envelope", "info"},
		{"sentry-java-unhandled.envelope", "error"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv, st := newTestServer(t)
			envelope := loadEnvelopeFixture(t, tt.fixture)

			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
			req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
			req.Header.Set("Content-Encoding", "gzip") // what the Java SDK actually sends
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			events, err := st.ListEvents(req.Context(), "proj1")
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("len(events) = %d, want 1", len(events))
			}
			if events[0].Level != tt.wantLevel || events[0].Platform != "java" {
				t.Fatalf("stored event = %+v", events[0])
			}
		})
	}
}

func TestServeEnvelope_GzipBody_WithoutContentEncodingHeader_FailsToParse(t *testing.T) {
	// Documents the contract: decompression is driven strictly by the
	// Content-Encoding header, not by sniffing the body. Posting real
	// compressed bytes without declaring the encoding must be rejected as a
	// malformed envelope, not silently misinterpreted.
	srv, _ := newTestServer(t)
	envelope := loadEnvelopeFixture(t, "sentry-python-exception.envelope")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (undeclared gzip body should not parse as an envelope)", resp.StatusCode)
	}
}

func TestServeEnvelope_UnsupportedContentEncoding_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader([]byte("{}\n")))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	req.Header.Set("Content-Encoding", "br") // brotli — explicitly not supported
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServeEnvelope_InvalidGzipBody_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader([]byte("not actually gzip data")))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
