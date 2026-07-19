package alert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookNotifier_SendsExpectedPayload(t *testing.T) {
	var receivedBody []byte
	var receivedContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL}
	ev := NotifyEvent{
		ProjectID:    "proj1",
		IssueID:      42,
		Title:        "NullPointerException: boom",
		Level:        "error",
		IsNew:        true,
		EventCount:   1,
		OccurredAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if receivedContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", receivedContentType)
	}

	var payload webhookPayload
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("unmarshaling received payload: %v", err)
	}
	if payload.ProjectID != "proj1" || payload.IssueID != 42 || payload.Title != ev.Title {
		t.Fatalf("payload = %+v", payload)
	}
	if !payload.IsNew {
		t.Fatal("expected IsNew=true in payload")
	}
}

func TestWebhookNotifier_SignsBodyWhenSecretSet(t *testing.T) {
	const secret = "shh-its-a-secret"
	var receivedSig string
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Trackdown-Signature")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL, Secret: secret}
	if err := n.Notify(context.Background(), NotifyEvent{Title: "boom"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if receivedSig != want {
		t.Fatalf("signature = %q, want %q", receivedSig, want)
	}
}

func TestWebhookNotifier_NoSecret_NoSignatureHeader(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["X-Trackdown-Signature"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL}
	if err := n.Notify(context.Background(), NotifyEvent{}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if sawHeader {
		t.Fatal("expected no X-Trackdown-Signature header when Secret is unset")
	}
}

func TestWebhookNotifier_NonSuccessStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL}
	err := n.Notify(context.Background(), NotifyEvent{})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected the error to mention the status code, got: %v", err)
	}
}
