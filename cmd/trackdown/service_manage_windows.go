//go:build windows

// `trackdown service install|uninstall|start|stop` registers Trackdown as a
// native Windows service directly, via the SCM (golang.org/x/sys/windows/svc/mgr)
// -- no external tool like NSSM required. NSSM remains documented in
// docs/self-hosting.md as an alternative for operators who specifically want
// its process-supervision behavior (auto-restart on crash), which a bare
// native service doesn't provide on its own.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func serviceCommand(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: trackdown service <install|uninstall|start|stop> [-- <serve flags>]")
	}
	switch args[0] {
	case "install":
		return serviceInstall(args[1:])
	case "uninstall":
		return serviceUninstall()
	case "start":
		return serviceStart()
	case "stop":
		return serviceStop()
	default:
		return fmt.Errorf("unknown service subcommand %q (want install, uninstall, start, or stop)", args[0])
	}
}

// serviceInstall registers the currently-running executable with the SCM,
// configured to launch as `trackdown serve <serveArgs...>` whenever the
// service starts. serveArgs are exactly the flags you'd otherwise pass to
// `trackdown serve` directly (e.g. -db, -addr, -config, -retention-days).
func serviceInstall(serveArgs []string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determining executable path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (try running as Administrator): %w", err)
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(serviceName); err == nil {
		existing.Close()
		return fmt.Errorf("service %q is already installed (uninstall it first to change its configuration)", serviceName)
	}

	svcArgs := append([]string{"serve"}, serveArgs...)
	s, err := m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName: "Trackdown",
		Description: "Trackdown error tracking server (Sentry-wire-protocol compatible)",
		StartType:   mgr.StartAutomatic,
	}, svcArgs...)
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	defer s.Close()

	fmt.Printf("service %q installed — will run: %s serve %s\n", serviceName, exePath, strings.Join(serveArgs, " "))
	fmt.Printf("start it with: trackdown service start\n")
	return nil
}

func serviceUninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (try running as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed: %w", serviceName, err)
	}
	defer s.Close()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting service: %w", err)
	}
	fmt.Printf("service %q uninstalled\n", serviceName)
	return nil
}

func serviceStart() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (try running as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed (run 'trackdown service install' first): %w", serviceName, err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("starting service: %w", err)
	}
	fmt.Printf("service %q started\n", serviceName)
	return nil
}

func serviceStop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (try running as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed: %w", serviceName, err)
	}
	defer s.Close()

	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("stopping service: %w", err)
	}
	fmt.Printf("service %q stop requested (state: %v)\n", serviceName, status.State)
	return nil
}
