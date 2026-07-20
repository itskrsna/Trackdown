//go:build !windows

package main

import "errors"

func serviceCommand([]string) error {
	return errors.New("the 'service' subcommand is only supported on Windows -- use systemd (Linux) or launchd (macOS) instead, see docs/self-hosting.md")
}
