//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows/svc"
)

// TestNextServiceAction covers the SCM-event -> action translation without
// needing an actual Windows service host (which a normal `go test` process
// can't provide) -- this is the part of Execute that's actually worth
// testing; the rest is direct, hard-to-fake OS/SCM plumbing.
func TestNextServiceAction(t *testing.T) {
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	tests := []struct {
		name           string
		cmd            svc.Cmd
		wantRespond    bool
		wantShouldStop bool
	}{
		{"stop requests shutdown", svc.Stop, false, true},
		{"shutdown requests shutdown", svc.Shutdown, false, true},
		{"interrogate echoes current status", svc.Interrogate, true, false},
		{"pause is not handled", svc.Pause, false, false},
		{"unknown command is not handled", svc.Cmd(9999), false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			respond, shouldStop := nextServiceAction(tt.cmd, current)
			if (respond != nil) != tt.wantRespond {
				t.Fatalf("respond != nil = %v, want %v", respond != nil, tt.wantRespond)
			}
			if respond != nil && *respond != current {
				t.Fatalf("respond = %+v, want echoed current status %+v", *respond, current)
			}
			if shouldStop != tt.wantShouldStop {
				t.Fatalf("shouldStop = %v, want %v", shouldStop, tt.wantShouldStop)
			}
		})
	}
}
