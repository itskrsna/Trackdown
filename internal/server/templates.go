package server

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/itskrsna/Trackdown/internal/protocol"
	"github.com/itskrsna/Trackdown/internal/store"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// pageNames lists every top-level page template. Each gets its OWN
// *template.Template built from layout.html + partials + that one page —
// NOT one shared tree parsed from every page file together. Go's
// html/template silently lets the last-parsed {{define "content"}} win if
// multiple page files defining the same block name are combined into a
// single tree, which would make every page render identically. Building
// one template per page avoids that trap entirely.
var pageNames = []string{"home", "setup", "issues_list", "issue_detail", "event_detail"}

var funcMap = template.FuncMap{
	"relTime": func(t time.Time) string { return humanize.Time(t) },
	"fmtTime": func(t time.Time) string { return t.Format("2006-01-02 15:04:05 MST") },
	"frameClass": func(f protocol.Frame) string {
		if f.InApp {
			return "frame-in-app"
		}
		return "frame-lib"
	},
	"statusBadgeClass": func(s store.IssueStatus) string {
		return "badge badge-" + string(s)
	},
	// sortedTagKeys gives stable rendering order for tags -- Go map
	// iteration order is randomized, which would otherwise make the tag
	// list flicker between renders and make tests non-deterministic.
	"sortedTagKeys": func(tags protocol.Tags) []string {
		keys := make([]string, 0, len(tags))
		for k := range tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	},
}

type templateSet map[string]*template.Template

func loadTemplates() (templateSet, error) {
	ts := make(templateSet, len(pageNames))
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(funcMap).ParseFS(templateFS,
			"templates/layout.html", "templates/partials/*.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parsing template %q: %w", name, err)
		}
		ts[name] = t
	}
	return ts, nil
}

// render executes the named page template into a buffer first, so a
// template execution error becomes a clean error response rather than a
// half-written 200 body -- the buffer is only flushed to w once execution
// has fully succeeded.
func (ts templateSet) render(w http.ResponseWriter, status int, name string, data interface{}) {
	t, ok := ts[name]
	if !ok {
		http.Error(w, "unknown template "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
