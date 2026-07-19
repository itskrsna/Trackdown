// Command e2echeck is a throwaway verification tool (not part of the test
// suite) that sends a real sentry-go event to an already-running Trackdown
// server and prints the result, for manual end-to-end verification against
// the actual compiled binary rather than an in-process test server.
package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: e2echeck <server-host:port>")
		os.Exit(2)
	}
	host := os.Args[1]

	dsn := fmt.Sprintf("http://public@%s/manualcheck", host)
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Transport:        sentry.NewHTTPSyncTransport(),
		AttachStacktrace: true,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "sentry.Init:", err)
		os.Exit(1)
	}
	defer sentry.Flush(2 * time.Second)

	// Send the identical error twice — this checks grouping collapses two
	// occurrences of "the same bug" into one issue, not just that ingest
	// works — then a distinct error, which must land as its own issue.
	for i := 0; i < 2; i++ {
		err := fmt.Errorf("manual e2e check: %w", errors.New("simulated failure"))
		eventID := sentry.CaptureException(err)
		if !sentry.Flush(2 * time.Second) {
			fmt.Fprintln(os.Stderr, "flush timed out")
			os.Exit(1)
		}
		fmt.Printf("sent occurrence %d, event_id=%s to project manualcheck on %s\n", i+1, *eventID, host)
	}

	distinctErr := errors.New("a completely different problem")
	eventID := sentry.CaptureException(distinctErr)
	if !sentry.Flush(2 * time.Second) {
		fmt.Fprintln(os.Stderr, "flush timed out")
		os.Exit(1)
	}
	fmt.Printf("sent distinct error, event_id=%s to project manualcheck on %s\n", *eventID, host)
}
