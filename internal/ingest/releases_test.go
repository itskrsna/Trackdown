package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestCreateRelease_ReturnsIDAndIsIdempotent(t *testing.T) {
	srv, _ := newTestServer(t)

	body := bytes.NewReader([]byte(`{"version":"v1.0.0"}`))
	resp, err := http.Post(srv.URL+"/api/proj1/releases", "application/json", body)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var first map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if first["version"] != "v1.0.0" {
		t.Fatalf("response = %+v, want version v1.0.0", first)
	}

	// Creating the same release again must not error, and must return the
	// same id (CI re-running the same version is a normal occurrence).
	resp2, err := http.Post(srv.URL+"/api/proj1/releases", "application/json", bytes.NewReader([]byte(`{"version":"v1.0.0"}`)))
	if err != nil {
		t.Fatalf("Post (second): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("status (second) = %d, want 201", resp2.StatusCode)
	}
	var second map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&second); err != nil {
		t.Fatalf("decoding second response: %v", err)
	}
	if first["id"] != second["id"] {
		t.Fatalf("ids differ (%v, %v) for the same version, want the same id", first["id"], second["id"])
	}
}

func TestCreateRelease_MissingVersion_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/api/proj1/releases", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadArtifact_ThenFindable(t *testing.T) {
	srv, st := newTestServer(t)

	resp, err := http.Post(srv.URL+"/api/proj1/releases", "application/json", bytes.NewReader([]byte(`{"version":"v1.0.0"}`)))
	if err != nil {
		t.Fatalf("Post (create release): %v", err)
	}
	resp.Body.Close()

	sourcemap := []byte(`{"version":3,"sources":["app.js"],"mappings":"AAAA"}`)
	absPath := "https://example.com/static/app.min.js"
	uploadURL := srv.URL + "/api/proj1/releases/v1.0.0/artifacts?abs_path=" + url.QueryEscape(absPath)
	uploadResp, err := http.Post(uploadURL, "application/json", bytes.NewReader(sourcemap))
	if err != nil {
		t.Fatalf("Post (upload artifact): %v", err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", uploadResp.StatusCode)
	}

	got, err := st.FindArtifact(t.Context(), "proj1", "v1.0.0", absPath)
	if err != nil {
		t.Fatalf("FindArtifact: %v", err)
	}
	if string(got) != string(sourcemap) {
		t.Fatalf("stored content = %q, want %q", got, sourcemap)
	}
}

func TestUploadArtifact_MissingAbsPath_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/api/proj1/releases/v1.0.0/artifacts", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadArtifact_EmptyBody_Returns400(t *testing.T) {
	srv, _ := newTestServer(t)
	uploadURL := srv.URL + "/api/proj1/releases/v1.0.0/artifacts?abs_path=" + url.QueryEscape("https://example.com/app.js")
	resp, err := http.Post(uploadURL, "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadArtifact_AutoCreatesReleaseIfMissing(t *testing.T) {
	// CreateRelease is idempotent and UploadArtifact calls it internally, so
	// an upload against a never-explicitly-created release still succeeds
	// -- this documents that behavior explicitly rather than leaving it
	// implicit.
	srv, st := newTestServer(t)
	uploadURL := srv.URL + "/api/proj1/releases/v9.9.9/artifacts?abs_path=" + url.QueryEscape("https://example.com/app.js")
	resp, err := http.Post(uploadURL, "application/json", bytes.NewReader([]byte("map-content")))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	got, err := st.FindArtifact(t.Context(), "proj1", "v9.9.9", "https://example.com/app.js")
	if err != nil {
		t.Fatalf("FindArtifact: %v", err)
	}
	if string(got) != "map-content" {
		t.Fatalf("content = %q, want map-content", got)
	}
}
