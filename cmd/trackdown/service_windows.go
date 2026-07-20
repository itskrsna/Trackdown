//go:build windows

// Native Windows Service Control Manager (SCM) integration. Trackdown's
// existing graceful-shutdown path (context cancellation -> srv.Shutdown with
// a bounded timeout) already does the right thing regardless of what
// triggers it; this file's only job is translating SCM control requests
// into that same cancellation, and reporting status back to the SCM as
// required. NSSM (see docs/self-hosting.md) remains a documented
// alternative for operators who want its process-supervision behavior
// (auto-restart on crash) that a bare native service doesn't provide.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sys/windows/svc"
)

// serviceName identifies this service to the SCM -- used both when running
// under it (svc.Run) and when installing/uninstalling it (svc/mgr).
const serviceName = "Trackdown"

func isWindowsService() (bool, error) {
	return svc.IsWindowsService()
}

// runAsService hands control to the Windows Service Control Manager for the
// remainder of the process's life. srv is already listening (started by the
// caller before this is invoked); serveErr receives its eventual
// ListenAndServe result. cancel triggers the same context the caller's
// background loops (retention, alert retry) already watch, so an SCM
// stop/shutdown request tears everything down exactly like an OS signal
// would in the foreground case.
func runAsService(ctx context.Context, cancel context.CancelFunc, srv *http.Server, serveErr chan error, shutdownTimeout time.Duration, logger *slog.Logger) error {
	h := &serviceHandler{
		cancel:          cancel,
		srv:             srv,
		serveErr:        serveErr,
		shutdownTimeout: shutdownTimeout,
		logger:          logger,
	}
	return svc.Run(serviceName, h)
}

type serviceHandler struct {
	cancel          context.CancelFunc
	srv             *http.Server
	serveErr        chan error
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	s <- current

	for {
		select {
		case err := <-h.serveErr:
			if err != nil && err != http.ErrServerClosed {
				h.logger.Error("server error", "error", err)
				s <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			s <- svc.Status{State: svc.Stopped}
			return false, 0

		case req := <-r:
			respond, shouldStop := nextServiceAction(req.Cmd, current)
			if respond != nil {
				current = *respond
				s <- current
			}
			if shouldStop {
				h.logger.Info("Windows service stop requested")
				s <- svc.Status{State: svc.StopPending}
				h.cancel()
				shutdownCtx, sc := context.WithTimeout(context.Background(), h.shutdownTimeout)
				if err := h.srv.Shutdown(shutdownCtx); err != nil {
					h.logger.Error("error during service shutdown", "error", err)
				}
				sc()
				s <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		}
	}
}

// nextServiceAction is the pure, unit-testable core of Execute's control
// loop: given one SCM change request and the currently-reported status, it
// decides what (if anything) to report back and whether the service should
// begin shutting down. Kept free of any real SCM/context/HTTP dependency so
// it can be tested without an actual Windows service host.
func nextServiceAction(cmd svc.Cmd, current svc.Status) (respond *svc.Status, shouldStop bool) {
	switch cmd {
	case svc.Interrogate:
		return &current, false
	case svc.Stop, svc.Shutdown:
		return nil, true
	default:
		return nil, false
	}
}
