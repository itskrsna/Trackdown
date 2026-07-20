package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/itskrsna/Trackdown/internal/protocol"
)

// realEsbuildSourceMap is the exact "mappings" produced by esbuild
// minifying a real multi-function JS file (see
// internal/symbolicate/testdata/esbuild-example.js.map, which this is
// copied from — sourcesContent is dropped here since it isn't needed for
// resolution). Generated column 197 on line 1 is independently verified
// (via Node's "source-map" reference library, see
// internal/symbolicate/sourcemap_test.go) to resolve to src.js line 18
// (1-based), column 4 (0-based) — the exact "throw new Error(...)" call
// this test's synthetic event points a frame at.
const realEsbuildSourceMap = `{"version":3,"sources":["src.js"],"mappings":"AAAA,SAAS,aAAaA,EAAY,CAChC,IAAIC,EAAQ,EACZ,UAAWC,KAASF,EAClBC,EAAQA,EAAQC,EAElB,OAAOD,CACT,CAEA,SAAS,cAAcA,EAAOE,EAAiB,CAC7C,MAAMC,EAAWH,GAASE,EAAkB,KAC5C,OAAOF,EAAQG,CACjB,CAEA,SAAS,SAASJ,EAAYG,EAAiB,CAC7C,MAAMF,EAAQ,aAAaD,CAAU,EAC/BK,EAAa,cAAcJ,EAAOE,CAAe,EACvD,GAAIE,EAAa,EACf,MAAM,IAAI,MAAM,8BAA8B,EAEhD,OAAOA,CACT,CAEA,OAAO,QAAU,CAAE,aAAc,cAAe,QAAS","names":["itemPrices","total","price","discountPercent","discount","finalTotal"]}`

// TestHandleEventDetail_SymbolicatesMinifiedFrame is the full integration
// test the plan called for: upload a real bundler-produced sourcemap as an
// artifact, ingest a synthetic exception event whose frame points at the
// minified position by abs_path + release, and confirm the rendered
// event-detail page shows the resolved *original* (non-minified) location
// rather than the raw minified one.
func TestHandleEventDetail_SymbolicatesMinifiedFrame(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	releaseID, err := st.CreateRelease(ctx, "proj1", "v1.0.0")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	absPath := "https://example.com/static/out.min.js"
	if err := st.UploadArtifact(ctx, releaseID, absPath, []byte(realEsbuildSourceMap)); err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}

	ev := &protocol.Event{
		EventID:  "ev-symbolicated",
		Level:    "error",
		Platform: "node",
		Release:  "v1.0.0",
		Exception: protocol.ExceptionValues{{
			Type:  "Error",
			Value: "checkout total went negative",
			Stacktrace: &protocol.Stacktrace{
				Frames: []protocol.Frame{
					// 1-based line/column, matching real JS stack traces --
					// generated column 197 (0-based) is column 198 (1-based).
					{Filename: "out.min.js", AbsPath: absPath, Lineno: 1, Colno: 198, InApp: true},
				},
			},
		}},
	}
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, mustMarshalEvent(t, ev), "fp-symbolicated", "checkout failed"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/events/ev-symbolicated")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "symbolicated") {
		t.Fatalf("expected a 'symbolicated' badge in body, got: %s", body)
	}
	if !strings.Contains(body, "src.js:18:4") {
		t.Fatalf("expected the resolved original location src.js:18:4 in body, got: %s", body)
	}
}

// TestHandleEventDetail_NoArtifactUploaded_RendersRawFrameWithoutError is
// the common case: an event referencing a release/abs_path with no
// uploaded sourcemap must render its raw minified frame, not error or 500 —
// symbolication being unavailable is normal, not exceptional.
func TestHandleEventDetail_NoArtifactUploaded_RendersRawFrameWithoutError(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	ev := &protocol.Event{
		EventID:  "ev-no-map",
		Level:    "error",
		Platform: "node",
		Release:  "v1.0.0", // no release/artifact was ever uploaded
		Exception: protocol.ExceptionValues{{
			Type:  "Error",
			Value: "boom",
			Stacktrace: &protocol.Stacktrace{
				Frames: []protocol.Frame{
					{Filename: "out.min.js", AbsPath: "https://example.com/static/out.min.js", Lineno: 1, Colno: 198, InApp: true},
				},
			},
		}},
	}
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, mustMarshalEvent(t, ev), "fp-no-map", "boom"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/events/ev-no-map")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(body, "badge-symbolicated") {
		t.Fatalf("expected no symbolication badge when no artifact was uploaded, got: %s", body)
	}
	if !strings.Contains(body, "out.min.js:1:198") {
		t.Fatalf("expected the raw minified frame location, got: %s", body)
	}
}
