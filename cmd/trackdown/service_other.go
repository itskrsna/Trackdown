//go:build !windows

// Native Windows Service Control Manager integration only exists on
// Windows; everywhere else, "am I running as a service" is trivially
// false and there's nothing to hand control to, keeping serve()'s dispatch
// logic identical across platforms without any platform-specific branching
// in main.go itself.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

func isWindowsService() (bool, error) {
	return false, nil
}

// runAsService is unreachable on non-Windows platforms (isWindowsService
// always returns false, so main.go never calls this) -- it exists purely so
// serve() compiles identically on every OS.
func runAsService(context.Context, context.CancelFunc, *http.Server, chan error, time.Duration, *slog.Logger) error {
	return errors.New("native service integration is only supported on Windows")
}
