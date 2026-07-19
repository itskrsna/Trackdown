package ingest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/itskrsna/Trackdown/internal/store"
)

// loadEnvelopeFixture reads a real captured SDK envelope from this
// package's own testdata/envelopes/ (see tools/genfixtures).
func loadEnvelopeFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "envelopes", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

// newTestServer builds a Handler backed by a fresh in-memory store and
// returns an httptest.Server serving its routes.
func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mux := http.NewServeMux()
	(&Handler{Store: st}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

// sentryAuthHeader builds an X-Sentry-Auth header value in the exact shape
// sentry-go's HTTP transport sends (verified against its source):
// "Sentry sentry_version=7, sentry_client=<name>/<version>, sentry_key=<key>".
func sentryAuthHeader(publicKey string) string {
	return "Sentry sentry_version=7, sentry_client=test-client/1.0, sentry_key=" + publicKey
}

func TestServerError_LogsRealErrorNotJustGeneric500(t *testing.T) {
	// Before Logger existed, a Store failure produced a bare 500 with
	// nothing printed anywhere server-side -- undebuggable in real
	// production. This proves the fix: the actual error reaches the log.
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.Close() // force every subsequent Store call to fail

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	mux := http.NewServeMux()
	(&Handler{Store: st, Logger: logger}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/proj1/events/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "listing events") {
		t.Fatalf("log output = %q, want it to mention the failing operation", logged)
	}
	if !strings.Contains(logged, "level=ERROR") {
		t.Fatalf("log output = %q, want an ERROR-level record", logged)
	}
}

func TestServeEnvelope_RealFixture_StoresEvent(t *testing.T) {
	srv, st := newTestServer(t)
	envelope := loadEnvelopeFixture(t, "sentry-go-exception.envelope")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	req.Header.Set("Content-Type", "application/x-sentry-envelope")

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
	if events[0].Level != "error" || events[0].Platform != "go" {
		t.Fatalf("stored event = %+v", events[0])
	}

	key, err := st.ProjectPublicKey(req.Context(), "proj1")
	if err != nil {
		t.Fatalf("ProjectPublicKey: %v", err)
	}
	if key != "public" {
		t.Fatalf("public key = %q, want public (project auto-created from the DSN key used)", key)
	}
}

func TestServeEnvelope_MissingAuth_Returns401(t *testing.T) {
	srv, _ := newTestServer(t)
	envelope := loadEnvelopeFixture(t, "sentry-go-exception.envelope")

	resp, err := http.Post(srv.URL+"/api/proj1/envelope/", "application/x-sentry-envelope", bytes.NewReader(envelope))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestServeEnvelope_AuthViaQueryParam(t *testing.T) {
	srv, st := newTestServer(t)
	envelope := loadEnvelopeFixture(t, "sentry-go-message.envelope")

	resp, err := http.Post(srv.URL+"/api/proj1/envelope/?sentry_key=public", "application/x-sentry-envelope", bytes.NewReader(envelope))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (query-param auth fallback)", resp.StatusCode)
	}

	events, err := st.ListEvents(resp.Request.Context(), "proj1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
}

func TestServeEnvelope_MalformedBody_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader([]byte("not json at all\x00\x01")))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServeEnvelope_SkipsNonEventItems(t *testing.T) {
	srv, st := newTestServer(t)

	// Build a synthetic envelope: an "attachment" item Trackdown doesn't yet
	// persist, followed by a real captured "event" item. Only the event
	// should land in storage — one unsupported item type must not sink the
	// whole envelope.
	eventPayload := `{"event_id":"abc123","level":"warning","platform":"python","message":"from a mixed envelope"}`
	var buf bytes.Buffer
	buf.WriteString(`{"event_id":"abc123"}` + "\n")
	buf.WriteString(`{"type":"attachment","length":5,"filename":"note.txt"}` + "\n")
	buf.WriteString("hello\n")
	buf.WriteString(`{"type":"event","length":` + strconv.Itoa(len(eventPayload)) + `}` + "\n")
	buf.WriteString(eventPayload)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", &buf)
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
		t.Fatalf("len(events) = %d, want 1 (only the event item should be stored)", len(events))
	}
	if events[0].EventID != "abc123" || events[0].Level != "warning" {
		t.Fatalf("stored event = %+v", events[0])
	}
}

func TestListEvents_And_GetEvent_HTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	envelope := loadEnvelopeFixture(t, "sentry-go-panic.envelope")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("posting envelope: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	listResp, err := http.Get(srv.URL + "/api/proj1/events/")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer listResp.Body.Close()
	var listed []struct {
		EventID string `json:"EventID"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decoding event list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(listed) = %d, want 1", len(listed))
	}
	eventID := listed[0].EventID
	if eventID == "" {
		t.Fatal("listed event has empty EventID")
	}

	getResp, err := http.Get(srv.URL + "/api/proj1/events/" + eventID)
	if err != nil {
		t.Fatalf("GET single event: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET single event status = %d, want 200", getResp.StatusCode)
	}

	notFoundResp, err := http.Get(srv.URL + "/api/proj1/events/does-not-exist")
	if err != nil {
		t.Fatalf("GET missing event: %v", err)
	}
	defer notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing event status = %d, want 404", notFoundResp.StatusCode)
	}
}
