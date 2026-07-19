package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/itskrsna/Trackdown/internal/protocol"
)

// syntheticEnvelopeWithEventID rebuilds the captured exception fixture's
// event payload with a different event_id, then wraps it in a fresh
// envelope. This simulates a genuinely second occurrence of "the same
// issue" (same exception type and stack trace, different event_id) — as
// opposed to POSTing the identical fixture bytes twice, which exercises the
// dedup-by-event_id path instead (covered separately).
func syntheticEnvelopeWithEventID(t *testing.T, eventID string) []byte {
	t.Helper()
	envelope := loadEnvelopeFixture(t, "sentry-go-exception.envelope")
	env, err := protocol.ParseEnvelope(envelope)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(env.Items[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	payload["event_id"] = eventID
	newPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal modified event payload: %v", err)
	}

	var buf bytes.Buffer
	buf.WriteString(`{}` + "\n")
	buf.WriteString(`{"type":"event","length":` + strconv.Itoa(len(newPayload)) + `}` + "\n")
	buf.Write(newPayload)
	return buf.Bytes()
}

func postEnvelope(t *testing.T, url string, envelope []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(envelope))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestIssuesEndpoints_SameFingerprintTwice_OneIssueCountTwo(t *testing.T) {
	srv, _ := newTestServer(t)
	envelopeURL := srv.URL + "/api/proj1/envelope/"

	resp1 := postEnvelope(t, envelopeURL, syntheticEnvelopeWithEventID(t, "occurrence-1"))
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first post status = %d, want 200", resp1.StatusCode)
	}
	resp2 := postEnvelope(t, envelopeURL, syntheticEnvelopeWithEventID(t, "occurrence-2"))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second post status = %d, want 200", resp2.StatusCode)
	}

	issuesResp, err := http.Get(srv.URL + "/api/proj1/issues/")
	if err != nil {
		t.Fatalf("GET issues: %v", err)
	}
	defer issuesResp.Body.Close()
	var issues []struct {
		ID         int64  `json:"ID"`
		Title      string `json:"Title"`
		Status     string `json:"Status"`
		EventCount int64  `json:"EventCount"`
	}
	if err := json.NewDecoder(issuesResp.Body).Decode(&issues); err != nil {
		t.Fatalf("decoding issues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want 1 (two occurrences of the same bug)", len(issues))
	}
	if issues[0].EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2", issues[0].EventCount)
	}
	if issues[0].Status != "unresolved" {
		t.Fatalf("Status = %q, want unresolved", issues[0].Status)
	}

	eventsResp, err := http.Get(srv.URL + "/api/proj1/issues/" + strconv.FormatInt(issues[0].ID, 10) + "/events")
	if err != nil {
		t.Fatalf("GET issue events: %v", err)
	}
	defer eventsResp.Body.Close()
	var events []struct{ EventID string }
	if err := json.NewDecoder(eventsResp.Body).Decode(&events); err != nil {
		t.Fatalf("decoding issue events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
}

func TestIssuesEndpoints_DifferentErrors_TwoIssues(t *testing.T) {
	srv, _ := newTestServer(t)
	envelopeURL := srv.URL + "/api/proj1/envelope/"

	resp1 := postEnvelope(t, envelopeURL, loadEnvelopeFixture(t, "sentry-go-exception.envelope"))
	resp1.Body.Close()
	resp2 := postEnvelope(t, envelopeURL, loadEnvelopeFixture(t, "sentry-go-message.envelope"))
	resp2.Body.Close()

	issuesResp, err := http.Get(srv.URL + "/api/proj1/issues/")
	if err != nil {
		t.Fatalf("GET issues: %v", err)
	}
	defer issuesResp.Body.Close()
	var issues []struct{ ID int64 }
	if err := json.NewDecoder(issuesResp.Body).Decode(&issues); err != nil {
		t.Fatalf("decoding issues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2 (distinct bugs)", len(issues))
	}
}

func TestIssueLifecycle_ResolveIgnoreReopen_HTTP(t *testing.T) {
	srv, st := newTestServer(t)
	envelope := loadEnvelopeFixture(t, "sentry-go-exception.envelope")
	resp := postEnvelope(t, srv.URL+"/api/proj1/envelope/", envelope)
	resp.Body.Close()

	events, err := st.ListEvents(t.Context(), "proj1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	issueID := strconv.FormatInt(events[0].IssueID, 10)
	base := srv.URL + "/api/proj1/issues/" + issueID

	for _, action := range []string{"resolve", "ignore", "reopen"} {
		resp, err := http.Post(base+"/"+action, "", nil)
		if err != nil {
			t.Fatalf("POST %s: %v", action, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST %s status = %d, want 204", action, resp.StatusCode)
		}
	}

	getResp, err := http.Get(base)
	if err != nil {
		t.Fatalf("GET issue: %v", err)
	}
	defer getResp.Body.Close()
	var issue struct{ Status string }
	if err := json.NewDecoder(getResp.Body).Decode(&issue); err != nil {
		t.Fatalf("decoding issue: %v", err)
	}
	if issue.Status != "unresolved" {
		t.Fatalf("final Status = %q, want unresolved (last action was reopen)", issue.Status)
	}
}

func TestSetIssueStatus_UnknownIssue_Returns404(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/api/proj1/issues/99999/resolve", "", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetIssue_NonNumericID_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/proj1/issues/not-a-number")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
