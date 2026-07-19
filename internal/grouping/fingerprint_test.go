package grouping

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/itskrsna/Trackdown/internal/protocol"
)

func loadFixtureEvent(t *testing.T, name string) *protocol.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "envelopes", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	env, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	ev, err := protocol.ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	return ev
}

func TestFingerprint_Deterministic(t *testing.T) {
	ev := loadFixtureEvent(t, "sentry-go-exception.envelope")
	a := Fingerprint(ev)
	b := Fingerprint(ev)
	if a != b {
		t.Fatalf("Fingerprint is not deterministic: %q != %q", a, b)
	}
	if a == "" {
		t.Fatal("Fingerprint returned an empty string")
	}
}

func TestFingerprint_RealFixtures_AreDistinct(t *testing.T) {
	exception := Fingerprint(loadFixtureEvent(t, "sentry-go-exception.envelope"))
	message := Fingerprint(loadFixtureEvent(t, "sentry-go-message.envelope"))
	panicEv := Fingerprint(loadFixtureEvent(t, "sentry-go-panic.envelope"))

	if exception == message || exception == panicEv || message == panicEv {
		t.Fatalf("expected 3 distinct fingerprints, got exception=%s message=%s panic=%s",
			exception, message, panicEv)
	}
}

func TestFingerprint_ChainedException_OnlyOutermostMatters(t *testing.T) {
	// The real fixture chains two errors: sentry-go puts the root cause
	// first and the outermost (user-visible) error last. Fingerprinting
	// should key off the outermost one only — changing the inner cause's
	// value must not change the fingerprint, since that's exactly the kind
	// of variance (different underlying root causes, same visible failure)
	// that should still group together.
	ev := loadFixtureEvent(t, "sentry-go-exception.envelope")
	original := Fingerprint(ev)

	if len(ev.Exception) != 2 {
		t.Fatalf("fixture has %d exceptions, want 2 (chained)", len(ev.Exception))
	}
	ev.Exception[0].Value = "a completely different inner cause"
	ev.Exception[0].Type = "*some.OtherInnerType"

	changed := Fingerprint(ev)
	if original != changed {
		t.Fatalf("changing only the inner exception changed the fingerprint: %s -> %s", original, changed)
	}
}

func TestFingerprint_DifferentOutermostType_Differs(t *testing.T) {
	ev := loadFixtureEvent(t, "sentry-go-exception.envelope")
	original := Fingerprint(ev)

	outer := len(ev.Exception) - 1
	ev.Exception[outer].Type = "*completely.DifferentType"

	changed := Fingerprint(ev)
	if original == changed {
		t.Fatal("changing the outermost exception's type did not change the fingerprint")
	}
}

func TestFingerprint_LineNumberInvariant(t *testing.T) {
	ev := loadFixtureEvent(t, "sentry-go-exception.envelope")
	original := Fingerprint(ev)

	outer := len(ev.Exception) - 1
	for i := range ev.Exception[outer].Stacktrace.Frames {
		ev.Exception[outer].Stacktrace.Frames[i].Lineno += 1000
		ev.Exception[outer].Stacktrace.Frames[i].Colno += 1000
	}

	changed := Fingerprint(ev)
	if original != changed {
		t.Fatalf("changing only line/column numbers changed the fingerprint: %s -> %s", original, changed)
	}
}

func TestFingerprint_RecursionCollapsing(t *testing.T) {
	base := &protocol.Event{
		Exception: protocol.ExceptionValues{
			{
				Type: "StackOverflowError",
				Stacktrace: &protocol.Stacktrace{
					Frames: []protocol.Frame{
						{Module: "main", Function: "run", InApp: true},
						{Module: "main", Function: "recurse", InApp: true},
					},
				},
			},
		},
	}
	recursive := &protocol.Event{
		Exception: protocol.ExceptionValues{
			{
				Type: "StackOverflowError",
				Stacktrace: &protocol.Stacktrace{
					Frames: []protocol.Frame{
						{Module: "main", Function: "run", InApp: true},
						{Module: "main", Function: "recurse", InApp: true},
						{Module: "main", Function: "recurse", InApp: true},
						{Module: "main", Function: "recurse", InApp: true},
						{Module: "main", Function: "recurse", InApp: true},
					},
				},
			},
		},
	}

	baseFP := Fingerprint(base)
	recursiveFP := Fingerprint(recursive)
	if baseFP != recursiveFP {
		t.Fatalf("recursion depth changed the fingerprint: %s (depth 1) != %s (depth 4)", baseFP, recursiveFP)
	}
}

func TestFingerprint_InAppFiltering(t *testing.T) {
	withNoisyLibFrame := &protocol.Event{
		Exception: protocol.ExceptionValues{{
			Type: "ValueError",
			Stacktrace: &protocol.Stacktrace{
				Frames: []protocol.Frame{
					{Module: "somelib", Function: "internalHelper", InApp: false},
					{Module: "myapp", Function: "handler", InApp: true},
				},
			},
		}},
	}
	withDifferentNoisyLibFrame := &protocol.Event{
		Exception: protocol.ExceptionValues{{
			Type: "ValueError",
			Stacktrace: &protocol.Stacktrace{
				Frames: []protocol.Frame{
					{Module: "differentlib", Function: "differentHelper", InApp: false},
					{Module: "myapp", Function: "handler", InApp: true},
				},
			},
		}},
	}

	fp1 := Fingerprint(withNoisyLibFrame)
	fp2 := Fingerprint(withDifferentNoisyLibFrame)
	if fp1 != fp2 {
		t.Fatalf("differing non-in-app frames changed the fingerprint: %s != %s (in-app frames were identical)", fp1, fp2)
	}

	// Now vary the in-app frame — this MUST change the fingerprint.
	withDifferentInAppFrame := &protocol.Event{
		Exception: protocol.ExceptionValues{{
			Type: "ValueError",
			Stacktrace: &protocol.Stacktrace{
				Frames: []protocol.Frame{
					{Module: "somelib", Function: "internalHelper", InApp: false},
					{Module: "myapp", Function: "differentHandler", InApp: true},
				},
			},
		}},
	}
	fp3 := Fingerprint(withDifferentInAppFrame)
	if fp1 == fp3 {
		t.Fatal("changing the in-app frame did not change the fingerprint")
	}
}

func TestFingerprint_MessageOnly_NoStacktrace(t *testing.T) {
	var evA, evB protocol.Event
	evA.Message.Formatted = "database connection failed"
	evB.Message.Formatted = "database connection failed"

	if Fingerprint(&evA) != Fingerprint(&evB) {
		t.Fatal("identical messages produced different fingerprints")
	}

	var evC protocol.Event
	evC.Message.Formatted = "a totally different problem"
	if Fingerprint(&evA) == Fingerprint(&evC) {
		t.Fatal("different messages produced the same fingerprint")
	}
}

func TestFingerprint_ThreadsFallback_UsesCurrentThread(t *testing.T) {
	ev := loadFixtureEvent(t, "sentry-go-message.envelope")
	if len(ev.Exception) != 0 {
		t.Fatalf("fixture unexpectedly has %d exceptions, want 0 (message-only)", len(ev.Exception))
	}
	if len(ev.Threads) != 1 || !ev.Threads[0].Current {
		t.Fatalf("fixture Threads = %+v, want exactly 1 thread marked current", ev.Threads)
	}

	original := Fingerprint(ev)

	// Changing a frame in the (only, current) thread's stacktrace must
	// change the fingerprint — proving Threads are actually consulted, not
	// just the message text.
	ev.Threads[0].Stacktrace.Frames[0].Function = "someTotallyDifferentFunction"
	changed := Fingerprint(ev)
	if original == changed {
		t.Fatal("changing the current thread's stacktrace did not change the fingerprint")
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		name string
		ev   *protocol.Event
		want string
	}{
		{
			name: "exception with value",
			ev: &protocol.Event{Exception: protocol.ExceptionValues{
				{Type: "ValueError", Value: "bad input"},
			}},
			want: "ValueError: bad input",
		},
		{
			name: "exception with type only",
			ev: &protocol.Event{Exception: protocol.ExceptionValues{
				{Type: "PanicError"},
			}},
			want: "PanicError",
		},
		{
			name: "message only",
			ev:   func() *protocol.Event { e := &protocol.Event{}; e.Message.Formatted = "hello"; return e }(),
			want: "hello",
		},
		{
			name: "empty event",
			ev:   &protocol.Event{},
			want: "(no message)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Title(tt.ev); got != tt.want {
				t.Fatalf("Title() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTitle_RealFixtures(t *testing.T) {
	exceptionTitle := Title(loadFixtureEvent(t, "sentry-go-exception.envelope"))
	if exceptionTitle != "*fmt.wrapError: outer failure: inner cause" {
		t.Fatalf("Title = %q", exceptionTitle)
	}

	messageTitle := Title(loadFixtureEvent(t, "sentry-go-message.envelope"))
	if messageTitle != "hello from trackdown fixture generator" {
		t.Fatalf("Title = %q", messageTitle)
	}
}
