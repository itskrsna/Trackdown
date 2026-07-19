package protocol

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SDKInfo identifies the SDK that produced an envelope or event.
type SDKInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// Event is a parsed Sentry "event" item payload (the same shape is used by
// the legacy /store/ endpoint). Only the fields needed for ingest, grouping,
// and display are modeled here; unrecognized fields are simply ignored by
// encoding/json rather than causing a parse failure, since the payload is
// intentionally extensible.
type Event struct {
	EventID     string          `json:"event_id"`
	Timestamp   Timestamp       `json:"timestamp"`
	Platform    string          `json:"platform"`
	Level       string          `json:"level"`
	Logger      string          `json:"logger"`
	Message     Message         `json:"message"`
	Exception   ExceptionValues `json:"exception"`
	Threads     []Thread        `json:"threads"`
	Breadcrumbs BreadcrumbList  `json:"breadcrumbs"`
	Tags        Tags            `json:"tags"`
	Release     string          `json:"release"`
	Environment string          `json:"environment"`
	ServerName  string          `json:"server_name"`
	SDK         *SDKInfo        `json:"sdk"`
}

// ParseEvent parses the payload of an envelope item whose Header.Type is
// "event".
func ParseEvent(payload []byte) (*Event, error) {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("parsing event payload: %w", err)
	}
	return &ev, nil
}

// Timestamp accepts both forms Sentry event payloads use in the wild: a
// numeric Unix timestamp (seconds, possibly fractional) or an RFC3339-ish
// string. Real SDKs are not consistent about which they send.
type Timestamp struct {
	time.Time
}

func (t *Timestamp) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" || s == "" {
		*t = Timestamp{}
		return nil
	}
	if s[0] != '"' {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("parsing numeric timestamp %q: %w", s, err)
		}
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		t.Time = time.Unix(sec, nsec).UTC()
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return fmt.Errorf("parsing string timestamp: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, str)
	if err != nil {
		return fmt.Errorf("parsing timestamp %q: %w", str, err)
	}
	t.Time = parsed
	return nil
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time.Format(time.RFC3339Nano))
}

// Message accepts both forms of the Sentry "message" field: a plain string,
// or an object with formatted/message/params.
type Message struct {
	Formatted string
	Raw       string
	Params    []string
}

func (m *Message) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*m = Message{}
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("parsing string-form message: %w", err)
		}
		m.Formatted = s
		return nil
	}
	var obj struct {
		Formatted string   `json:"formatted"`
		Message   string   `json:"message"`
		Params    []string `json:"params"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("parsing object-form message: %w", err)
	}
	m.Formatted = obj.Formatted
	m.Raw = obj.Message
	m.Params = obj.Params
	return nil
}

// String returns the best available human-readable form of the message.
func (m Message) String() string {
	if m.Formatted != "" {
		return m.Formatted
	}
	return m.Raw
}

// Tags accepts both forms Sentry uses for the "tags" field: an object
// ({"key": "value"}) or an array of [key, value] pairs.
type Tags map[string]string

func (t *Tags) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*t = nil
		return nil
	}
	if trimmed[0] == '{' {
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("parsing object-form tags: %w", err)
		}
		*t = m
		return nil
	}
	var pairs [][2]string
	if err := json.Unmarshal(data, &pairs); err != nil {
		return fmt.Errorf("parsing array-form tags: %w", err)
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		m[p[0]] = p[1]
	}
	*t = m
	return nil
}

// Mechanism describes how an exception was captured (e.g. handled vs.
// unhandled panic, chained-error linkage).
type Mechanism struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	Handled     *bool  `json:"handled,omitempty"`
	ExceptionID int    `json:"exception_id,omitempty"`
	ParentID    *int   `json:"parent_id,omitempty"`
}

// Frame is a single stack frame within a Stacktrace.
type Frame struct {
	Filename    string `json:"filename,omitempty"`
	AbsPath     string `json:"abs_path,omitempty"`
	Function    string `json:"function,omitempty"`
	Module      string `json:"module,omitempty"`
	Lineno      int    `json:"lineno,omitempty"`
	Colno       int    `json:"colno,omitempty"`
	ContextLine string `json:"context_line,omitempty"`
	InApp       bool   `json:"in_app,omitempty"`
}

// Stacktrace is an ordered list of frames, outermost call first (per the
// Sentry convention SDKs follow — verified against captured fixtures).
type Stacktrace struct {
	Frames []Frame `json:"frames"`
}

// Thread is one thread/goroutine's stack, used for messages and panics where
// there's no Go error value to attach a Stacktrace to via ExceptionValue —
// e.g. sentry-go's CaptureMessage and panic recovery both populate Threads
// instead of Exception, verified against captured fixtures.
type Thread struct {
	ID         string      `json:"id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Stacktrace *Stacktrace `json:"stacktrace,omitempty"`
	Crashed    bool        `json:"crashed,omitempty"`
	Current    bool        `json:"current,omitempty"`
}

// ExceptionValue is one exception in a (possibly chained) exception group.
type ExceptionValue struct {
	Type       string      `json:"type,omitempty"`
	Value      string      `json:"value,omitempty"`
	Module     string      `json:"module,omitempty"`
	Stacktrace *Stacktrace `json:"stacktrace,omitempty"`
	Mechanism  *Mechanism  `json:"mechanism,omitempty"`
}

// ExceptionValues accepts both forms of the Sentry "exception" field: a bare
// array (what sentry-go emits) or an object wrapping the array in "values"
// (the form some protocol documentation and other SDKs use).
type ExceptionValues []ExceptionValue

func (e *ExceptionValues) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*e = nil
		return nil
	}
	if trimmed[0] == '[' {
		var values []ExceptionValue
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("parsing array-form exception: %w", err)
		}
		*e = values
		return nil
	}
	var wrapped struct {
		Values []ExceptionValue `json:"values"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return fmt.Errorf("parsing object-form exception: %w", err)
	}
	*e = wrapped.Values
	return nil
}

// Breadcrumb is one entry in an event's breadcrumb trail.
type Breadcrumb struct {
	Type      string                 `json:"type,omitempty"`
	Category  string                 `json:"category,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Level     string                 `json:"level,omitempty"`
	Timestamp Timestamp              `json:"timestamp,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// BreadcrumbList accepts both forms of the Sentry "breadcrumbs" field: a
// bare array (what sentry-go emits) or an object wrapping the array in
// "values".
type BreadcrumbList []Breadcrumb

func (b *BreadcrumbList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*b = nil
		return nil
	}
	if trimmed[0] == '[' {
		var values []Breadcrumb
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("parsing array-form breadcrumbs: %w", err)
		}
		*b = values
		return nil
	}
	var wrapped struct {
		Values []Breadcrumb `json:"values"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return fmt.Errorf("parsing object-form breadcrumbs: %w", err)
	}
	*b = wrapped.Values
	return nil
}
