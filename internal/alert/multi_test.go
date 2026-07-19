package alert

import (
	"context"
	"errors"
	"testing"
)

type recordingNotifier struct {
	called bool
	err    error
}

func (r *recordingNotifier) Notify(_ context.Context, _ NotifyEvent) error {
	r.called = true
	return r.err
}

func TestMultiNotifier_CallsAll(t *testing.T) {
	a := &recordingNotifier{}
	b := &recordingNotifier{}
	m := MultiNotifier{a, b}

	if err := m.Notify(context.Background(), NotifyEvent{}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !a.called || !b.called {
		t.Fatalf("expected both notifiers called: a=%v b=%v", a.called, b.called)
	}
}

func TestMultiNotifier_OneFailing_StillCallsOthersAndReturnsError(t *testing.T) {
	failing := &recordingNotifier{err: errors.New("smtp unreachable")}
	working := &recordingNotifier{}
	m := MultiNotifier{failing, working}

	err := m.Notify(context.Background(), NotifyEvent{})
	if err == nil {
		t.Fatal("expected a combined error since one notifier failed")
	}
	if !working.called {
		t.Fatal("a failing notifier must not prevent the others from being called")
	}
	if !errors.Is(err, failing.err) {
		t.Fatalf("expected the combined error to wrap the underlying failure, got: %v", err)
	}
}

func TestMultiNotifier_Empty_NoError(t *testing.T) {
	var m MultiNotifier
	if err := m.Notify(context.Background(), NotifyEvent{}); err != nil {
		t.Fatalf("Notify on empty MultiNotifier: %v", err)
	}
}
