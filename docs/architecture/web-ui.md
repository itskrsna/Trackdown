# Web UI

Implemented in `internal/server`. A plain server-rendered `html/template` dashboard — no JS framework, no build step, no external assets. Everything (templates, CSS) ships inside the binary via `go:embed`, consistent with the single-binary, zero-dependency self-hosting story the rest of the project follows.

## Why no framework, why (almost) no JS

This was a deliberate choice, not a default: a small self-hosted dashboard doesn't need a SPA. `html/template` gives auto-escaping (see the XSS note below), server-side rendering means no client-side state to keep in sync with the server, and zero JS means nothing to bundle, version, or audit for supply-chain risk. The one piece of "interactivity" beyond plain links and forms — the collapsible raw-JSON view on the event detail page — uses the native `<details>`/`<summary>` HTML elements, not JavaScript.

## Composing with `internal/ingest` (auth boundary)

`cmd/trackdown/main.go` mounts ingest and the web UI on **separate sub-muxes** under one root mux:

```
root := http.NewServeMux()

// ingest.Handler.Register already applies auth selectively per-route
// (envelope ingest is never wrapped; its own management endpoints always are)
(&ingest.Handler{...}).Register(root, wrapManagement)
root.HandleFunc("GET /healthz", healthzHandler(st))

// The web UI has no unauthenticated routes of its own -- wrap the whole
// sub-mux uniformly, then mount it at "/"
uiMux := http.NewServeMux()
webUI.Register(uiMux)
root.Handle("/", wrapManagement(uiMux))
```

This composes safely because the two packages' route spaces don't overlap (`/api/...` + `/healthz` vs. `/`, `/projects/...`, `/static/...`), and Go's `ServeMux` always picks the most specific registered pattern for a given path regardless of which sub-mux (or registration order) it came from.

## Template structure — the trap this avoids

Every page gets its **own** `*template.Template`, built from `layout.html` + the partials + that one page file — not one shared tree parsed from every page file together:

```go
for _, name := range pageNames {
    t, _ := template.New("layout.html").Funcs(funcMap).ParseFS(templateFS,
        "templates/layout.html", "templates/partials/*.html", "templates/"+name+".html")
    ts[name] = t
}
```

If instead every page file were parsed into one combined tree, and more than one page defines `{{define "content"}}`, `html/template` silently lets the **last-parsed** definition win — every page would render identically, with no error or warning. Building one template per page sidesteps this entirely.

`render()` (in `templates.go`) executes into a `bytes.Buffer` before writing to the response, so a template execution error becomes a clean error response rather than a half-written `200` body.

## Routes and data flow

Handler → one or two `Store` calls → a small page-scoped view-model struct (`viewmodel.go`) → `render()`. No ORM-like abstraction, no registry beyond the `templateSet` map. See `docs/api.md`'s style for the JSON API — the web UI routes aren't separately documented there since they render HTML, not JSON; read `internal/server/server.go`'s `Register` method for the authoritative route list.

Key handler quirk worth knowing: `issueListPage.Counts` is keyed by **plain `string`**, not `store.IssueStatus` — even though `store.Issue.Status` is the latter. `html/template`'s `{{index .Counts "unresolved"}}` requires an *exact* map key type match (unlike `{{eq}}`/`{{ne}}`, which handle named string types transparently against string literals), so a plain-string-keyed map sidesteps a template execution error that a `map[store.IssueStatus]int` would otherwise cause. This was found by the test suite, not anticipated in advance — worth remembering if a similar map ever needs indexing by a custom string type in a template.

## Security: escaping is verified, not assumed

Exception messages, tag values, and other event content are attacker/user-influenced text (whatever a client-side app's error message happens to be) flowing through `html/template`. Auto-escaping is `html/template`'s default behavior, but `internal/server/server_test.go`'s `TestHandleEventDetail_ExceptionValueIsHTMLEscaped` seeds an event with a literal `<script>` payload and asserts the rendered output contains the escaped `&lt;script&gt;`, not a raw tag — a real regression test, not a hypothetical one.

## What's deferred

Pagination, search/filter beyond status, bulk actions, project settings/retention config in the UI, and symbolicated (sourcemap-resolved) frame display are all explicitly out of scope for this version .
