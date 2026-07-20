package symbolicate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolve_SyntheticSpecExample decodes a hand-built (but VLQ-valid)
// mappings string and asserts against ground truth produced independently
// by Mozilla's own "source-map" reference implementation (the library the
// wider JS ecosystem actually relies on) — not against this package's own
// reasoning about what the mappings *should* decode to. See the comment
// below for exactly how that ground truth was obtained.
//
// Ground truth (Node.js, "source-map" package v0.7):
//
//	const map = {version: 3, sources: ["foo.js"], names: ["bar"],
//	             mappings: "AAAAA,CACC;AACA,SAAUC"}
//	consumer.originalPositionFor({line: 1, column: 0}) -> {source: "foo.js", line: 1, column: 0, name: "bar"}
//	consumer.originalPositionFor({line: 1, column: 1}) -> {source: "foo.js", line: 2, column: 1, name: null}
//	consumer.originalPositionFor({line: 2, column: 0}) -> {source: "foo.js", line: 3, column: 1, name: null}
//	consumer.originalPositionFor({line: 2, column: 4}) -> {source: "foo.js", line: 3, column: 1, name: null}
//
// (source-map's public API is 1-based line / 0-based column; this package's
// Resolve is 0-based for both, so queries/expectations below are shifted by
// one line accordingly.)
func TestResolve_SyntheticSpecExample(t *testing.T) {
	sm, err := Parse([]byte(`{"version":3,"sources":["foo.js"],"names":["bar"],"mappings":"AAAAA,CACC;AACA,SAAUC"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := []struct {
		name            string
		genLine, genCol int
		want            Position
	}{
		{"line0 col0 resolves with a name", 0, 0, Position{Source: "foo.js", Line: 0, Column: 0, Name: "bar", HasName: true}},
		{"line0 col1 second segment, no name", 0, 1, Position{Source: "foo.js", Line: 1, Column: 1}},
		{"line1 col0 first segment of second line", 1, 0, Position{Source: "foo.js", Line: 2, Column: 1}},
		{"line1 col4 still covered by the same segment", 1, 4, Position{Source: "foo.js", Line: 2, Column: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sm.Resolve(tt.genLine, tt.genCol)
			if !ok {
				t.Fatal("Resolve returned ok=false, want a match")
			}
			if got != tt.want {
				t.Fatalf("Resolve(%d,%d) = %+v, want %+v", tt.genLine, tt.genCol, got, tt.want)
			}
		})
	}
}

// TestResolve_RealEsbuildFixture uses an actual sourcemap produced by
// esbuild minifying a real multi-function JS file (testdata/esbuild-example.js.map,
// testdata/esbuild-example.min.js) and checks a handful of generated
// positions against ground truth independently produced by Node's
// "source-map" library decoding the exact same file — this is the "real
// tool as conformance oracle" approach this project uses for SDK wire
// formats, applied here to the sourcemap/bundler side, per the plan.
func TestResolve_RealEsbuildFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "esbuild-example.js.map"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	sm, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// All generated code sits on line 1 (line index 0) — esbuild emitted it
	// as a single minified line. Ground truth from Node's source-map library
	// (1-based line, 0-based column in its own API):
	//   column 0   -> src.js line 1, column 0
	//   column 197 -> src.js line 18, column 4   (the "throw new Error(...)" call)
	//   column 122 -> src.js line 14, column 0   ("function checkout")
	//   column 65  -> src.js line 9, column 0    ("function applyDiscount")
	//   column 56  -> src.js line 6, column 2    ("return t}" inside computeTotal)
	//   column 87  -> src.js line 9, column 9    (applyDiscount's 2nd param)
	//   column 254 -> src.js line 23, column 0   ("module.exports")
	tests := []struct {
		name       string
		genCol     int
		wantLine   int
		wantColumn int
	}{
		{"start of file", 0, 0, 0},
		{"throw statement", 197, 17, 4},
		{"checkout function", 122, 13, 0},
		{"applyDiscount function", 65, 8, 0},
		{"return inside computeTotal", 56, 5, 2},
		{"applyDiscount second param", 87, 8, 9},
		{"module.exports", 254, 22, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sm.Resolve(0, tt.genCol)
			if !ok {
				t.Fatal("Resolve returned ok=false, want a match")
			}
			if got.Source != "src.js" {
				t.Fatalf("Source = %q, want src.js", got.Source)
			}
			if got.Line != tt.wantLine || got.Column != tt.wantColumn {
				t.Fatalf("Resolve(0,%d) = line %d col %d, want line %d col %d", tt.genCol, got.Line, got.Column, tt.wantLine, tt.wantColumn)
			}
		})
	}
}

func TestResolve_OutOfRange(t *testing.T) {
	sm, err := Parse([]byte(`{"version":3,"sources":["a.js"],"mappings":"AAAA"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := sm.Resolve(5, 0); ok {
		t.Fatal("Resolve for an out-of-range line returned ok=true, want false")
	}
	if _, ok := sm.Resolve(-1, 0); ok {
		t.Fatal("Resolve for a negative line returned ok=true, want false")
	}
}

func TestParse_RejectsUnsupportedVersion(t *testing.T) {
	_, err := Parse([]byte(`{"version":2,"mappings":""}`))
	if err == nil {
		t.Fatal("expected an error for version != 3")
	}
}

func TestParse_RejectsInvalidVLQCharacter(t *testing.T) {
	_, err := Parse([]byte(`{"version":3,"mappings":"!!!!"}`))
	if err == nil {
		t.Fatal("expected an error for an invalid VLQ character")
	}
}
