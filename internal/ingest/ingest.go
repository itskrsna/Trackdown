// Package ingest serves Sentry's envelope ingest endpoint: the HTTP surface
// that lets any existing Sentry SDK deliver events to Trackdown by changing
// only its DSN.
package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/itskrsna/Trackdown/internal/alert"
	"github.com/itskrsna/Trackdown/internal/grouping"
	"github.com/itskrsna/Trackdown/internal/protocol"
	"github.com/itskrsna/Trackdown/internal/store"
)

// notifyTimeout bounds how long an alert delivery (SMTP/webhook) is allowed
// to take, run in the background after a save -- a slow or unreachable
// notifier must never block or fail the SDK's ingest request.
const notifyTimeout = 10 * time.Second

// maxEnvelopeSize bounds how much of a request body is read (compressed, if
// Content-Encoding is set — see readEnvelopeBody). It's a generous ceiling
// for now — real rate limiting and backpressure are a later
// production-hardening milestone, not a v1 concern.
const maxEnvelopeSize = 20 << 20 // 20 MiB

// maxDecompressedEnvelopeSize bounds decompressed output size independently
// of the compressed input cap above, as a defense against decompression
// bombs (a small gzip payload that expands to gigabytes).
const maxDecompressedEnvelopeSize = 100 << 20 // 100 MiB

// authKeyPattern extracts sentry_key from an X-Sentry-Auth header value,
// e.g. "Sentry sentry_version=7, sentry_client=sentry.go/0.48.0, sentry_key=public"
// — the exact shape sentry-go's HTTP transport sends (verified against its
// source, not just the protocol docs).
var authKeyPattern = regexp.MustCompile(`sentry_key=([^,\s]+)`)

// RateLimiter is satisfied by internal/ratelimit.Limiter. Declared here as a
// small interface (rather than importing internal/ratelimit directly) so
// Handler has no compile-time dependency on it — ingest works standalone,
// unthrottled, until a caller sets RateLimiter.
type RateLimiter interface {
	Allow(key string) bool
}

// Handler serves Trackdown's ingest and event-inspection endpoints.
type Handler struct {
	Store *store.Store
	// Logger receives structured logs for server-side failures — nil-safe:
	// falls back to slog.Default() so existing callers (including tests)
	// that don't set it keep working unchanged.
	Logger *slog.Logger
	// RateLimiter, if set, throttles envelope ingest per client IP. Ingest
	// is deliberately unauthenticated (DSN keys aren't secrets), so without
	// this an unthrottled flood to a guessed/leaked project ID is a trivial
	// DoS vector against the single serialized store connection.
	RateLimiter RateLimiter
	// Notifier, if set, is called (in a background goroutine, never
	// blocking the ingest response) whenever a saved event creates a new
	// issue or regresses a resolved one.
	Notifier alert.Notifier
}

func (h *Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// notifyAsync delivers a notification in the background with a bounded
// timeout, using a fresh context (not the request's, which is canceled the
// moment the HTTP response is written) — best-effort, no retry queue: a
// slow or unreachable notifier must never block or fail the SDK's ingest
// request. Delivery failures are logged, not surfaced to the SDK.
func (h *Handler) notifyAsync(ev alert.NotifyEvent) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		if err := h.Notifier.Notify(ctx, ev); err != nil {
			h.logger().Error("alert delivery failed", "error", err, "project_id", ev.ProjectID, "issue_id", ev.IssueID)
		}
	}()
}

// serverError logs the real error server-side (so failures are actually
// debuggable — a bare 500 with nothing printed anywhere is not) and writes a
// generic message to the client, never leaking internal details.
func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	h.logger().Error(msg, "error", err, "path", r.URL.Path, "method", r.Method)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// Register attaches Handler's routes to mux. Envelope ingest is never
// wrapped by wrapManagement — DSN keys are meant to be public-ish (matching
// real Sentry semantics), so SDKs must keep working unauthenticated.
// Everything else (the management/inspection API) is wrapped by it, since
// those endpoints can list and mutate all of a project's data. Pass nil to
// leave the management API unwrapped (identity) — used by tests and by
// -insecure-no-auth.
func (h *Handler) Register(mux *http.ServeMux, wrapManagement func(http.Handler) http.Handler) {
	mux.HandleFunc("POST /api/{project_id}/envelope/", h.ServeEnvelope)

	if wrapManagement == nil {
		wrapManagement = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("GET /api/{project_id}/events/", wrapManagement(http.HandlerFunc(h.ListEvents)))
	mux.Handle("GET /api/{project_id}/events/{event_id}", wrapManagement(http.HandlerFunc(h.GetEvent)))
	mux.Handle("GET /api/{project_id}/issues/", wrapManagement(http.HandlerFunc(h.ListIssues)))
	mux.Handle("GET /api/{project_id}/issues/{issue_id}", wrapManagement(http.HandlerFunc(h.GetIssue)))
	mux.Handle("GET /api/{project_id}/issues/{issue_id}/events", wrapManagement(http.HandlerFunc(h.ListIssueEvents)))
	mux.Handle("POST /api/{project_id}/issues/{issue_id}/resolve", wrapManagement(h.setIssueStatus(store.StatusResolved)))
	mux.Handle("POST /api/{project_id}/issues/{issue_id}/ignore", wrapManagement(h.setIssueStatus(store.StatusIgnored)))
	mux.Handle("POST /api/{project_id}/issues/{issue_id}/reopen", wrapManagement(h.setIssueStatus(store.StatusUnresolved)))
}

// ServeEnvelope accepts a Sentry envelope for a project, extracting and
// storing every "event" item it contains. Other item types (attachments,
// transactions, client reports, ...) are accepted but not yet persisted —
// that's a later milestone, not a reason to reject the whole envelope.
func (h *Handler) ServeEnvelope(w http.ResponseWriter, r *http.Request) {
	if h.RateLimiter != nil && !h.RateLimiter.Allow(clientIP(r)) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	projectID := r.PathValue("project_id")

	publicKey, err := extractPublicKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	body, err := readEnvelopeBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	env, err := protocol.ParseEnvelope(body)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid envelope: %v", err), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	// A project becomes usable the instant its DSN is used — there's no
	// separate provisioning step in this milestone.
	if err := h.Store.EnsureProject(ctx, projectID, publicKey); err != nil {
		h.serverError(w, r, "ensuring project", err)
		return
	}

	for _, item := range env.Items {
		if item.Header.Type != "event" {
			continue
		}
		ev, err := protocol.ParseEvent(item.Payload)
		if err != nil {
			// One malformed item shouldn't fail delivery of the rest of the
			// envelope; a real Sentry server behaves the same way.
			continue
		}
		fingerprint := grouping.Fingerprint(ev)
		title := grouping.Title(ev)
		issueID, isNew, isRegression, err := h.Store.SaveEvent(ctx, projectID, ev, item.Payload, fingerprint, title)
		if err != nil {
			h.serverError(w, r, "saving event", err)
			return
		}
		if h.Notifier != nil && (isNew || isRegression) {
			// EventCount isn't among SaveEvent's return values -- fetch it
			// for an accurate alert message. This extra query only runs on
			// the already-rare new/regression path, not on every event.
			eventCount := int64(1)
			if issue, err := h.Store.GetIssue(ctx, projectID, issueID); err == nil {
				eventCount = issue.EventCount
			}
			h.notifyAsync(alert.NotifyEvent{
				ProjectID:    projectID,
				IssueID:      issueID,
				Title:        title,
				Level:        ev.Level,
				IsNew:        isNew,
				IsRegression: isRegression,
				EventCount:   eventCount,
				OccurredAt:   time.Now(),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"` + env.Header.EventID + `"}`))
}

// ListEvents returns every stored event for a project, most recent first.
// This is a minimal inspection endpoint for this milestone; a real
// management API (filtering, pagination, issue grouping) is later work.
func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	events, err := h.Store.ListEvents(r.Context(), projectID)
	if err != nil {
		h.serverError(w, r, "listing events", err)
		return
	}
	writeJSON(w, events)
}

// GetEvent returns a single stored event by its Sentry event_id.
func (h *Handler) GetEvent(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	eventID := r.PathValue("event_id")
	ev, err := h.Store.GetEvent(r.Context(), projectID, eventID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, ev)
}

// ListIssues returns every issue for a project, most recently active first.
func (h *Handler) ListIssues(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	issues, err := h.Store.ListIssues(r.Context(), projectID)
	if err != nil {
		h.serverError(w, r, "listing issues", err)
		return
	}
	writeJSON(w, issues)
}

// GetIssue returns a single issue by ID.
func (h *Handler) GetIssue(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	issueID, err := parseIssueID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	issue, err := h.Store.GetIssue(r.Context(), projectID, issueID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, issue)
}

// ListIssueEvents returns every event linked to a specific issue.
func (h *Handler) ListIssueEvents(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	issueID, err := parseIssueID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	events, err := h.Store.ListEventsByIssue(r.Context(), projectID, issueID)
	if err != nil {
		h.serverError(w, r, "listing issue events", err)
		return
	}
	writeJSON(w, events)
}

// setIssueStatus returns a handler that transitions an issue to status —
// shared implementation for the resolve/ignore/reopen endpoints, which
// differ only in the target status.
func (h *Handler) setIssueStatus(status store.IssueStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("project_id")
		issueID, err := parseIssueID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.Store.SetIssueStatus(r.Context(), projectID, issueID, status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			h.serverError(w, r, "setting issue status", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func parseIssueID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("issue_id"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid issue_id: %w", err)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// readEnvelopeBody reads and, if necessary, decompresses a request body,
// honoring Content-Encoding. sentry-sdk (Python) gzip-compresses every
// envelope by default with no size threshold (confirmed by reading its
// transport.py directly) — this is the common case for that SDK, not an
// edge case, so it must always be handled, not treated as optional.
func readEnvelopeBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxEnvelopeSize))
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}

	switch r.Header.Get("Content-Encoding") {
	case "", "identity":
		return raw, nil
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		defer gz.Close()
		decompressed, err := io.ReadAll(io.LimitReader(gz, maxDecompressedEnvelopeSize))
		if err != nil {
			return nil, fmt.Errorf("decompressing gzip body: %w", err)
		}
		return decompressed, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", r.Header.Get("Content-Encoding"))
	}
}

// clientIP extracts the host portion of RemoteAddr, falling back to the raw
// value if it isn't in host:port form.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func extractPublicKey(r *http.Request) (string, error) {
	if auth := r.Header.Get("X-Sentry-Auth"); auth != "" {
		if m := authKeyPattern.FindStringSubmatch(auth); len(m) == 2 {
			return m[1], nil
		}
	}
	// Some SDKs/transports pass the key as a query parameter instead of the
	// X-Sentry-Auth header.
	if key := r.URL.Query().Get("sentry_key"); key != "" {
		return key, nil
	}
	return "", errors.New("missing sentry_key (X-Sentry-Auth header or sentry_key query param)")
}
