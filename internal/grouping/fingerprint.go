// Package grouping computes a stable fingerprint for an event, so repeated
// occurrences of "the same" underlying bug collapse into one issue instead
// of flooding storage with one row per crash. This is Trackdown's own
// grouping algorithm — not a port of Sentry's (which is considerably more
// elaborate) — tuned for the common case: group by the outermost
// exception's type and its in-app call path, falling back to the message
// text when there's no stacktrace to anchor on.
package grouping

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/itskrsna/Trackdown/internal/protocol"
)

// Fingerprint computes a stable identifier for the issue an event belongs
// to. Two events with the same Fingerprint are the same issue.
func Fingerprint(ev *protocol.Event) string {
	parts := components(ev)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Title returns a human-readable summary of an event, used as its issue's
// display title. It follows the same exception-then-message precedence as
// Fingerprint, so the title always describes what was actually grouped on.
func Title(ev *protocol.Event) string {
	if len(ev.Exception) > 0 {
		primary := ev.Exception[len(ev.Exception)-1]
		if primary.Value != "" {
			return primary.Type + ": " + primary.Value
		}
		if primary.Type != "" {
			return primary.Type
		}
	}
	if msg := ev.Message.String(); msg != "" {
		return msg
	}
	return "(no message)"
}

// components builds the ordered list of normalized signals that determine
// an event's fingerprint. Kept separate from Fingerprint so grouping
// decisions are directly unit-testable, independent of the hash.
func components(ev *protocol.Event) []string {
	if len(ev.Exception) > 0 {
		// sentry-go (and the wider chained-error convention) appends the
		// root cause first and the outermost/most-recognizable error last —
		// verified against a real captured fixture in fingerprint_test.go.
		// The outermost exception is what a user actually sees, so it's the
		// most stable grouping anchor; inner causes vary more across calls.
		primary := ev.Exception[len(ev.Exception)-1]
		parts := []string{"exception", primary.Type}
		if primary.Stacktrace != nil {
			parts = append(parts, frameComponents(primary.Stacktrace.Frames)...)
		}
		return parts
	}

	if frames := currentThreadFrames(ev.Threads); frames != nil {
		parts := []string{"message", ev.Message.String()}
		return append(parts, frameComponents(frames)...)
	}

	return []string{"message", ev.Message.String()}
}

// currentThreadFrames returns the stacktrace of the thread marked "current"
// (the one that was executing when the event was captured), falling back to
// the first thread with a stacktrace if none is marked current.
func currentThreadFrames(threads []protocol.Thread) []protocol.Frame {
	for _, th := range threads {
		if th.Current && th.Stacktrace != nil {
			return th.Stacktrace.Frames
		}
	}
	for _, th := range threads {
		if th.Stacktrace != nil {
			return th.Stacktrace.Frames
		}
	}
	return nil
}

// frameComponents normalizes a stacktrace into fingerprint signals:
// prefer in-app frames only (library/runtime frames are noise shared by
// unrelated bugs), reduce each frame to (module, function) — dropping
// line/column numbers, which shift across otherwise-identical builds — and
// collapse consecutive duplicate frames so recursion depth doesn't change
// the fingerprint.
func frameComponents(frames []protocol.Frame) []string {
	target := frames
	if inApp := filterInApp(frames); len(inApp) > 0 {
		target = inApp
	}

	out := make([]string, 0, len(target)*2)
	var lastModule, lastFunction string
	first := true
	for _, f := range target {
		if !first && f.Module == lastModule && f.Function == lastFunction {
			continue // recursive call at the same site — already represented
		}
		out = append(out, f.Module, f.Function)
		lastModule, lastFunction = f.Module, f.Function
		first = false
	}
	return out
}

func filterInApp(frames []protocol.Frame) []protocol.Frame {
	var out []protocol.Frame
	for _, f := range frames {
		if f.InApp {
			out = append(out, f)
		}
	}
	return out
}
