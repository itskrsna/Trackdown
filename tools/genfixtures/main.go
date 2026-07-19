// Command genfixtures captures real envelope bytes emitted by official Sentry SDKs
// and writes them to every package's testdata/envelopes/ that needs them.
// These fixtures are the conformance oracle for the envelope parser and
// storage layer: a valid envelope is defined by what a real SDK actually
// sends, not by a hand re-derivation of the spec. Each consuming package
// gets its own copy (rather than reaching into a sibling package's testdata)
// so packages stay independently testable. Re-run this whenever an SDK
// dependency is upgraded to catch wire-format drift.
package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genfixtures:", err)
		os.Exit(1)
	}
}

// outDirs lists every package testdata directory that needs a copy of the
// captured fixtures.
var outDirs = []string{
	filepath.Join("internal", "protocol", "testdata", "envelopes"),
	filepath.Join("internal", "store", "testdata", "envelopes"),
	filepath.Join("internal", "ingest", "testdata", "envelopes"),
	filepath.Join("internal", "grouping", "testdata", "envelopes"),
}

func run() error {
	for _, dir := range outDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating output dir %s: %w", dir, err)
		}
	}

	if err := capture("sentry-go-exception.envelope", func() {
		err := fmt.Errorf("outer failure: %w", errors.New("inner cause"))
		sentry.CaptureException(err)
	}); err != nil {
		return err
	}

	if err := capture("sentry-go-message.envelope", func() {
		sentry.CaptureMessage("hello from trackdown fixture generator")
	}); err != nil {
		return err
	}

	if err := capture("sentry-go-panic.envelope", func() {
		defer func() {
			if r := recover(); r != nil {
				sentry.CurrentHub().Recover(r)
			}
		}()
		panic("simulated panic for fixture capture")
	}); err != nil {
		return err
	}

	return nil
}

// capture starts a local HTTP server that records the first request body it
// receives, initializes a real sentry-go client pointed at that server,
// invokes emit (which should call a Capture* function exactly once), flushes
// the client, and writes the captured envelope bytes to name under every
// directory in outDirs.
func capture(name string, emit func()) error {
	var (
		mu   sync.Mutex
		body []byte
		got  bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		mu.Lock()
		if err == nil && !got {
			body = b
			got = true
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dsn := fmt.Sprintf("http://public@%s/1", srv.Listener.Addr().String())

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Transport:        sentry.NewHTTPSyncTransport(),
		AttachStacktrace: true,
	}); err != nil {
		return fmt.Errorf("sentry.Init: %w", err)
	}
	defer sentry.Flush(2 * time.Second)

	emit()
	sentry.Flush(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if !got {
		return fmt.Errorf("no envelope captured for %s", name)
	}

	for _, dir := range outDirs {
		outPath := filepath.Join(dir, name)
		if err := os.WriteFile(outPath, body, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", outPath, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", outPath, len(body))
	}
	return nil
}
