package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itskrsna/Trackdown/internal/protocol"
	"github.com/itskrsna/Trackdown/internal/store"
)

func TestLoadTemplates_NoParseError(t *testing.T) {
	ts, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	for _, name := range pageNames {
		if ts[name] == nil {
			t.Fatalf("template %q was not loaded", name)
		}
	}
}

// newTestServer builds a Server backed by a fresh in-memory store and
// returns an httptest.Server serving its routes, plus the Store for direct
// seeding. Test data is hand-built protocol.Event values rather than
// envelope fixtures, deliberately decoupling UI tests from the wire format.
func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv, err := New(st)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	mux := http.NewServeMux()
	srv.Register(mux)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return httpSrv, st
}

func mustGetBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestHandleHome_EmptyState(t *testing.T) {
	srv, _ := newTestServer(t)
	status, body := mustGetBody(t, srv.URL+"/")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "No projects yet") {
		t.Fatalf("expected empty-state message, got: %s", body)
	}
}

func TestHandleHome_ListsProjects(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "proj1") {
		t.Fatalf("expected project ID in body, got: %s", body)
	}
	if !strings.Contains(body, `/projects/proj1/issues/`) {
		t.Fatalf("expected a link to the project's issue list, got: %s", body)
	}
}

func TestHandleProjectSetup_RendersDSN(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "mykey"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/setup")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	// DSN shape verified elsewhere (internal/ingest e2e tests) against real
	// SDK clients: scheme://public_key@host/project_id.
	if !strings.Contains(body, "mykey@") || !strings.Contains(body, "/proj1") {
		t.Fatalf("expected the DSN mykey@...:port/proj1 in body, got: %s", body)
	}
}

func TestHandleProjectSetup_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	status, _ := mustGetBody(t, srv.URL+"/projects/does-not-exist/setup")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestHandleProjectIndex_RedirectsToIssues(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/projects/proj1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/projects/proj1/issues/" {
		t.Fatalf("Location = %q, want /projects/proj1/issues/", loc)
	}
}

// buildExceptionEvent constructs a realistic hand-built event with a
// chained exception (in-app + library frames) for UI tests to seed.
func buildExceptionEvent(eventID, exceptionValue, level string) *protocol.Event {
	ev := &protocol.Event{
		EventID:  eventID,
		Level:    level,
		Platform: "go",
		Exception: protocol.ExceptionValues{{
			Type:  "*errors.errorString",
			Value: exceptionValue,
			Stacktrace: &protocol.Stacktrace{
				Frames: []protocol.Frame{
					{Module: "net/http", Function: "serverHandler.ServeHTTP", Filename: "server.go", Lineno: 100, InApp: false},
					{Module: "main", Function: "handler", Filename: "main.go", Lineno: 42, InApp: true, ContextLine: "return doSomething()"},
				},
			},
		}},
	}
	ev.Timestamp.Time = time.Now()
	return ev
}

func TestHandleIssueList_DefaultsToUnresolvedAndFilters(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	unresolvedEv := buildExceptionEvent("ev1", "still broken", "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", unresolvedEv, []byte(`{}`), "fp-unresolved", "Unresolved Bug"); err != nil {
		t.Fatalf("SaveEvent unresolved: %v", err)
	}

	resolvedEv := buildExceptionEvent("ev2", "fixed now", "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", resolvedEv, []byte(`{}`), "fp-resolved", "Resolved Bug"); err != nil {
		t.Fatalf("SaveEvent resolved: %v", err)
	}
	events, err := st.ListEvents(ctx, "proj1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var resolvedIssueID int64
	for _, e := range events {
		if e.EventID == "ev2" {
			resolvedIssueID = e.IssueID
		}
	}
	if err := st.SetIssueStatus(ctx, "proj1", resolvedIssueID, store.StatusResolved); err != nil {
		t.Fatalf("SetIssueStatus: %v", err)
	}

	// Default view (no ?status=) must show the unresolved issue and hide the resolved one.
	status, body := mustGetBody(t, srv.URL+"/projects/proj1/issues/")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "Unresolved Bug") {
		t.Fatalf("default view should show the unresolved issue, got: %s", body)
	}
	if strings.Contains(body, "Resolved Bug") {
		t.Fatalf("default view should NOT show the resolved issue, got: %s", body)
	}

	// ?status=resolved must show the resolved issue and hide the unresolved one.
	status, body = mustGetBody(t, srv.URL+"/projects/proj1/issues/?status=resolved")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "Resolved Bug") {
		t.Fatalf("resolved view should show the resolved issue, got: %s", body)
	}
	if strings.Contains(body, "Unresolved Bug") {
		t.Fatalf("resolved view should NOT show the unresolved issue, got: %s", body)
	}

	// ?status=all must show both.
	status, body = mustGetBody(t, srv.URL+"/projects/proj1/issues/?status=all")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "Unresolved Bug") || !strings.Contains(body, "Resolved Bug") {
		t.Fatalf("all view should show both issues, got: %s", body)
	}
}

func TestHandleIssueList_ProjectNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	status, _ := mustGetBody(t, srv.URL+"/projects/does-not-exist/issues/")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestHandleIssueDetail_ShowsFingerprintEventsAndActions(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev := buildExceptionEvent("ev1", "boom", "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, []byte(`{}`), "fp-abc123", "Boom Title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}
	events, _ := st.ListEvents(ctx, "proj1")
	issueID := events[0].IssueID

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/issues/"+strconv.FormatInt(issueID, 10))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "fp-abc123") {
		t.Fatalf("expected fingerprint in body, got: %s", body)
	}
	if !strings.Contains(body, "ev1") {
		t.Fatalf("expected linked event ID in body, got: %s", body)
	}
	// Unresolved issue: resolve and ignore actions should be offered, reopen should not.
	if !strings.Contains(body, `/resolve"`) {
		t.Fatalf("expected a resolve action for an unresolved issue, got: %s", body)
	}
	if !strings.Contains(body, `/ignore"`) {
		t.Fatalf("expected an ignore action for an unresolved issue, got: %s", body)
	}
	if strings.Contains(body, `/reopen"`) {
		t.Fatalf("did not expect a reopen action for an already-unresolved issue, got: %s", body)
	}
}

func TestHandleIssueDetail_ResolvedIssue_OffersReopenNotResolve(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev := buildExceptionEvent("ev1", "boom", "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, []byte(`{}`), "fp-abc123", "Boom Title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}
	events, _ := st.ListEvents(ctx, "proj1")
	issueID := events[0].IssueID
	if err := st.SetIssueStatus(ctx, "proj1", issueID, store.StatusResolved); err != nil {
		t.Fatalf("SetIssueStatus: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/issues/"+strconv.FormatInt(issueID, 10))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(body, `/resolve"`) {
		t.Fatalf("did not expect a resolve action for an already-resolved issue, got: %s", body)
	}
	if !strings.Contains(body, `/reopen"`) {
		t.Fatalf("expected a reopen action for a resolved issue, got: %s", body)
	}
}

func TestHandleIssueDetail_NotFound(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	status, _ := mustGetBody(t, srv.URL+"/projects/proj1/issues/9999")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestHandleIssueDetail_InvalidID(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	status, _ := mustGetBody(t, srv.URL+"/projects/proj1/issues/not-a-number")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestHandleIssueAction_Resolve_RedirectsAndUpdatesStatus(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev := buildExceptionEvent("ev1", "boom", "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, []byte(`{}`), "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}
	events, _ := st.ListEvents(ctx, "proj1")
	issueID := events[0].IssueID

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Post(srv.URL+"/projects/proj1/issues/"+strconv.FormatInt(issueID, 10)+"/resolve", "", nil)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	issue, err := st.GetIssue(ctx, "proj1", issueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != store.StatusResolved {
		t.Fatalf("Status = %q, want resolved", issue.Status)
	}
}

func TestHandleIssueAction_UnknownIssue_Returns404(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	resp, err := http.Post(srv.URL+"/projects/proj1/issues/9999/resolve", "", nil)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleEventDetail_RendersExceptionFramesAndInAppDistinction(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev := buildExceptionEvent("ev1", "boom goes the dynamite", "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, mustMarshalEvent(t, ev), "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/events/ev1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "boom goes the dynamite") {
		t.Fatalf("expected exception value in body, got: %s", body)
	}
	if !strings.Contains(body, "frame-in-app") {
		t.Fatalf("expected an in-app frame class in body, got: %s", body)
	}
	if !strings.Contains(body, "frame-lib") {
		t.Fatalf("expected a library frame class in body, got: %s", body)
	}
	if !strings.Contains(body, "Raw JSON") {
		t.Fatalf("expected the raw JSON collapsible section, got: %s", body)
	}
}

func TestHandleEventDetail_NotFound(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.EnsureProject(bgCtx(), "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	status, _ := mustGetBody(t, srv.URL+"/projects/proj1/events/does-not-exist")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// TestHandleEventDetail_ExceptionValueIsHTMLEscaped is a real regression
// test, not a hypothetical: exception messages are attacker/user-influenced
// text (whatever error a client-side app throws) flowing through
// html/template. html/template auto-escapes by default, but this proves it,
// rather than just assuming it.
func TestHandleEventDetail_ExceptionValueIsHTMLEscaped(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := bgCtx()
	if err := st.EnsureProject(ctx, "proj1", "key1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	ev := buildExceptionEvent("ev1", `<script>alert(1)</script>`, "error")
	if _, _, _, err := st.SaveEvent(ctx, "proj1", ev, mustMarshalEvent(t, ev), "fp1", "title"); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	status, body := mustGetBody(t, srv.URL+"/projects/proj1/events/ev1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("exception value was rendered UNESCAPED -- XSS risk. Body: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected the exception value HTML-escaped, got: %s", body)
	}
}

func TestStaticAssets_ServeStylesheet(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/static/style.css")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "prefers-color-scheme") {
		t.Fatalf("expected the dark-mode media query in the served stylesheet")
	}
}

func bgCtx() context.Context { return context.Background() }

func mustMarshalEvent(t *testing.T, ev *protocol.Event) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshaling event: %v", err)
	}
	return b
}
