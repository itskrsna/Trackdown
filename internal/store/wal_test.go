package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestWALMode_WriteDoesNotBlockOnHeldRead proves the actual property WAL
// mode + a separate reader pool exist to provide. This is the direction that
// actually distinguishes WAL from the default rollback-journal mode: a
// held-open read transaction (e.g. a slow web-UI query) takes a SHARED lock
// that's compatible with a writer merely opening a transaction under either
// mode, but under rollback-journal mode the writer blocks at COMMIT time
// until every reader's SHARED lock is released -- confirmed empirically
// here, not just asserted, by running this same test with WAL disabled and
// watching it fail. Under WAL, the writer's commit never waits on readers at
// all. A real file-backed database is used deliberately, not ":memory:",
// since this is exactly the kind of property worth verifying against real
// SQLite file-locking behavior rather than assumed from documentation.
func TestWALMode_WriteDoesNotBlockOnHeldRead(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal-concurrency-test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	readTx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BeginTx (read): %v", err)
	}
	var count int
	if err := readTx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&count); err != nil {
		t.Fatalf("query within held read transaction: %v", err)
	}

	const holdDuration = 500 * time.Millisecond
	readDone := make(chan struct{})
	go func() {
		time.Sleep(holdDuration)
		if err := readTx.Rollback(); err != nil {
			t.Errorf("Rollback: %v", err)
		}
		close(readDone)
	}()

	writeStart := time.Now()
	if err := s.EnsureProject(ctx, "proj2", "key2"); err != nil {
		t.Fatalf("EnsureProject during held read: %v", err)
	}
	writeElapsed := time.Since(writeStart)
	if writeElapsed >= holdDuration {
		t.Fatalf("write took %v, wanted well under %v — it looks like it blocked behind the held read transaction", writeElapsed, holdDuration)
	}

	<-readDone // let the held read transaction finish cleanly before the test ends
}
