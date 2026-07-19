package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/itskrsna/Trackdown/internal/protocol"
)

// loadEnvelopeFixture reads a real captured SDK envelope from this package's
// own testdata/envelopes/ (see tools/genfixtures — the same fixtures are
// also kept under internal/protocol/testdata for that package's tests).
func loadEnvelopeFixture(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", "envelopes", name))
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	// A unique in-memory database per test, so tests don't share state —
	// modernc.org/sqlite's ":memory:" is per-connection, and Store pins a
	// single connection (MaxOpenConns(1)), so this is safe.
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEnsureProject(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.EnsureProject(ctx, "proj1", "pubkey1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	key, err := s.ProjectPublicKey(ctx, "proj1")
	if err != nil {
		t.Fatalf("ProjectPublicKey: %v", err)
	}
	if key != "pubkey1" {
		t.Fatalf("public key = %q, want pubkey1", key)
	}

	// Calling it again with a different key must not overwrite the original —
	// EnsureProject is a "create if absent" operation, not an upsert.
	if err := s.EnsureProject(ctx, "proj1", "different-key"); err != nil {
		t.Fatalf("EnsureProject (second call): %v", err)
	}
	key, err = s.ProjectPublicKey(ctx, "proj1")
	if err != nil {
		t.Fatalf("ProjectPublicKey (after second EnsureProject): %v", err)
	}
	if key != "pubkey1" {
		t.Fatalf("public key after second EnsureProject = %q, want unchanged pubkey1", key)
	}
}

func TestGetProject_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	_, err := s.GetProject(ctx, "does-not-exist")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestGetProject_Found(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.EnsureProject(ctx, "proj1", "pubkey1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	p, err := s.GetProject(ctx, "proj1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.ID != "proj1" || p.PublicKey != "pubkey1" {
		t.Fatalf("project = %+v", p)
	}
	if p.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero")
	}
}

func TestListProjects_MultipleAndEmpty(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	empty, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects (empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("len(empty) = %d, want 0", len(empty))
	}

	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject proj1: %v", err)
	}
	if err := s.EnsureProject(ctx, "proj2", "key2"); err != nil {
		t.Fatalf("EnsureProject proj2: %v", err)
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("len(projects) = %d, want 2", len(projects))
	}
}

func TestProjectPublicKey_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	_, err := s.ProjectPublicKey(ctx, "does-not-exist")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

// loadFixtureEvent parses one of the real captured envelope fixtures from
// the protocol package into an Event, giving store tests realistic data
// instead of hand-built structs.
func loadFixtureEvent(t *testing.T, filename string) (*protocol.Event, []byte) {
	t.Helper()
	data, err := loadEnvelopeFixture(filename)
	if err != nil {
		t.Fatalf("loading fixture %s: %v", filename, err)
	}
	env, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if len(env.Items) != 1 {
		t.Fatalf("fixture %s: len(Items) = %d, want 1", filename, len(env.Items))
	}
	ev, err := protocol.ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	return ev, env.Items[0].Payload
}

func TestSaveAndGetEvent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")

	if err := s.EnsureProject(ctx, "proj1", "pubkey1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp-exception", "outer failure"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	got, err := s.GetEvent(ctx, "proj1", ev.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.EventID != ev.EventID {
		t.Fatalf("EventID = %q, want %q", got.EventID, ev.EventID)
	}
	if got.Level != "error" {
		t.Fatalf("Level = %q, want error", got.Level)
	}
	if got.Platform != "go" {
		t.Fatalf("Platform = %q, want go", got.Platform)
	}
	if got.Timestamp.IsZero() {
		t.Fatal("Timestamp is zero, want the event's parsed timestamp")
	}
	if len(got.Payload) != len(raw) {
		t.Fatalf("stored payload length = %d, want %d (original bytes preserved)", len(got.Payload), len(raw))
	}
	if got.IssueID == 0 {
		t.Fatal("IssueID is 0, want the event linked to its created issue")
	}

	issue, err := s.GetIssue(ctx, "proj1", got.IssueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Fingerprint != "fp-exception" || issue.Title != "outer failure" {
		t.Fatalf("issue = %+v", issue)
	}
	if issue.EventCount != 1 {
		t.Fatalf("issue.EventCount = %d, want 1", issue.EventCount)
	}
	if issue.Status != StatusUnresolved {
		t.Fatalf("issue.Status = %q, want unresolved (default for a new issue)", issue.Status)
	}
}

func TestGetEvent_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.EnsureProject(ctx, "proj1", "pubkey1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	_, err := s.GetEvent(ctx, "proj1", "nonexistent-event-id")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestSaveEvent_DuplicateIsNoOp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	ev, raw := loadFixtureEvent(t, "sentry-go-message.envelope")
	if err := s.EnsureProject(ctx, "proj1", "pubkey1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	// SDKs may retry a delivery; saving the same event twice must not error
	// or create a duplicate row — and must not double-count the issue.
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp-message", "hello"); err != nil {
		t.Fatalf("SaveEvent (first): %v", err)
	}
	_, isNew, isRegression, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp-message", "hello")
	if err != nil {
		t.Fatalf("SaveEvent (duplicate): %v", err)
	}
	if isNew || isRegression {
		t.Fatalf("duplicate save: isNew=%v isRegression=%v, want both false (a no-op is not a notify-worthy event)", isNew, isRegression)
	}

	events, err := s.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (duplicate save must not create a second row)", len(events))
	}

	issue, err := s.GetIssue(ctx, "proj1", events[0].IssueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.EventCount != 1 {
		t.Fatalf("issue.EventCount = %d, want 1 (the duplicate save must not increment it)", issue.EventCount)
	}
}

func TestListEvents_MultipleAndIsolatedByProject(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject proj1: %v", err)
	}
	if err := s.EnsureProject(ctx, "proj2", "key2"); err != nil {
		t.Fatalf("EnsureProject proj2: %v", err)
	}

	exceptionEv, exceptionRaw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	messageEv, messageRaw := loadFixtureEvent(t, "sentry-go-message.envelope")
	panicEv, panicRaw := loadFixtureEvent(t, "sentry-go-panic.envelope")

	if _, _, _, err := s.SaveEvent(ctx, "proj1", exceptionEv, exceptionRaw, "fp-exception", "outer failure"); err != nil {
		t.Fatalf("SaveEvent exception: %v", err)
	}
	if _, _, _, err := s.SaveEvent(ctx, "proj1", messageEv, messageRaw, "fp-message", "hello"); err != nil {
		t.Fatalf("SaveEvent message: %v", err)
	}
	// Different project — must not show up in proj1's list.
	if _, _, _, err := s.SaveEvent(ctx, "proj2", panicEv, panicRaw, "fp-panic", "simulated panic"); err != nil {
		t.Fatalf("SaveEvent panic: %v", err)
	}

	proj1Events, err := s.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents proj1: %v", err)
	}
	if len(proj1Events) != 2 {
		t.Fatalf("len(proj1Events) = %d, want 2", len(proj1Events))
	}

	proj2Events, err := s.ListEvents(ctx, "proj2")
	if err != nil {
		t.Fatalf("ListEvents proj2: %v", err)
	}
	if len(proj2Events) != 1 || proj2Events[0].EventID != panicEv.EventID {
		t.Fatalf("proj2Events = %+v, want exactly the panic event", proj2Events)
	}
}

func TestIssue_Level_ReflectsMostRecentEvent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if ev.Level != "error" {
		t.Fatalf("fixture level = %q, want error (test assumption)", ev.Level)
	}
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "shared-fp", "title"); err != nil {
		t.Fatalf("SaveEvent (occurrence 1, error): %v", err)
	}

	issue, err := s.GetIssue(ctx, "proj1", 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Level != "error" {
		t.Fatalf("Issue.Level = %q, want error", issue.Level)
	}

	// A second occurrence with a different level (e.g. downgraded to
	// warning by the app) should update what GetIssue reports, since Level
	// reflects the most recent event, not the first.
	ev.EventID = "second-occurrence"
	ev.Level = "warning"
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "shared-fp", "title"); err != nil {
		t.Fatalf("SaveEvent (occurrence 2, warning): %v", err)
	}

	issue, err = s.GetIssue(ctx, "proj1", 1)
	if err != nil {
		t.Fatalf("GetIssue (after second occurrence): %v", err)
	}
	if issue.Level != "warning" {
		t.Fatalf("Issue.Level after second occurrence = %q, want warning (most recent)", issue.Level)
	}
}

func TestSaveEvent_SameFingerprint_AggregatesIntoOneIssue(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	_, isNew, isRegression, err := s.SaveEvent(ctx, "proj1", ev, raw, "shared-fp", "same bug")
	if err != nil {
		t.Fatalf("SaveEvent (occurrence 1): %v", err)
	}
	if !isNew {
		t.Fatal("occurrence 1: isNew = false, want true (first occurrence of this fingerprint)")
	}
	if isRegression {
		t.Fatal("occurrence 1: isRegression = true, want false (a brand new issue can't be a regression)")
	}

	// A second, distinct occurrence of "the same issue" — different
	// event_id (as a real second crash would have), same fingerprint.
	ev.EventID = "second-occurrence-id"
	_, isNew, isRegression, err = s.SaveEvent(ctx, "proj1", ev, raw, "shared-fp", "same bug")
	if err != nil {
		t.Fatalf("SaveEvent (occurrence 2): %v", err)
	}
	if isNew {
		t.Fatal("occurrence 2: isNew = true, want false (the issue already existed)")
	}
	if isRegression {
		t.Fatal("occurrence 2: isRegression = true, want false (the issue was never resolved)")
	}

	events, err := s.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2 (two distinct event rows)", len(events))
	}
	if events[0].IssueID != events[1].IssueID {
		t.Fatalf("events have different IssueID (%d, %d), want the same issue", events[0].IssueID, events[1].IssueID)
	}

	issues, err := s.ListIssues(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want exactly 1 issue for two occurrences of the same fingerprint", len(issues))
	}
	if issues[0].EventCount != 2 {
		t.Fatalf("issues[0].EventCount = %d, want 2", issues[0].EventCount)
	}

	byIssue, err := s.ListEventsByIssue(ctx, "proj1", issues[0].ID)
	if err != nil {
		t.Fatalf("ListEventsByIssue: %v", err)
	}
	if len(byIssue) != 2 {
		t.Fatalf("ListEventsByIssue returned %d events, want 2", len(byIssue))
	}
}

func TestSetIssueStatus_ResolveIgnoreReopen(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}
	events, _ := s.ListEvents(ctx, "proj1")
	issueID := events[0].IssueID

	if err := s.SetIssueStatus(ctx, "proj1", issueID, StatusResolved); err != nil {
		t.Fatalf("SetIssueStatus(resolved): %v", err)
	}
	issue, err := s.GetIssue(ctx, "proj1", issueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != StatusResolved {
		t.Fatalf("Status = %q, want resolved", issue.Status)
	}

	if err := s.SetIssueStatus(ctx, "proj1", issueID, StatusIgnored); err != nil {
		t.Fatalf("SetIssueStatus(ignored): %v", err)
	}
	issue, _ = s.GetIssue(ctx, "proj1", issueID)
	if issue.Status != StatusIgnored {
		t.Fatalf("Status = %q, want ignored", issue.Status)
	}

	if err := s.SetIssueStatus(ctx, "proj1", issueID, StatusUnresolved); err != nil {
		t.Fatalf("SetIssueStatus(unresolved): %v", err)
	}
	issue, _ = s.GetIssue(ctx, "proj1", issueID)
	if issue.Status != StatusUnresolved {
		t.Fatalf("Status = %q, want unresolved (manual reopen)", issue.Status)
	}
}

func TestSetIssueStatus_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	err := s.SetIssueStatus(ctx, "proj1", 9999, StatusResolved)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestSaveEvent_NewOccurrence_RegressesResolvedIssue(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent (first): %v", err)
	}
	events, _ := s.ListEvents(ctx, "proj1")
	issueID := events[0].IssueID

	if err := s.SetIssueStatus(ctx, "proj1", issueID, StatusResolved); err != nil {
		t.Fatalf("SetIssueStatus: %v", err)
	}

	ev.EventID = "a-new-occurrence-after-the-fix-didnt-hold"
	_, isNew, isRegression, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp1", "title")
	if err != nil {
		t.Fatalf("SaveEvent (regression): %v", err)
	}
	if isNew {
		t.Fatal("isNew = true, want false (this issue already existed)")
	}
	if !isRegression {
		t.Fatal("isRegression = false, want true (the issue was resolved immediately before this call)")
	}

	issue, err := s.GetIssue(ctx, "proj1", issueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != StatusUnresolved {
		t.Fatalf("Status = %q, want unresolved (a new occurrence must regress a resolved issue)", issue.Status)
	}
	if issue.EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2", issue.EventCount)
	}
}

func TestSaveEvent_NewOccurrence_KeepsIgnoredIssueIgnored(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent (first): %v", err)
	}
	events, _ := s.ListEvents(ctx, "proj1")
	issueID := events[0].IssueID

	if err := s.SetIssueStatus(ctx, "proj1", issueID, StatusIgnored); err != nil {
		t.Fatalf("SetIssueStatus: %v", err)
	}

	ev.EventID = "another-occurrence-while-ignored"
	_, isNew, isRegression, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp1", "title")
	if err != nil {
		t.Fatalf("SaveEvent (while ignored): %v", err)
	}
	if isNew {
		t.Fatal("isNew = true, want false")
	}
	if isRegression {
		t.Fatal("isRegression = true, want false -- the prior status was ignored, not resolved, so this is not a regression")
	}

	issue, err := s.GetIssue(ctx, "proj1", issueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != StatusIgnored {
		t.Fatalf("Status = %q, want ignored to remain (unlike resolved, ignored is a deliberate suppression)", issue.Status)
	}
	if issue.EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2 (still counted, just not surfaced as a regression)", issue.EventCount)
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	_, err := s.GetIssue(ctx, "proj1", 9999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteOldEvents_RemovesOnlyOldEvents(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	oldEv, oldRaw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if _, _, _, err := s.SaveEvent(ctx, "proj1", oldEv, oldRaw, "fp-old", "old bug"); err != nil {
		t.Fatalf("SaveEvent old: %v", err)
	}
	newEv, newRaw := loadFixtureEvent(t, "sentry-go-message.envelope")
	if _, _, _, err := s.SaveEvent(ctx, "proj1", newEv, newRaw, "fp-new", "new bug"); err != nil {
		t.Fatalf("SaveEvent new: %v", err)
	}

	// SaveEvent always stamps received_at with CURRENT_TIMESTAMP -- backdate
	// directly (tests are in-package, so s.db is accessible) since that's
	// the only way to deterministically exercise a retention cutoff.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE events SET received_at = ? WHERE event_id = ?`,
		time.Now().Add(-30*24*time.Hour), oldEv.EventID); err != nil {
		t.Fatalf("backdating old event: %v", err)
	}

	deleted, err := s.DeleteOldEvents(ctx, 7*24*time.Hour) // retain 7 days
	if err != nil {
		t.Fatalf("DeleteOldEvents: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (only the backdated event)", deleted)
	}

	events, err := s.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].EventID != newEv.EventID {
		t.Fatalf("remaining events = %+v, want only the new one", events)
	}

	// Issue aggregate rows must survive even though their underlying events
	// were pruned -- they're the historical summary, not bulky data.
	issues, err := s.ListIssues(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2 (both issue rows survive event pruning)", len(issues))
	}
}

func TestDeleteOldEvents_NothingToDelete(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if _, _, _, err := s.SaveEvent(ctx, "proj1", ev, raw, "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	deleted, err := s.DeleteOldEvents(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("DeleteOldEvents: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (nothing old enough)", deleted)
	}
}

func TestBackupTo_ProducesValidIndependentCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "backup.db")

	src, err := Open(srcPath)
	if err != nil {
		t.Fatalf("Open source: %v", err)
	}
	defer src.Close()

	if err := src.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev, raw := loadFixtureEvent(t, "sentry-go-exception.envelope")
	if _, _, _, err := src.SaveEvent(ctx, "proj1", ev, raw, "fp1", "backed up bug"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	if err := src.BackupTo(ctx, destPath); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}

	// The backup must be a fully independent, openable database -- not a
	// reference to the source, not a partial/torn copy.
	backup, err := Open(destPath)
	if err != nil {
		t.Fatalf("Open backup: %v", err)
	}
	defer backup.Close()

	events, err := backup.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents on backup: %v", err)
	}
	if len(events) != 1 || events[0].EventID != ev.EventID {
		t.Fatalf("backup events = %+v, want the one saved event", events)
	}

	issues, err := backup.ListIssues(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListIssues on backup: %v", err)
	}
	if len(issues) != 1 || issues[0].Title != "backed up bug" {
		t.Fatalf("backup issues = %+v", issues)
	}

	// Further writes to the source must not appear in the (independent) backup.
	ev.EventID = "written-after-backup"
	if _, _, _, err := src.SaveEvent(ctx, "proj1", ev, raw, "fp1", "backed up bug"); err != nil {
		t.Fatalf("SaveEvent (post-backup): %v", err)
	}
	eventsAfter, err := backup.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents on backup (post-write): %v", err)
	}
	if len(eventsAfter) != 1 {
		t.Fatalf("backup should be frozen at backup time, got %d events", len(eventsAfter))
	}
}
