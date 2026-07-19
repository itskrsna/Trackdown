package ingest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/itskrsna/Trackdown/internal/alert"
	"github.com/itskrsna/Trackdown/internal/store"
)

// recordingNotifier captures alert.NotifyEvent calls onto a channel, since
// notifyAsync fires in a background goroutine -- tests must wait for a
// delivery rather than assume it already happened by the time the HTTP
// response returns.
type recordingNotifier struct {
	calls chan alert.NotifyEvent
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{calls: make(chan alert.NotifyEvent, 10)}
}

func (r *recordingNotifier) Notify(_ context.Context, ev alert.NotifyEvent) error {
	r.calls <- ev
	return nil
}

func (r *recordingNotifier) waitForCall(t *testing.T) alert.NotifyEvent {
	t.Helper()
	select {
	case ev := <-r.calls:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a notification")
		return alert.NotifyEvent{}
	}
}

func (r *recordingNotifier) expectNoCall(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case ev := <-r.calls:
		t.Fatalf("unexpected notification: %+v", ev)
	case <-time.After(within):
	}
}

func postEnvelopeTo(t *testing.T, url string, envelope []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(envelope))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServeEnvelope_NewIssue_FiresNotification(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	notifier := newRecordingNotifier()
	mux := http.NewServeMux()
	(&Handler{Store: st, Notifier: notifier}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	postEnvelopeTo(t, srv.URL+"/api/proj1/envelope/", loadEnvelopeFixture(t, "sentry-go-exception.envelope"))

	ev := notifier.waitForCall(t)
	if !ev.IsNew {
		t.Fatalf("expected IsNew=true for a brand new issue, got %+v", ev)
	}
	if ev.IsRegression {
		t.Fatalf("a brand new issue can't be a regression, got %+v", ev)
	}
	if ev.ProjectID != "proj1" {
		t.Fatalf("ProjectID = %q, want proj1", ev.ProjectID)
	}
	if ev.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1", ev.EventCount)
	}
	if ev.Level != "error" {
		t.Fatalf("Level = %q, want error", ev.Level)
	}
}

func TestServeEnvelope_DuplicateEvent_DoesNotFireNotification(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	notifier := newRecordingNotifier()
	mux := http.NewServeMux()
	(&Handler{Store: st, Notifier: notifier}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	envelope := loadEnvelopeFixture(t, "sentry-go-exception.envelope")
	postEnvelopeTo(t, srv.URL+"/api/proj1/envelope/", envelope)
	notifier.waitForCall(t) // first delivery: new issue

	postEnvelopeTo(t, srv.URL+"/api/proj1/envelope/", envelope) // duplicate event_id
	notifier.expectNoCall(t, 300*time.Millisecond)
}

func TestServeEnvelope_SecondDistinctOccurrence_DoesNotRenotify(t *testing.T) {
	// A second occurrence of an already-unresolved issue is neither new nor
	// a regression -- it must not trigger a second notification, only the
	// first occurrence should.
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	notifier := newRecordingNotifier()
	mux := http.NewServeMux()
	(&Handler{Store: st, Notifier: notifier}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	postEnvelopeTo(t, srv.URL+"/api/proj1/envelope/", syntheticEnvelopeWithEventID(t, "occurrence-1"))
	notifier.waitForCall(t)

	postEnvelopeTo(t, srv.URL+"/api/proj1/envelope/", syntheticEnvelopeWithEventID(t, "occurrence-2"))
	notifier.expectNoCall(t, 300*time.Millisecond)
}

func TestServeEnvelope_NoNotifierSet_DoesNotPanic(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	(&Handler{Store: st}).Register(mux, nil) // no Notifier set -- must not panic
	srv := httptest.NewServer(mux)
	defer srv.Close()

	postEnvelopeTo(t, srv.URL+"/api/proj1/envelope/", loadEnvelopeFixture(t, "sentry-go-exception.envelope"))
}
