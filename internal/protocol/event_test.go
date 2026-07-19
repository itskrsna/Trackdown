package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTimestamp_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:  "unix seconds integer",
			input: `1700000000`,
			want:  time.Unix(1700000000, 0).UTC(),
		},
		{
			name:  "unix seconds fractional",
			input: `1700000000.5`,
			want:  time.Unix(1700000000, 500000000).UTC(),
		},
		{
			name:  "RFC3339 string",
			input: `"2026-07-19T05:51:16Z"`,
			want:  time.Date(2026, 7, 19, 5, 51, 16, 0, time.UTC),
		},
		{
			name:  "RFC3339 with offset and fraction",
			input: `"2026-07-19T05:51:16.3222961+05:30"`,
			want:  time.Date(2026, 7, 19, 5, 51, 16, 322296100, time.FixedZone("", 5*3600+30*60)),
		},
		{
			name:  "null",
			input: `null`,
			want:  time.Time{},
		},
		{
			name:    "garbage string",
			input:   `"not a time"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ts Timestamp
			err := json.Unmarshal([]byte(tt.input), &ts)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !ts.Time.Equal(tt.want) {
				t.Fatalf("Time = %v, want %v", ts.Time, tt.want)
			}
		})
	}
}

func TestMessage_UnmarshalJSON(t *testing.T) {
	t.Run("string form", func(t *testing.T) {
		var m Message
		if err := json.Unmarshal([]byte(`"hello world"`), &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got := m.String(); got != "hello world" {
			t.Fatalf("String() = %q, want %q", got, "hello world")
		}
	})

	t.Run("object form", func(t *testing.T) {
		var m Message
		raw := `{"formatted":"user 42 failed","message":"user %s failed","params":["42"]}`
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got := m.String(); got != "user 42 failed" {
			t.Fatalf("String() = %q, want %q", got, "user 42 failed")
		}
		if len(m.Params) != 1 || m.Params[0] != "42" {
			t.Fatalf("Params = %v", m.Params)
		}
	})

	t.Run("null", func(t *testing.T) {
		var m Message
		if err := json.Unmarshal([]byte(`null`), &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got := m.String(); got != "" {
			t.Fatalf("String() = %q, want empty", got)
		}
	})
}

func TestTags_UnmarshalJSON(t *testing.T) {
	t.Run("object form", func(t *testing.T) {
		var tags Tags
		if err := json.Unmarshal([]byte(`{"env":"prod","team":"core"}`), &tags); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if tags["env"] != "prod" || tags["team"] != "core" {
			t.Fatalf("tags = %v", tags)
		}
	})

	t.Run("array-of-pairs form", func(t *testing.T) {
		var tags Tags
		if err := json.Unmarshal([]byte(`[["env","prod"],["team","core"]]`), &tags); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if tags["env"] != "prod" || tags["team"] != "core" {
			t.Fatalf("tags = %v", tags)
		}
	})
}

func TestExceptionValues_UnmarshalJSON(t *testing.T) {
	t.Run("bare array form (sentry-go)", func(t *testing.T) {
		var ev ExceptionValues
		raw := `[{"type":"*errors.errorString","value":"boom"}]`
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(ev) != 1 || ev[0].Value != "boom" {
			t.Fatalf("ExceptionValues = %+v", ev)
		}
	})

	t.Run("wrapped values form", func(t *testing.T) {
		var ev ExceptionValues
		raw := `{"values":[{"type":"ValueError","value":"bad input"}]}`
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(ev) != 1 || ev[0].Value != "bad input" {
			t.Fatalf("ExceptionValues = %+v", ev)
		}
	})
}

func TestBreadcrumbList_UnmarshalJSON(t *testing.T) {
	t.Run("bare array form", func(t *testing.T) {
		var bc BreadcrumbList
		raw := `[{"category":"http","message":"GET /"}]`
		if err := json.Unmarshal([]byte(raw), &bc); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(bc) != 1 || bc[0].Category != "http" {
			t.Fatalf("BreadcrumbList = %+v", bc)
		}
	})

	t.Run("wrapped values form", func(t *testing.T) {
		var bc BreadcrumbList
		raw := `{"values":[{"category":"nav","message":"clicked button"}]}`
		if err := json.Unmarshal([]byte(raw), &bc); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(bc) != 1 || bc[0].Category != "nav" {
			t.Fatalf("BreadcrumbList = %+v", bc)
		}
	})
}

func TestParseEvent_RealExceptionFixture(t *testing.T) {
	data := readFixture(t, "sentry-go-exception.envelope")
	env, err := ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	ev, err := ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	if len(ev.Exception) != 2 {
		t.Fatalf("len(Exception) = %d, want 2 (chained: inner cause + outer wrap)", len(ev.Exception))
	}

	inner, outer := ev.Exception[0], ev.Exception[1]
	if inner.Value != "inner cause" {
		t.Fatalf("Exception[0].Value = %q, want %q", inner.Value, "inner cause")
	}
	if outer.Value != "outer failure: inner cause" {
		t.Fatalf("Exception[1].Value = %q, want %q", outer.Value, "outer failure: inner cause")
	}
	if outer.Stacktrace == nil || len(outer.Stacktrace.Frames) != 4 {
		t.Fatalf("Exception[1].Stacktrace = %+v, want 4 frames", outer.Stacktrace)
	}
	if outer.Mechanism == nil || outer.Mechanism.Type != "generic" {
		t.Fatalf("Exception[1].Mechanism = %+v", outer.Mechanism)
	}
	if inner.Mechanism == nil || inner.Mechanism.Source != "unwrap" {
		t.Fatalf("Exception[0].Mechanism = %+v, want source=unwrap (chained error)", inner.Mechanism)
	}
}

func TestParseEvent_RealMessageFixture(t *testing.T) {
	data := readFixture(t, "sentry-go-message.envelope")
	env, err := ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	ev, err := ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	// sentry-go's CaptureMessage puts the text in the top-level "message"
	// field (a plain string here, not the object form) — confirmed by
	// inspecting the captured fixture bytes directly.
	if got := ev.Message.String(); got != "hello from trackdown fixture generator" {
		t.Fatalf("Message.String() = %q", got)
	}
	if len(ev.Exception) != 0 {
		t.Fatalf("Exception = %+v, want none for a plain CaptureMessage", ev.Exception)
	}
	if len(ev.Threads) != 1 || ev.Threads[0].Stacktrace == nil {
		t.Fatalf("Threads = %+v, want 1 thread with a stacktrace", ev.Threads)
	}
}

func TestParseEvent_RealPanicFixture(t *testing.T) {
	data := readFixture(t, "sentry-go-panic.envelope")
	env, err := ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	ev, err := ParseEvent(env.Items[0].Payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	if ev.Level != "fatal" {
		t.Fatalf("Level = %q, want fatal", ev.Level)
	}
	if got := ev.Message.String(); got != "simulated panic for fixture capture" {
		t.Fatalf("Message.String() = %q", got)
	}
	if len(ev.Threads) != 1 || len(ev.Threads[0].Stacktrace.Frames) != 5 {
		t.Fatalf("Threads = %+v, want 1 thread with 5 frames", ev.Threads)
	}
}
