package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxArtifactSize bounds how large a single uploaded sourcemap file may be —
// generous for a real-world minified-JS sourcemap (these are usually a few
// hundred KB to a few MB even for large bundles), while still bounding
// memory use per upload.
const maxArtifactSize = 20 << 20 // 20 MiB

// CreateRelease registers a release (a project + version pair) that
// artifacts can subsequently be uploaded against. Idempotent: creating an
// already-known release is not an error, since a CI pipeline re-running the
// same version (e.g. a re-deploy) is a normal occurrence.
func (h *Handler) CreateRelease(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")

	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxArtifactSize)).Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}
	if body.Version == "" {
		http.Error(w, "version is required", http.StatusBadRequest)
		return
	}

	id, err := h.Store.CreateRelease(r.Context(), projectID, body.Version)
	if err != nil {
		h.serverError(w, r, "creating release", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "version": body.Version})
}

// UploadArtifact stores a single sourcemap file against a release, keyed by
// the abs_path a stack frame's AbsPath field references (passed as a query
// parameter, since the request body is the raw sourcemap file content
// itself, not a JSON envelope — this mirrors how envelope ingest treats its
// body as opaque payload rather than wrapping it in another encoding). The
// release doesn't need a separate explicit CreateRelease call first — like
// EnsureProject on the envelope-ingest path, referencing a release here
// creates it if needed, so a CI pipeline can upload artifacts in one step
// without a prerequisite request.
func (h *Handler) UploadArtifact(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	version := r.PathValue("version")
	absPath := r.URL.Query().Get("abs_path")
	if absPath == "" {
		http.Error(w, "abs_path query parameter is required", http.StatusBadRequest)
		return
	}

	releaseID, err := h.Store.CreateRelease(r.Context(), projectID, version)
	if err != nil {
		h.serverError(w, r, "looking up release", err)
		return
	}

	content, err := io.ReadAll(io.LimitReader(r.Body, maxArtifactSize+1))
	if err != nil {
		http.Error(w, fmt.Sprintf("reading request body: %v", err), http.StatusBadRequest)
		return
	}
	if len(content) > maxArtifactSize {
		http.Error(w, "artifact too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(content) == 0 {
		http.Error(w, "request body (the sourcemap content) is empty", http.StatusBadRequest)
		return
	}

	if err := h.Store.UploadArtifact(r.Context(), releaseID, absPath, content); err != nil {
		h.serverError(w, r, "uploading artifact", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
