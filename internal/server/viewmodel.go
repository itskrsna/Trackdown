package server

import (
	"encoding/json"
	"time"

	"github.com/itskrsna/Trackdown/internal/protocol"
	"github.com/itskrsna/Trackdown/internal/store"
)

type homePage struct {
	Projects []*store.Project
}

type setupPage struct {
	ProjectID string
	DSN       string
}

type issueRow struct {
	ID         int64
	Title      string
	Status     store.IssueStatus
	Level      string
	EventCount int64
	FirstSeen  time.Time
	LastSeen   time.Time
}

type issueListPage struct {
	ProjectID string
	Status    string // current filter: "unresolved" (default), "resolved", "ignored", "all"
	// Counts is keyed by plain string, not store.IssueStatus -- the
	// template's {{index .Counts "unresolved"}} requires an exact map key
	// type match (unlike {{eq}}/{{ne}}, which handle named string types
	// transparently), so a string key sidesteps that mismatch entirely.
	Counts map[string]int
	Issues []issueRow
}

type issueDetailPage struct {
	ProjectID   string
	ID          int64
	Title       string
	Status      store.IssueStatus
	Fingerprint string
	EventCount  int64
	FirstSeen   time.Time
	LastSeen    time.Time
	Events      []*store.StoredEvent
}

type eventDetailPage struct {
	ProjectID string
	Stored    *store.StoredEvent
	Event     *protocol.Event
	RawJSON   string
}

func toIssueRow(i *store.Issue) issueRow {
	return issueRow{
		ID:         i.ID,
		Title:      i.Title,
		Status:     i.Status,
		Level:      i.Level,
		EventCount: i.EventCount,
		FirstSeen:  i.FirstSeen,
		LastSeen:   i.LastSeen,
	}
}

// indentJSON pretty-prints raw event JSON for the debug view. If the bytes
// somehow aren't valid JSON, it falls back to the raw string rather than
// failing the whole page render — this is a fallback view, not something
// that should itself become a new failure mode.
func indentJSON(raw []byte) string {
	var buf []byte
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(buf)
}
