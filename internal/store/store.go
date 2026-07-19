// Package store is Trackdown's storage layer: a single SQLite database file
// holding projects and their ingested events. It uses modernc.org/sqlite, a
// pure-Go driver with no CGO dependency, so the whole server stays a single
// dependency-free binary on every platform, Windows included.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/itskrsna/Trackdown/internal/protocol"
)

// IssueStatus is an issue's position in its resolution lifecycle.
type IssueStatus string

const (
	StatusUnresolved IssueStatus = "unresolved"
	StatusResolved   IssueStatus = "resolved"
	StatusIgnored    IssueStatus = "ignored"
)

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	public_key TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS issues (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL REFERENCES projects(id),
	fingerprint TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'unresolved',
	first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	event_count INTEGER NOT NULL DEFAULT 0,
	UNIQUE(project_id, fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_issues_project_last_seen ON issues(project_id, last_seen DESC, id DESC);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL REFERENCES projects(id),
	issue_id INTEGER REFERENCES issues(id),
	event_id TEXT NOT NULL,
	received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	event_timestamp DATETIME,
	level TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '',
	payload BLOB NOT NULL,
	UNIQUE(project_id, event_id)
);

CREATE INDEX IF NOT EXISTS idx_events_project_received ON events(project_id, received_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_events_issue ON events(issue_id, received_at DESC);
`

// Store wraps a SQLite database holding all of Trackdown's persisted state.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) a SQLite database at path and applies
// the schema. path may be ":memory:" for an ephemeral in-process database,
// as used in tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	// SQLite serializes writers regardless; a single shared connection avoids
	// "database is locked" errors under concurrent ingest without requiring
	// WAL-mode tuning yet (a later performance milestone, not a v1 concern).
	db.SetMaxOpenConns(1)

	// A defensive one-liner regardless of the single-connection setup above:
	// if a query ever does contend (e.g. a future WAL/multi-connection
	// change), wait up to 5s for the lock instead of failing immediately
	// with "database is locked."
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy_timeout: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping verifies the database connection is alive, for use by a health-check
// endpoint.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// BackupTo writes a consistent point-in-time snapshot of the database to
// destPath using SQLite's VACUUM INTO — a single SQL statement that
// produces a complete, self-contained, valid copy while the server keeps
// running, with no risk of the torn/inconsistent snapshot a raw file copy
// could produce if something writes mid-copy.
func (s *Store) BackupTo(ctx context.Context, destPath string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, destPath); err != nil {
		return fmt.Errorf("backing up to %q: %w", destPath, err)
	}
	return nil
}

// DeleteOldEvents deletes event rows (and their JSON payload blobs)
// received before now.Add(-olderThan), returning how many were removed.
//
// Issue rows are deliberately NOT deleted here — they remain as historical
// aggregate summaries (title, status, first/last seen, event_count) even
// after their underlying events are pruned. It's the per-event JSON
// payloads that are bulky and actually need a retention limit in practice;
// the small aggregate row is worth keeping indefinitely as a record that
// the issue existed.
func (s *Store) DeleteOldEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE received_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("deleting events older than %s: %w", olderThan, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("checking rows affected: %w", err)
	}
	return n, nil
}

// EnsureProject creates a project record if one doesn't already exist for
// id. It's called from the ingest path so a project becomes usable the
// moment its DSN is configured in an SDK, without a separate provisioning
// step. An existing project's public key is left untouched.
func (s *Store) EnsureProject(ctx context.Context, id, publicKey string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (id, public_key) VALUES (?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		id, publicKey)
	if err != nil {
		return fmt.Errorf("ensuring project %q: %w", id, err)
	}
	return nil
}

// ProjectPublicKey returns the stored public key for a project, or
// sql.ErrNoRows if the project doesn't exist.
func (s *Store) ProjectPublicKey(ctx context.Context, projectID string) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx,
		`SELECT public_key FROM projects WHERE id = ?`, projectID).Scan(&key)
	if err != nil {
		return "", err
	}
	return key, nil
}

// Project is a Trackdown project — created automatically on first ingest,
// identified by the ID used in its DSN.
type Project struct {
	ID        string
	PublicKey string
	Name      string
	CreatedAt time.Time
}

const projectColumns = `id, public_key, name, created_at`

// GetProject looks up a single project by ID. It returns sql.ErrNoRows if
// no such project exists.
func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	return scanProject(row)
}

// ListProjects returns every project, most recently created first.
func (s *Store) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()

	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProject(row rowScanner) (*Project, error) {
	var p Project
	if err := row.Scan(&p.ID, &p.PublicKey, &p.Name, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveEvent stores a parsed event under projectID and links it to the issue
// identified by fingerprint (created on first occurrence). raw is the
// original event JSON exactly as received, preserved so nothing is lost to
// the Event struct's inevitably partial field coverage.
//
// Saving the same (projectID, event_id) pair twice is a no-op — SDKs may
// retry deliveries — and deliberately does NOT bump the issue's event count
// a second time; the existence check below runs before the issue upsert
// specifically so a retried delivery can't inflate the count. In that case
// isNew and isRegression are both false regardless of the issue's actual
// history, since nothing about it changed on this call.
//
// isNew reports whether this occurrence created a brand new issue.
// isRegression reports whether the issue was StatusResolved immediately
// before this call — a resolved issue that receives a new event
// automatically regresses to unresolved (a fix that didn't hold is worth
// surfacing again), while an ignored issue stays ignored (a deliberate
// "don't tell me again" signal, not something a new occurrence should
// silently override) — isRegression is false in that case, by design.
// Callers (alerting) use issueID/isNew/isRegression to decide when and
// about what to notify.
func (s *Store) SaveEvent(ctx context.Context, projectID string, ev *protocol.Event, raw []byte, fingerprint, title string) (issueID int64, isNew, isRegression bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, false, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM events WHERE project_id = ? AND event_id = ?`,
		projectID, ev.EventID).Scan(&exists)
	switch {
	case err == nil:
		// Already stored; a no-op, not a second count against the issue.
		// Still worth returning the existing issue ID rather than 0.
		var existingIssueID sql.NullInt64
		if lookupErr := tx.QueryRowContext(ctx,
			`SELECT issue_id FROM events WHERE project_id = ? AND event_id = ?`,
			projectID, ev.EventID).Scan(&existingIssueID); lookupErr == nil && existingIssueID.Valid {
			issueID = existingIssueID.Int64
		}
		return issueID, false, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, false, fmt.Errorf("checking for existing event %q: %w", ev.EventID, err)
	}

	var priorStatus sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM issues WHERE project_id = ? AND fingerprint = ?`,
		projectID, fingerprint).Scan(&priorStatus)
	switch {
	case err == nil:
		isNew = false
	case errors.Is(err, sql.ErrNoRows):
		isNew = true
	default:
		return 0, false, false, fmt.Errorf("checking for existing issue with fingerprint %q: %w", fingerprint, err)
	}
	isRegression = !isNew && priorStatus.String == string(StatusResolved)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, event_count)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(project_id, fingerprint) DO UPDATE SET
			last_seen = CURRENT_TIMESTAMP,
			event_count = event_count + 1,
			status = CASE WHEN status = 'resolved' THEN 'unresolved' ELSE status END
		RETURNING id`,
		projectID, fingerprint, title).Scan(&issueID)
	if err != nil {
		return 0, false, false, fmt.Errorf("upserting issue for fingerprint %q: %w", fingerprint, err)
	}

	var ts sql.NullTime
	if !ev.Timestamp.IsZero() {
		ts = sql.NullTime{Time: ev.Timestamp.Time, Valid: true}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events (project_id, issue_id, event_id, event_timestamp, level, platform, message, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, issueID, ev.EventID, ts, ev.Level, ev.Platform, ev.Message.String(), raw); err != nil {
		return 0, false, false, fmt.Errorf("saving event %q for project %q: %w", ev.EventID, projectID, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, false, false, fmt.Errorf("committing event %q: %w", ev.EventID, err)
	}
	return issueID, isNew, isRegression, nil
}

// StoredEvent is a summary of an event as read back from storage.
type StoredEvent struct {
	EventID    string
	IssueID    int64
	ReceivedAt time.Time
	Timestamp  time.Time
	Level      string
	Platform   string
	Message    string
	Payload    json.RawMessage
}

const eventColumns = `event_id, issue_id, received_at, event_timestamp, level, platform, message, payload`

// GetEvent looks up a single event by its Sentry event_id within a project.
// It returns sql.ErrNoRows if no such event exists.
func (s *Store) GetEvent(ctx context.Context, projectID, eventID string) (*StoredEvent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = ? AND event_id = ?`,
		projectID, eventID)
	return scanEvent(row)
}

// ListEvents returns all events for a project, most recently received first.
func (s *Store) ListEvents(ctx context.Context, projectID string) ([]*StoredEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = ? ORDER BY received_at DESC, id DESC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("listing events for project %q: %w", projectID, err)
	}
	defer rows.Close()

	var out []*StoredEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ListEventsByIssue returns every event linked to a specific issue, most
// recently received first.
func (s *Store) ListEventsByIssue(ctx context.Context, projectID string, issueID int64) ([]*StoredEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = ? AND issue_id = ? ORDER BY received_at DESC, id DESC`,
		projectID, issueID)
	if err != nil {
		return nil, fmt.Errorf("listing events for issue %d in project %q: %w", issueID, projectID, err)
	}
	defer rows.Close()

	var out []*StoredEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting scan
// helpers serve single-row and multi-row queries identically.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanEvent(row rowScanner) (*StoredEvent, error) {
	var (
		ev         StoredEvent
		issueID    sql.NullInt64
		receivedAt time.Time
		ts         sql.NullTime
		payload    []byte
	)
	if err := row.Scan(&ev.EventID, &issueID, &receivedAt, &ts, &ev.Level, &ev.Platform, &ev.Message, &payload); err != nil {
		return nil, err
	}
	if issueID.Valid {
		ev.IssueID = issueID.Int64
	}
	ev.ReceivedAt = receivedAt
	if ts.Valid {
		ev.Timestamp = ts.Time
	}
	ev.Payload = payload
	return &ev, nil
}

// Issue is an aggregation of events sharing a fingerprint — Trackdown's unit
// of "the same underlying bug."
type Issue struct {
	ID          int64
	Fingerprint string
	Title       string
	Status      IssueStatus
	FirstSeen   time.Time
	LastSeen    time.Time
	EventCount  int64
	// Level is the most recent event's level (e.g. "error", "warning").
	// Issues themselves don't store a level column — this is a correlated
	// subquery against events rather than a schema migration, since level
	// can legitimately vary across occurrences of the same issue and the
	// most recent one is the most useful single value to show in a list.
	Level string
}

const issueColumns = `id, fingerprint, title, status, first_seen, last_seen, event_count,
	(SELECT level FROM events WHERE issue_id = issues.id ORDER BY received_at DESC, id DESC LIMIT 1)`

// GetIssue looks up a single issue by ID within a project. It returns
// sql.ErrNoRows if no such issue exists.
func (s *Store) GetIssue(ctx context.Context, projectID string, issueID int64) (*Issue, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE project_id = ? AND id = ?`,
		projectID, issueID)
	return scanIssue(row)
}

// ListIssues returns every issue for a project, most recently active first.
func (s *Store) ListIssues(ctx context.Context, projectID string) ([]*Issue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE project_id = ? ORDER BY last_seen DESC, id DESC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("listing issues for project %q: %w", projectID, err)
	}
	defer rows.Close()

	var out []*Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, issue)
	}
	return out, rows.Err()
}

// SetIssueStatus transitions an issue to a new lifecycle status (resolve,
// ignore, or reopen by setting StatusUnresolved directly). It returns
// sql.ErrNoRows if no such issue exists in the project.
func (s *Store) SetIssueStatus(ctx context.Context, projectID string, issueID int64, status IssueStatus) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE issues SET status = ? WHERE project_id = ? AND id = ?`,
		string(status), projectID, issueID)
	if err != nil {
		return fmt.Errorf("setting issue %d status to %q: %w", issueID, status, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected updating issue %d: %w", issueID, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanIssue(row rowScanner) (*Issue, error) {
	var (
		issue     Issue
		status    string
		firstSeen time.Time
		lastSeen  time.Time
		level     sql.NullString
	)
	if err := row.Scan(&issue.ID, &issue.Fingerprint, &issue.Title, &status, &firstSeen, &lastSeen, &issue.EventCount, &level); err != nil {
		return nil, err
	}
	issue.Status = IssueStatus(status)
	issue.FirstSeen = firstSeen
	issue.LastSeen = lastSeen
	if level.Valid {
		issue.Level = level.String
	}
	return &issue, nil
}
