// Package server implements Trackdown's embedded web UI: a plain
// server-rendered html/template dashboard, no JS framework, no build step,
// no external assets — the templates and stylesheet ship inside the binary
// via go:embed, in keeping with the single-binary, zero-dependency
// self-hosting story the rest of the project follows.
package server

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/itskrsna/Trackdown/internal/store"
)

// Server serves the web UI. Construct with New, then call Register on
// whatever mux it should be wired into (typically wrapped in an auth
// middleware by the caller — Server itself has no opinion on auth).
type Server struct {
	Store     *store.Store
	templates templateSet
	static    http.Handler
}

// New builds a Server, parsing all embedded templates up front so a
// malformed template fails loudly at startup, not silently on some later
// request.
func New(st *store.Store) (*Server, error) {
	ts, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("preparing static assets: %w", err)
	}
	return &Server{
		Store:     st,
		templates: ts,
		static:    http.StripPrefix("/static/", http.FileServerFS(staticSub)),
	}, nil
}

// Register attaches the web UI's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /projects/{project_id}", s.handleProjectIndex)
	mux.HandleFunc("GET /projects/{project_id}/setup", s.handleProjectSetup)
	mux.HandleFunc("GET /projects/{project_id}/issues/", s.handleIssueList)
	mux.HandleFunc("GET /projects/{project_id}/issues/{issue_id}", s.handleIssueDetail)
	mux.HandleFunc("POST /projects/{project_id}/issues/{issue_id}/resolve", s.handleIssueAction(store.StatusResolved))
	mux.HandleFunc("POST /projects/{project_id}/issues/{issue_id}/ignore", s.handleIssueAction(store.StatusIgnored))
	mux.HandleFunc("POST /projects/{project_id}/issues/{issue_id}/reopen", s.handleIssueAction(store.StatusUnresolved))
	mux.HandleFunc("GET /projects/{project_id}/events/{event_id}", s.handleEventDetail)
	mux.Handle("GET /static/", s.static)
}
