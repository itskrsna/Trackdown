// Package symbolicate resolves minified JS stack-frame positions back to
// their original source location using a Source Map v3 document — a
// from-scratch implementation (Base64-VLQ decoding of the "mappings"
// field), matching this project's convention of implementing wire/file
// formats itself rather than pulling in a third-party sourcemap library.
// It depends on nothing else in this repo, same leaf-package convention as
// internal/protocol.
package symbolicate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SourceMap is a parsed Source Map v3 document — only the fields needed to
// resolve a generated position back to an original one are modeled;
// encoding/json ignores unrecognized fields (index maps, "file",
// "sourceRoot", vendor x_ extensions, etc.) rather than erroring, since the
// format is explicitly extensible.
type SourceMap struct {
	Version        int      `json:"version"`
	Sources        []string `json:"sources"`
	SourcesContent []string `json:"sourcesContent"`
	Names          []string `json:"names"`
	Mappings       string   `json:"mappings"`

	lines [][]segment // decoded once at Parse time, not re-parsed per Resolve call
}

// segment is one decoded mapping entry. hasSource/hasName track whether the
// optional 4th/5th VLQ fields were present (a segment may have only 1 field
// — "this generated column has no useful original mapping").
type segment struct {
	genCol    int
	hasSource bool
	srcIdx    int
	srcLine   int
	srcCol    int
	hasName   bool
	nameIdx   int
}

// Position is a resolved original source location. Line and Column are
// 0-based, matching the Source Map spec's own internal encoding exactly —
// callers translating from 1-based JS stack-trace line numbers (the usual
// convention V8 and most SDKs use) must subtract 1 from the queried
// generated line before calling Resolve, and add 1 back to the result's
// Line for display. Keeping this package's convention spec-literal (rather
// than guessing at a "friendlier" 1-based convention) avoids introducing an
// off-by-one bug at the one place that actually needs to get it right.
type Position struct {
	Source  string
	Line    int
	Column  int
	Name    string
	HasName bool
}

// Parse parses a Source Map v3 JSON document and decodes its mappings once
// up front, so repeated Resolve calls don't re-parse the mappings string.
func Parse(data []byte) (*SourceMap, error) {
	var sm SourceMap
	if err := json.Unmarshal(data, &sm); err != nil {
		return nil, fmt.Errorf("parsing source map: %w", err)
	}
	if sm.Version != 3 {
		return nil, fmt.Errorf("unsupported source map version %d, want 3", sm.Version)
	}
	lines, err := decodeMappings(sm.Mappings)
	if err != nil {
		return nil, fmt.Errorf("decoding mappings: %w", err)
	}
	sm.lines = lines
	return &sm, nil
}

// Resolve finds the original source position for a generated (line, column)
// — both 0-based. Per the Source Map spec, a segment's mapping covers every
// generated column from its own column up to (but not including) the next
// segment's column on the same line, so this returns the last segment
// whose column is <= the queried column. Returns ok=false if genLine is out
// of range, the line has no segments, or the matching segment has no source
// mapping at all (a 1-field segment — "nothing useful to resolve here").
func (sm *SourceMap) Resolve(genLine, genCol int) (Position, bool) {
	if genLine < 0 || genLine >= len(sm.lines) {
		return Position{}, false
	}
	segs := sm.lines[genLine]
	if len(segs) == 0 {
		return Position{}, false
	}
	// Segments within a line are emitted in non-decreasing generated-column
	// order (a Source Map spec requirement), so a binary search for the
	// rightmost segment with genCol <= the query is valid.
	idx := sort.Search(len(segs), func(i int) bool { return segs[i].genCol > genCol }) - 1
	if idx < 0 || !segs[idx].hasSource {
		return Position{}, false
	}
	seg := segs[idx]
	pos := Position{Line: seg.srcLine, Column: seg.srcCol}
	if seg.srcIdx >= 0 && seg.srcIdx < len(sm.Sources) {
		pos.Source = sm.Sources[seg.srcIdx]
	}
	if seg.hasName && seg.nameIdx >= 0 && seg.nameIdx < len(sm.Names) {
		pos.Name = sm.Names[seg.nameIdx]
		pos.HasName = true
	}
	return pos, true
}

// decodeMappings decodes the semicolon-separated (one entry per generated
// line), comma-separated (one entry per segment) "mappings" field. Per the
// spec: the generated-column field resets to 0 at the start of each line,
// but the source-index/source-line/source-column/name-index fields are
// cumulative across the *entire* mappings string, not reset per line.
func decodeMappings(mappings string) ([][]segment, error) {
	var lines [][]segment
	srcIdx, srcLine, srcCol, nameIdx := 0, 0, 0, 0

	for _, lineStr := range strings.Split(mappings, ";") {
		genCol := 0
		var segs []segment
		if lineStr != "" {
			for _, segStr := range strings.Split(lineStr, ",") {
				if segStr == "" {
					continue
				}
				var vals [5]int
				n := 0
				rest := segStr
				for rest != "" {
					v, r, err := decodeVLQ(rest)
					if err != nil {
						return nil, fmt.Errorf("segment %q: %w", segStr, err)
					}
					if n < 5 {
						vals[n] = v
					}
					n++
					rest = r
				}
				if n != 1 && n != 4 && n != 5 {
					return nil, fmt.Errorf("segment %q has %d fields, want 1, 4, or 5", segStr, n)
				}

				genCol += vals[0]
				seg := segment{genCol: genCol}
				if n >= 4 {
					srcIdx += vals[1]
					srcLine += vals[2]
					srcCol += vals[3]
					seg.hasSource = true
					seg.srcIdx = srcIdx
					seg.srcLine = srcLine
					seg.srcCol = srcCol
				}
				if n == 5 {
					nameIdx += vals[4]
					seg.hasName = true
					seg.nameIdx = nameIdx
				}
				segs = append(segs, seg)
			}
		}
		lines = append(lines, segs)
	}
	return lines, nil
}

// base64Chars is the Source Map spec's fixed VLQ alphabet (RFC 4648 base64
// without padding).
const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var base64Decode [256]int8

func init() {
	for i := range base64Decode {
		base64Decode[i] = -1
	}
	for i := 0; i < len(base64Chars); i++ {
		base64Decode[base64Chars[i]] = int8(i)
	}
}

// decodeVLQ reads one Base64-VLQ value from the start of s, returning the
// decoded value and the unconsumed remainder. Each base64 digit contributes
// 5 bits of magnitude plus a continuation bit (0x20); the first bit of the
// final assembled value is a sign bit (1 = negative), per the Source Map
// spec's VLQ encoding (itself borrowed from the Closure Compiler).
func decodeVLQ(s string) (value int, rest string, err error) {
	result := 0
	shift := uint(0)
	for i := 0; i < len(s); i++ {
		digit := base64Decode[s[i]]
		if digit < 0 {
			return 0, "", fmt.Errorf("invalid VLQ character %q", s[i])
		}
		cont := digit & 0x20
		result += int(digit&0x1f) << shift
		if cont == 0 {
			negate := result&1 == 1
			result >>= 1
			if negate {
				result = -result
			}
			return result, s[i+1:], nil
		}
		shift += 5
	}
	return 0, "", fmt.Errorf("truncated VLQ value in %q", s)
}
