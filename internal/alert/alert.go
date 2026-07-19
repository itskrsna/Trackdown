// Package alert notifies operators about issue lifecycle events — new
// issues and regressions — via SMTP email and generic webhooks. No
// proprietary vendor APIs: both are standard, self-hostable protocols, in
// keeping with the project's zero-proprietary-vendor rule.
package alert

import (
	"context"
	"errors"
	"time"
)

// NotifyEvent describes an issue-lifecycle event worth notifying about.
type NotifyEvent struct {
	ProjectID    string
	IssueID      int64
	Title        string
	Level        string
	IsNew        bool
	IsRegression bool
	EventCount   int64
	OccurredAt   time.Time
}

// Notifier delivers a NotifyEvent somewhere.
type Notifier interface {
	Notify(ctx context.Context, ev NotifyEvent) error
}

// MultiNotifier fans a NotifyEvent out to every Notifier in the slice,
// always attempting all of them regardless of individual failures (one
// down notifier — e.g. an unreachable SMTP server — must not prevent a
// working webhook from firing). Errors are combined via errors.Join.
type MultiNotifier []Notifier

func (m MultiNotifier) Notify(ctx context.Context, ev NotifyEvent) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, ev); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
