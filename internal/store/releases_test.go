package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestCreateRelease_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	id1, err := s.CreateRelease(ctx, "proj1", "v1.0.0")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	id2, err := s.CreateRelease(ctx, "proj1", "v1.0.0")
	if err != nil {
		t.Fatalf("CreateRelease (second call): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("CreateRelease returned different ids (%d, %d) for the same version, want the same id", id1, id2)
	}
}

func TestUploadArtifact_AndFindArtifact_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	releaseID, err := s.CreateRelease(ctx, "proj1", "v1.0.0")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	sourcemap := []byte(`{"version":3,"sources":["app.js"],"mappings":"AAAA"}`)
	if err := s.UploadArtifact(ctx, releaseID, "https://example.com/static/app.min.js", sourcemap); err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}

	got, err := s.FindArtifact(ctx, "proj1", "v1.0.0", "https://example.com/static/app.min.js")
	if err != nil {
		t.Fatalf("FindArtifact: %v", err)
	}
	if string(got) != string(sourcemap) {
		t.Fatalf("FindArtifact content = %q, want %q", got, sourcemap)
	}
}

func TestUploadArtifact_ReplacesExisting(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	releaseID, err := s.CreateRelease(ctx, "proj1", "v1.0.0")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	if err := s.UploadArtifact(ctx, releaseID, "https://example.com/app.js", []byte("old")); err != nil {
		t.Fatalf("UploadArtifact (first): %v", err)
	}
	if err := s.UploadArtifact(ctx, releaseID, "https://example.com/app.js", []byte("new")); err != nil {
		t.Fatalf("UploadArtifact (replace): %v", err)
	}

	got, err := s.FindArtifact(ctx, "proj1", "v1.0.0", "https://example.com/app.js")
	if err != nil {
		t.Fatalf("FindArtifact: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want the replaced content %q", got, "new")
	}
}

func TestFindArtifact_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	_, err := s.FindArtifact(ctx, "proj1", "v-does-not-exist", "https://example.com/app.js")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestFindArtifact_ScopedByProjectAndVersion(t *testing.T) {
	// The same abs_path uploaded for a different project or release must
	// not be found -- symbolication is scoped per (project, release), not
	// globally by path alone.
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject proj1: %v", err)
	}
	if err := s.EnsureProject(ctx, "proj2", "key2"); err != nil {
		t.Fatalf("EnsureProject proj2: %v", err)
	}
	releaseID, err := s.CreateRelease(ctx, "proj1", "v1.0.0")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if err := s.UploadArtifact(ctx, releaseID, "https://example.com/app.js", []byte("proj1 v1")); err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}

	if _, err := s.FindArtifact(ctx, "proj2", "v1.0.0", "https://example.com/app.js"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("proj2 lookup err = %v, want sql.ErrNoRows (different project)", err)
	}
	if _, err := s.FindArtifact(ctx, "proj1", "v2.0.0", "https://example.com/app.js"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("v2.0.0 lookup err = %v, want sql.ErrNoRows (different release)", err)
	}
}
