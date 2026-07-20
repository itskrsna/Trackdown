package server

import (
	"context"

	"github.com/itskrsna/Trackdown/internal/protocol"
	"github.com/itskrsna/Trackdown/internal/symbolicate"
)

// displayFrame wraps a raw protocol.Frame with symbolication results for
// rendering. Embedding Frame keeps every existing template field
// (.Module, .Function, .Filename, .Lineno, .Colno, .InApp, .ContextLine)
// working unchanged; the new fields are additive.
type displayFrame struct {
	protocol.Frame
	Symbolicated bool
	OrigFile     string
	OrigLine     int
	OrigColumn   int
	OrigFunction string
}

// displayException mirrors protocol.ExceptionValue but with Frames replaced
// by the symbolication-aware displayFrame.
type displayException struct {
	Type   string
	Value  string
	Frames []displayFrame
}

// buildDisplayExceptions resolves each frame's original source location
// where possible, and is deliberately display-only: it never errors and
// never blocks rendering on a missing or unparseable sourcemap (the common
// case for any event ingested before its release's artifacts were
// uploaded, or one with no release at all) -- a frame simply renders
// unsymbolicated in that case. It does NOT feed back into
// internal/grouping's fingerprinting; changing that too would retroactively
// alter fingerprints for already-ingested issues, out of scope for this
// version (see docs/architecture/symbolication.md).
func (s *Server) buildDisplayExceptions(ctx context.Context, projectID string, ev *protocol.Event) []displayException {
	// One sourcemap fetch+parse per distinct abs_path per render, not per
	// frame -- a stack trace commonly has several frames from the same
	// bundle file. A nil cached value means "already looked up, nothing
	// found," distinct from "not yet looked up."
	cache := make(map[string]*symbolicate.SourceMap)
	lookup := func(absPath string) *symbolicate.SourceMap {
		if sm, cached := cache[absPath]; cached {
			return sm
		}
		var sm *symbolicate.SourceMap
		if ev.Release != "" {
			if content, err := s.Store.FindArtifact(ctx, projectID, ev.Release, absPath); err == nil {
				if parsed, perr := symbolicate.Parse(content); perr == nil {
					sm = parsed
				}
			}
		}
		cache[absPath] = sm
		return sm
	}

	out := make([]displayException, 0, len(ev.Exception))
	for _, exc := range ev.Exception {
		de := displayException{Type: exc.Type, Value: exc.Value}
		if exc.Stacktrace != nil {
			de.Frames = make([]displayFrame, 0, len(exc.Stacktrace.Frames))
			for _, f := range exc.Stacktrace.Frames {
				de.Frames = append(de.Frames, resolveFrame(f, lookup))
			}
		}
		out = append(out, de)
	}
	return out
}

func resolveFrame(f protocol.Frame, lookup func(absPath string) *symbolicate.SourceMap) displayFrame {
	df := displayFrame{Frame: f}
	if f.AbsPath == "" || f.Lineno <= 0 {
		return df
	}
	sm := lookup(f.AbsPath)
	if sm == nil {
		return df
	}
	col := 0
	if f.Colno > 0 {
		col = f.Colno - 1
	}
	pos, ok := sm.Resolve(f.Lineno-1, col)
	if !ok {
		return df
	}
	df.Symbolicated = true
	df.OrigFile = pos.Source
	df.OrigLine = pos.Line + 1
	df.OrigColumn = pos.Column
	if pos.HasName {
		df.OrigFunction = pos.Name
	}
	return df
}
