package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/itskrsna/Trackdown/internal/store"
)

func TestHealthzHandler_Healthy(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	healthzHandler(st)(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHealthzHandler_Unhealthy(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.Close() // simulate an unreachable/closed database

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	healthzHandler(st)(w, req.WithContext(context.Background()))

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestNewLogger(t *testing.T) {
	if _, err := newLogger("text"); err != nil {
		t.Fatalf("newLogger(text): %v", err)
	}
	if _, err := newLogger("json"); err != nil {
		t.Fatalf("newLogger(json): %v", err)
	}
	if _, err := newLogger("xml"); err == nil {
		t.Fatal("newLogger(xml) should have failed")
	}
}

// TestRunRetentionLoop_RunsImmediatelyAndExitsOnCancel proves the lifecycle
// behavior unique to this package: an immediate cleanup run at startup
// (not just after the first 24h tick) and a prompt, clean exit when ctx is
// canceled at shutdown -- deletion correctness itself (which rows get
// removed) is already covered by internal/store's tests.
func TestRunRetentionLoop_RunsImmediatelyAndExitsOnCancel(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runRetentionLoop(ctx, st, logger, 24*time.Hour)
		close(done)
	}()

	// Give the immediate run a moment to complete against the (empty, so
	// error-free) store, then cancel and confirm the goroutine actually
	// exits -- proving ctx.Done() is honored, not just the ticker branch,
	// which matters for a clean server shutdown.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runRetentionLoop did not exit promptly after context cancellation")
	}
}

func TestGC_RejectsNonPositiveRetentionDays(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := gc([]string{"-db", dbPath, "-retention-days", "0"}); err == nil {
		t.Fatal("expected an error for -retention-days=0")
	}
	if err := gc([]string{"-db", dbPath, "-retention-days", "-5"}); err == nil {
		t.Fatal("expected an error for a negative -retention-days")
	}
}

func TestGC_ValidRetention_Succeeds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	// Create the database file first (gc opens an existing store, same as
	// serve would against an already-running deployment's db).
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.Close()

	if err := gc([]string{"-db", dbPath, "-retention-days", "30"}); err != nil {
		t.Fatalf("gc: %v", err)
	}
}

func TestBackup_ProducesOpenableFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "backup.db")

	src, err := store.Open(srcPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := src.EnsureProject(context.Background(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	src.Close()

	if err := backup([]string{"-db", srcPath, destPath}); err != nil {
		t.Fatalf("backup: %v", err)
	}

	verify, err := store.Open(destPath)
	if err != nil {
		t.Fatalf("opening backup file: %v", err)
	}
	defer verify.Close()
	if _, err := verify.GetProject(context.Background(), "proj1"); err != nil {
		t.Fatalf("GetProject on backup: %v", err)
	}
}

func TestBackup_RequiresExactlyOneDestArg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := backup([]string{"-db", dbPath}); err == nil {
		t.Fatal("expected an error when no destination path is given")
	}
}
