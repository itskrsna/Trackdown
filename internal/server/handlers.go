package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/itskrsna/Trackdown/internal/protocol"
	"github.com/itskrsna/Trackdown/internal/store"
)

// handleHome lists every project. There's no "create project" form here —
// projects auto-provision the instant their DSN is used by ingest, so
// there's genuinely nothing to create through the UI.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.ListProjects(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.templates.render(w, http.StatusOK, "home", homePage{Projects: projects})
}

// handleProjectIndex redirects to the issue list — a project has no useful
// "overview" beyond that.
func (s *Server) handleProjectIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/projects/"+r.PathValue("project_id")+"/issues/", http.StatusFound)
}

func (s *Server) handleProjectSetup(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	project, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	// This is exactly the DSN shape Trackdown's own tests and tools verify
	// SDKs actually parse: scheme://public_key@host/project_id.
	dsn := fmt.Sprintf("%s://%s@%s/%s", scheme, project.PublicKey, r.Host, project.ID)

	s.templates.render(w, http.StatusOK, "setup", setupPage{ProjectID: projectID, DSN: dsn})
}

func (s *Server) handleIssueList(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	if _, err := s.Store.GetProject(r.Context(), projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	allIssues, err := s.Store.ListIssues(r.Context(), projectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	counts := map[string]int{}
	for _, i := range allIssues {
		counts[string(i.Status)]++
	}

	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = string(store.StatusUnresolved)
	}

	var rows []issueRow
	for _, i := range allIssues {
		if statusFilter != "all" && string(i.Status) != statusFilter {
			continue
		}
		rows = append(rows, toIssueRow(i))
	}

	s.templates.render(w, http.StatusOK, "issues_list", issueListPage{
		ProjectID: projectID,
		Status:    statusFilter,
		Counts:    counts,
		Issues:    rows,
	})
}

func (s *Server) handleIssueDetail(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	issueID, err := parseIssueID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	issue, err := s.Store.GetIssue(r.Context(), projectID, issueID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "issue not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	events, err := s.Store.ListEventsByIssue(r.Context(), projectID, issueID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.templates.render(w, http.StatusOK, "issue_detail", issueDetailPage{
		ProjectID:   projectID,
		ID:          issue.ID,
		Title:       issue.Title,
		Status:      issue.Status,
		Fingerprint: issue.Fingerprint,
		EventCount:  issue.EventCount,
		FirstSeen:   issue.FirstSeen,
		LastSeen:    issue.LastSeen,
		Events:      events,
	})
}

// handleIssueAction returns a handler that transitions an issue to status —
// shared implementation for the resolve/ignore/reopen routes, mirroring
// internal/ingest's setIssueStatus. Redirects back to the issue detail page
// with 303 (not 302) so a page refresh after the POST doesn't resubmit it.
func (s *Server) handleIssueAction(status store.IssueStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("project_id")
		issueID, err := parseIssueID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Store.SetIssueStatus(r.Context(), projectID, issueID, status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "issue not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/projects/%s/issues/%d", projectID, issueID), http.StatusSeeOther)
	}
}

func (s *Server) handleEventDetail(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	eventID := r.PathValue("event_id")

	stored, err := s.Store.GetEvent(r.Context(), projectID, eventID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The event's full structured detail (exception chain, threads,
	// breadcrumbs, tags) lives only in the stored JSON payload — StoredEvent
	// carries just the summary columns used for lists.
	parsed, err := protocol.ParseEvent(stored.Payload)
	if err != nil {
		http.Error(w, "internal error: corrupt stored event", http.StatusInternalServerError)
		return
	}

	s.templates.render(w, http.StatusOK, "event_detail", eventDetailPage{
		ProjectID: projectID,
		Stored:    stored,
		Event:     parsed,
		RawJSON:   indentJSON(stored.Payload),
	})
}

func parseIssueID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("issue_id"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid issue_id: %w", err)
	}
	return id, nil
}
