package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WebhookNotifier POSTs a JSON payload to URL for each notification.
type WebhookNotifier struct {
	URL    string
	Secret string // if set, signs the body -- see the header this adds below
	Client *http.Client
}

type webhookPayload struct {
	ProjectID    string    `json:"project_id"`
	IssueID      int64     `json:"issue_id"`
	Title        string    `json:"title"`
	Level        string    `json:"level"`
	IsNew        bool      `json:"is_new"`
	IsRegression bool      `json:"is_regression"`
	EventCount   int64     `json:"event_count"`
	OccurredAt   time.Time `json:"occurred_at"`
}

func (w *WebhookNotifier) Notify(ctx context.Context, ev NotifyEvent) error {
	body, err := json.Marshal(webhookPayload{
		ProjectID:    ev.ProjectID,
		IssueID:      ev.IssueID,
		Title:        ev.Title,
		Level:        ev.Level,
		IsNew:        ev.IsNew,
		IsRegression: ev.IsRegression,
		EventCount:   ev.EventCount,
		OccurredAt:   ev.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("marshaling webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Secret != "" {
		// HMAC-SHA256 over the raw body, hex-encoded, "sha256=" prefixed --
		// mirrors the GitHub/Stripe webhook signature convention so
		// receivers can verify the request actually came from this
		// Trackdown instance and wasn't forged or tampered with in transit.
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write(body)
		req.Header.Set("X-Trackdown-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
