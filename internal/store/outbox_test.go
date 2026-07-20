package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAlertBackoff_SchedulesGrowThenCap(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 1 * time.Minute}, // never called with 0 in practice, but must not panic/index out of range
		{1, 1 * time.Minute},
		{2, 5 * time.Minute},
		{3, 30 * time.Minute},
		{4, 2 * time.Hour},
		{5, 12 * time.Hour},
		{6, 12 * time.Hour}, // past the schedule's length: caps at the last entry
		{8, 12 * time.Hour},
		{100, 12 * time.Hour},
	}
	for _, tt := range tests {
		if got := alertBackoff(tt.attempts); got != tt.want {
			t.Errorf("alertBackoff(%d) = %v, want %v", tt.attempts, got, tt.want)
		}
	}
}

func TestEnqueueAlert_SetsFirstBackoffAndAttempt(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	before := time.Now()
	id, err := s.EnqueueAlert(ctx, OutboxEntry{
		ProjectID:  "proj1",
		IssueID:    1,
		Title:      "boom",
		Level:      "error",
		IsNew:      true,
		EventCount: 1,
		OccurredAt: time.Now(),
	}, errors.New("smtp down"))
	if err != nil {
		t.Fatalf("EnqueueAlert: %v", err)
	}
	if id == 0 {
		t.Fatal("EnqueueAlert returned id 0")
	}

	due, err := s.DueAlerts(ctx, before.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("DueAlerts: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("len(due) = %d, want 1", len(due))
	}
	e := due[0]
	if e.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1", e.Attempts)
	}
	if e.LastError != "smtp down" {
		t.Fatalf("LastError = %q, want %q", e.LastError, "smtp down")
	}
	if e.Status != OutboxPending {
		t.Fatalf("Status = %q, want pending", e.Status)
	}
}

func TestDueAlerts_RespectsNextAttemptAt(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.EnqueueAlert(ctx, OutboxEntry{
		ProjectID: "proj1", IssueID: 1, Title: "boom", OccurredAt: time.Now(),
	}, errors.New("down")); err != nil {
		t.Fatalf("EnqueueAlert: %v", err)
	}

	// First backoff is 1 minute -- not due yet at "now".
	due, err := s.DueAlerts(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("DueAlerts (not yet due): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("len(due) = %d, want 0 (not due yet)", len(due))
	}

	// Once the backoff window has passed, it should show up.
	due, err = s.DueAlerts(ctx, time.Now().Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("DueAlerts (due): %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("len(due) = %d, want 1 (due now)", len(due))
	}
}

func TestMarkAlertDelivered_RemovesFromDueAlerts(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	id, err := s.EnqueueAlert(ctx, OutboxEntry{
		ProjectID: "proj1", IssueID: 1, Title: "boom", OccurredAt: time.Now(),
	}, errors.New("down"))
	if err != nil {
		t.Fatalf("EnqueueAlert: %v", err)
	}
	if err := s.MarkAlertDelivered(ctx, id); err != nil {
		t.Fatalf("MarkAlertDelivered: %v", err)
	}

	due, err := s.DueAlerts(ctx, time.Now().Add(1*time.Hour), 10)
	if err != nil {
		t.Fatalf("DueAlerts: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("len(due) = %d, want 0 (delivered entries are no longer due)", len(due))
	}
}

func TestMarkAlertFailed_RetriesUntilDeadLettered(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	id, err := s.EnqueueAlert(ctx, OutboxEntry{
		ProjectID: "proj1", IssueID: 1, Title: "boom", OccurredAt: time.Now(),
	}, errors.New("attempt 1 failed"))
	if err != nil {
		t.Fatalf("EnqueueAlert: %v", err)
	}

	// Fail it repeatedly, mirroring what cmd/trackdown's retry loop does:
	// attempts 2..7 stay pending (still under alertMaxAttempts), attempt 8
	// (== alertMaxAttempts) dead-letters it.
	for attempt := 2; attempt <= alertMaxAttempts; attempt++ {
		if err := s.MarkAlertFailed(ctx, id, attempt, errors.New("still failing")); err != nil {
			t.Fatalf("MarkAlertFailed (attempt %d): %v", attempt, err)
		}
	}

	due, err := s.DueAlerts(ctx, time.Now().Add(24*time.Hour), 10)
	if err != nil {
		t.Fatalf("DueAlerts: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("len(due) = %d, want 0 (dead-lettered entries must never be retried again)", len(due))
	}
}
