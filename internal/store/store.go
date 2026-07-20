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
	"sync/atomic"
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

CREATE TABLE IF NOT EXISTS alert_outbox (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	issue_id INTEGER NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	level TEXT NOT NULL DEFAULT '',
	is_new INTEGER NOT NULL DEFAULT 0,
	is_regression INTEGER NOT NULL DEFAULT 0,
	event_count INTEGER NOT NULL DEFAULT 0,
	occurred_at DATETIME NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	attempts INTEGER NOT NULL DEFAULT 0,
	next_attempt_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_alert_outbox_due ON alert_outbox(status, next_attempt_at);

CREATE TABLE IF NOT EXISTS releases (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL REFERENCES projects(id),
	version TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(project_id, version)
);

CREATE TABLE IF NOT EXISTS artifacts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	release_id INTEGER NOT NULL REFERENCES releases(id),
	abs_path TEXT NOT NULL,
	sourcemap_content BLOB NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(release_id, abs_path)
);
`

// readerPoolSize is the reader connection pool's fixed size. Not
// user-configurable in v1 -- there's no evidence yet of needing that knob,
// and adding one before real usage data justifies it would be speculative.
const readerPoolSize = 4

// memoryDBCounter gives each ":memory:" Store its own uniquely-named
// shared-cache in-memory database (see the comment in Open for why this
// matters), so concurrent tests using ":memory:" never collide with each
// other.
var memoryDBCounter atomic.Int64

// Store wraps a SQLite database holding all of Trackdown's persisted state.
// Reads and writes go through separate *sql.DB pools so concurrent web-UI
// reads don't serialize behind ingest writes (see the WAL mode comment in
// Open) -- but both point at the exact same underlying database, so every
// method's behavior is identical to a single shared connection as far as
// callers can tell.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// Open opens (creating if necessary) a SQLite database at path and applies
// the schema. path may be ":memory:" for an ephemeral in-process database,
// as used in tests.
func Open(path string) (*Store, error) {
	dsn := path
	if path == ":memory:" {
		// A bare ":memory:" DSN gives every *sql.DB connection its own
		// independent, unshared in-memory database -- fine for the old
		// single-connection design, but readDB and writeDB below are two
		// separate connection pools that need to see the same data. SQLite's
		// shared-cache URI form makes multiple connections share one named
		// in-memory database; the counter keeps concurrent Store instances
		// (e.g. parallel tests) from colliding on the same shared name.
		dsn = fmt.Sprintf("file:trackdown-memory-%d?mode=memory&cache=shared", memoryDBCounter.Add(1))
	}

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database %q: %w", path, err)
	}
	// SQLite serializes writers regardless of how many connections ask for
	// one; a single shared write connection avoids "database is locked"
	// errors under concurrent ingest.
	writeDB.SetMaxOpenConns(1)

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("opening database %q (reader pool): %w", path, err)
	}
	readDB.SetMaxOpenConns(readerPoolSize)

	// busy_timeout on both: if a query ever does contend (WAL still allows a
	// writer and readers to briefly race a page), wait up to 5s for the lock
	// instead of failing immediately with "database is locked."
	for _, db := range []*sql.DB{writeDB, readDB} {
		if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
			writeDB.Close()
			readDB.Close()
			return nil, fmt.Errorf("setting busy_timeout: %w", err)
		}
	}

	// WAL mode is what actually lets readDB's connections read concurrently
	// with an in-flight write on writeDB, rather than blocking behind it (the
	// default rollback-journal mode takes a database-wide lock for the
	// duration of a write). It's a property of the database file itself, so
	// setting it once via either connection is enough, but every reader
	// still needs its own busy_timeout above for the brief windows WAL
	// doesn't fully avoid (e.g. a writer mid-checkpoint).
	if _, err := writeDB.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	if _, err := writeDB.Exec(schema); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	return &Store{writeDB: writeDB, readDB: readDB}, nil
}

// Close closes both underlying database connection pools.
func (s *Store) Close() error {
	writeErr := s.writeDB.Close()
	readErr := s.readDB.Close()
	if writeErr != nil {
		return writeErr
	}
	return readErr
}

// Ping verifies both database connection pools are alive, for use by a
// health-check endpoint.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.writeDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping (writer): %w", err)
	}
	if err := s.readDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping (reader): %w", err)
	}
	return nil
}

// BackupTo writes a consistent point-in-time snapshot of the database to
// destPath using SQLite's VACUUM INTO — a single SQL statement that
// produces a complete, self-contained, valid copy while the server keeps
// running, with no risk of the torn/inconsistent snapshot a raw file copy
// could produce if something writes mid-copy.
func (s *Store) BackupTo(ctx context.Context, destPath string) error {
	if _, err := s.writeDB.ExecContext(ctx, `VACUUM INTO ?`, destPath); err != nil {
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
	res, err := s.writeDB.ExecContext(ctx, `DELETE FROM events WHERE received_at < ?`, cutoff)
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
	_, err := s.writeDB.ExecContext(ctx,
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
	err := s.readDB.QueryRowContext(ctx,
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
	row := s.readDB.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	return scanProject(row)
}

// ListProjects returns every project, most recently created first.
func (s *Store) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC, id DESC`)
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
	tx, err := s.writeDB.BeginTx(ctx, nil)
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
		// .UTC() defensively, even though protocol already normalizes: the
		// SQLite driver's default time round-trip only survives a named zone
		// (UTC always has one; an unnormalized non-UTC offset may not).
		// Storage shouldn't depend on every caller
		// remembering this.
		ts = sql.NullTime{Time: ev.Timestamp.Time.UTC(), Valid: true}
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
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = ? AND event_id = ?`,
		projectID, eventID)
	return scanEvent(row)
}

// ListEvents returns all events for a project, most recently received first.
func (s *Store) ListEvents(ctx context.Context, projectID string) ([]*StoredEvent, error) {
	rows, err := s.readDB.QueryContext(ctx,
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
	rows, err := s.readDB.QueryContext(ctx,
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
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE project_id = ? AND id = ?`,
		projectID, issueID)
	return scanIssue(row)
}

// ListIssues returns every issue for a project, most recently active first.
func (s *Store) ListIssues(ctx context.Context, projectID string) ([]*Issue, error) {
	rows, err := s.readDB.QueryContext(ctx,
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
	res, err := s.writeDB.ExecContext(ctx,
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

// alertBackoffSchedule gives the wait duration before each successive retry
// attempt (index 0 = wait after the 1st failure, etc.), capping at its last
// entry rather than growing unbounded. alertMaxAttempts bounds total retries
// before an entry is dead-lettered (kept in the table for inspection, never
// silently deleted, but no longer picked up by DueAlerts).
var alertBackoffSchedule = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	12 * time.Hour,
}

const alertMaxAttempts = 8

func alertBackoff(attempts int) time.Duration {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(alertBackoffSchedule) {
		idx = len(alertBackoffSchedule) - 1
	}
	return alertBackoffSchedule[idx]
}

// OutboxStatus is an alert_outbox entry's delivery state.
type OutboxStatus string

const (
	OutboxPending   OutboxStatus = "pending"
	OutboxDelivered OutboxStatus = "delivered"
	OutboxDead      OutboxStatus = "dead"
)

// OutboxEntry is a durable record of an alert delivery that failed at least
// once and needs retrying — the persistent counterpart to the immediate,
// best-effort notification attempt in internal/ingest. Field names
// deliberately mirror internal/alert.NotifyEvent's shape; store doesn't
// import internal/alert directly (keeping store's dependency graph exactly
// as documented — it depends only on protocol), so callers convert between
// the two.
type OutboxEntry struct {
	ID            int64
	ProjectID     string
	IssueID       int64
	Title         string
	Level         string
	IsNew         bool
	IsRegression  bool
	EventCount    int64
	OccurredAt    time.Time
	Status        OutboxStatus
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	LastError     string
}

// EnqueueAlert persists an alert delivery that just failed its first
// (immediate, best-effort) attempt, so a background retry loop can pick it
// up later. lastErr is recorded for operator visibility.
func (s *Store) EnqueueAlert(ctx context.Context, e OutboxEntry, lastErr error) (int64, error) {
	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	nextAttemptAt := time.Now().UTC().Add(alertBackoff(1))
	res, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO alert_outbox
		 (project_id, issue_id, title, level, is_new, is_regression, event_count, occurred_at, status, attempts, next_attempt_at, last_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ProjectID, e.IssueID, e.Title, e.Level, e.IsNew, e.IsRegression, e.EventCount, e.OccurredAt.UTC(),
		string(OutboxPending), 1, nextAttemptAt, errMsg)
	if err != nil {
		return 0, fmt.Errorf("enqueuing alert for issue %d: %w", e.IssueID, err)
	}
	return res.LastInsertId()
}

const outboxColumns = `id, project_id, issue_id, title, level, is_new, is_regression, event_count, occurred_at, status, attempts, next_attempt_at, created_at, last_error`

// DueAlerts returns pending outbox entries whose next_attempt_at has passed,
// oldest first, up to limit.
func (s *Store) DueAlerts(ctx context.Context, now time.Time, limit int) ([]*OutboxEntry, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+outboxColumns+` FROM alert_outbox
		 WHERE status = ? AND next_attempt_at <= ?
		 ORDER BY next_attempt_at ASC, id ASC
		 LIMIT ?`,
		string(OutboxPending), now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("listing due alerts: %w", err)
	}
	defer rows.Close()

	var out []*OutboxEntry
	for rows.Next() {
		e, err := scanOutboxEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkAlertDelivered marks an outbox entry as successfully delivered.
func (s *Store) MarkAlertDelivered(ctx context.Context, id int64) error {
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE alert_outbox SET status = ? WHERE id = ?`, string(OutboxDelivered), id)
	if err != nil {
		return fmt.Errorf("marking alert %d delivered: %w", id, err)
	}
	return nil
}

// MarkAlertFailed records another failed delivery attempt: bumps the
// attempt count, and either schedules the next retry via alertBackoff or,
// past alertMaxAttempts, dead-letters the entry (kept for inspection, never
// retried again, never deleted).
func (s *Store) MarkAlertFailed(ctx context.Context, id int64, attempts int, lastErr error) error {
	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	if attempts >= alertMaxAttempts {
		_, err := s.writeDB.ExecContext(ctx,
			`UPDATE alert_outbox SET status = ?, attempts = ?, last_error = ? WHERE id = ?`,
			string(OutboxDead), attempts, errMsg, id)
		if err != nil {
			return fmt.Errorf("dead-lettering alert %d: %w", id, err)
		}
		return nil
	}
	nextAttemptAt := time.Now().UTC().Add(alertBackoff(attempts))
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE alert_outbox SET attempts = ?, next_attempt_at = ?, last_error = ? WHERE id = ?`,
		attempts, nextAttemptAt, errMsg, id)
	if err != nil {
		return fmt.Errorf("updating alert %d after failed retry: %w", id, err)
	}
	return nil
}

func scanOutboxEntry(row rowScanner) (*OutboxEntry, error) {
	var (
		e             OutboxEntry
		status        string
		occurredAt    time.Time
		nextAttemptAt time.Time
		createdAt     time.Time
	)
	if err := row.Scan(&e.ID, &e.ProjectID, &e.IssueID, &e.Title, &e.Level, &e.IsNew, &e.IsRegression,
		&e.EventCount, &occurredAt, &status, &e.Attempts, &nextAttemptAt, &createdAt, &e.LastError); err != nil {
		return nil, err
	}
	e.Status = OutboxStatus(status)
	e.OccurredAt = occurredAt
	e.NextAttemptAt = nextAttemptAt
	e.CreatedAt = createdAt
	return &e, nil
}

// CreateRelease ensures a release row exists for (projectID, version),
// returning its ID. Idempotent — re-uploading artifacts for an
// already-known release (CI re-runs, additional files uploaded
// incrementally) is a normal workflow, not an error condition, so a second
// call for the same (projectID, version) returns the existing release's ID
// rather than failing on the UNIQUE constraint.
func (s *Store) CreateRelease(ctx context.Context, projectID, version string) (int64, error) {
	var id int64
	err := s.writeDB.QueryRowContext(ctx,
		`INSERT INTO releases (project_id, version) VALUES (?, ?)
		 ON CONFLICT(project_id, version) DO UPDATE SET version = excluded.version
		 RETURNING id`,
		projectID, version).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("creating release %q for project %q: %w", version, projectID, err)
	}
	return id, nil
}

// UploadArtifact stores (or replaces) a sourcemap file for a release, keyed
// by its abs_path — the same absolute URL a stack frame's AbsPath field
// references, which is how FindArtifact looks it back up at symbolication
// time.
func (s *Store) UploadArtifact(ctx context.Context, releaseID int64, absPath string, sourcemapContent []byte) error {
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO artifacts (release_id, abs_path, sourcemap_content) VALUES (?, ?, ?)
		 ON CONFLICT(release_id, abs_path) DO UPDATE SET sourcemap_content = excluded.sourcemap_content, created_at = CURRENT_TIMESTAMP`,
		releaseID, absPath, sourcemapContent)
	if err != nil {
		return fmt.Errorf("uploading artifact %q for release %d: %w", absPath, releaseID, err)
	}
	return nil
}

// FindArtifact looks up a sourcemap by project, release version, and the
// frame's abs_path. Returns sql.ErrNoRows when no matching release/artifact
// exists — the common case for any event ingested before its sourcemaps
// were uploaded (or for a release/path that was simply never uploaded), so
// callers must treat that as "nothing to resolve, render the raw frame,"
// never as an error worth surfacing to a user.
func (s *Store) FindArtifact(ctx context.Context, projectID, version, absPath string) ([]byte, error) {
	var content []byte
	err := s.readDB.QueryRowContext(ctx,
		`SELECT a.sourcemap_content FROM artifacts a
		 JOIN releases r ON r.id = a.release_id
		 WHERE r.project_id = ? AND r.version = ? AND a.abs_path = ?`,
		projectID, version, absPath).Scan(&content)
	if err != nil {
		return nil, err
	}
	return content, nil
}
