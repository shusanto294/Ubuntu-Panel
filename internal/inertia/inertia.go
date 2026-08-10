// Package inertia implements the server half of the Inertia protocol, which is
// what lets a Go binary serve the panel's existing Vue frontend unchanged.
//
// The contract is small. A page is
//
//	{"component": "Sites/Index", "props": {...}, "url": "/sites", "version": "..."}
//
// returned as JSON when the request carries X-Inertia, and embedded in a
// data-page attribute on the mount element otherwise. Visits, partial reloads,
// form errors and redirects are all that same object plus a few headers.
//
// The one rule worth stating outright, because breaking it is how the panel
// used to dead-end: an Inertia request must never be answered with a JSON body
// that is not a page object. The client refuses anything else and shows "All
// Inertia requests must receive a valid Inertia response", with no way forward.
// Anything that is not a page — an expired session, a missing sign-in — is a
// 409 with X-Inertia-Location, which is the protocol's own way to say "leave
// the app and load this URL".
package inertia

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// Props are a page's data.
type Props map[string]any

// Page is what the client renders.
type Page struct {
	Component string `json:"component"`
	Props     Props  `json:"props"`
	URL       string `json:"url"`
	Version   string `json:"version"`
}

// Renderer turns components and props into responses.
type Renderer struct {
	log    *slog.Logger
	assets fs.FS
	title  string

	// SharedProps is called for every page and supplies the props no handler
	// should have to repeat: the signed-in user, flash messages, validation
	// errors, the route table.
	SharedProps func(r *http.Request) Props

	once    sync.Once
	tags    string
	version string
}

// NewRenderer builds a renderer over a filesystem holding the built frontend —
// the manifest at build/.vite/manifest.json and the hashed files beside it.
func NewRenderer(assets fs.FS, title string, log *slog.Logger) *Renderer {
	return &Renderer{assets: assets, title: title, log: log}
}

// Version is the asset version. The client sends it back on every visit, and a
// change means the frontend was rebuilt under a tab that is still open, so the
// next visit becomes a full page load instead of running new JavaScript
// against an old payload.
func (rn *Renderer) Version() string {
	rn.once.Do(rn.readManifest)
	return rn.version
}

// Render writes a page.
func (rn *Renderer) Render(w http.ResponseWriter, r *http.Request, component string, props Props) {
	rn.once.Do(rn.readManifest)

	merged := Props{}

	if rn.SharedProps != nil {
		for key, value := range rn.SharedProps(r) {
			merged[key] = value
		}
	}
	for key, value := range props {
		merged[key] = value
	}

	// A partial reload names the props it wants; computing the rest would be
	// work whose result is thrown away.
	if only := partialProps(r, component); only != nil {
		for key := range merged {
			if !only[key] {
				delete(merged, key)
			}
		}
	}

	url := r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	page := Page{Component: component, Props: merged, URL: url, Version: rn.Version()}

	encoded, err := json.Marshal(page)
	if err != nil {
		rn.log.Error("could not encode the page", "component", component, "error", err)
		http.Error(w, "The panel could not render this page.", http.StatusInternalServerError)
		return
	}

	if IsInertia(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Inertia", "true")
		w.Header().Set("Vary", "X-Inertia")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
		return
	}

	rn.renderShell(w, encoded)
}

// renderShell is the HTML a first, non-Inertia load gets: the mount element
// with the page payload on it, and the tags for the built frontend.
func (rn *Renderer) renderShell(w http.ResponseWriter, page []byte) {
	var out strings.Builder

	out.WriteString("<!DOCTYPE html>\n<html lang=\"en\" class=\"h-full\">\n<head>\n")
	out.WriteString("<meta charset=\"utf-8\">\n")
	out.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("<title>" + html.EscapeString(rn.title) + "</title>\n")
	out.WriteString("<link rel=\"icon\" href=\"/favicon.ico\" sizes=\"any\">\n")
	out.WriteString(rn.tags)
	out.WriteString("</head>\n<body class=\"font-sans antialiased\">\n<div id=\"app\" data-page=\"")

	// The payload is HTML-escaped into an attribute. encoding/json already
	// escaped <, > and & into \u sequences, so this is the second of two
	// independent layers; either alone would do and neither is trusted to.
	out.WriteString(html.EscapeString(string(page)))

	out.WriteString("\"></div>\n</body>\n</html>\n")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(out.String()))
}

// Redirect sends the browser somewhere else the Inertia way.
//
// 303 for anything that is not a GET. A 302 after a DELETE tells the browser
// to repeat the DELETE against the redirect target, which turns "delete this
// site, then show the list" into "delete the list".
func Redirect(w http.ResponseWriter, r *http.Request, location string) {
	status := http.StatusFound

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		status = http.StatusSeeOther
	}

	http.Redirect(w, r, location, status)
}

// Back returns to the page the request came from, which is what a form does
// when its input did not validate.
//
// Only a same-origin path is followed. A Referer is set by the client, and
// following an absolute one turns every "go back" into an open redirect.
func Back(w http.ResponseWriter, r *http.Request, fallback string) {
	Redirect(w, r, RefererPath(r, fallback))
}

// RefererPath is the path the request came from, or fallback if the header is
// absent or points off-site.
func RefererPath(r *http.Request, fallback string) string {
	referer := r.Header.Get("Referer")
	if referer == "" {
		return fallback
	}

	path := referer

	if strings.HasPrefix(referer, "http://") || strings.HasPrefix(referer, "https://") {
		rest := referer[strings.Index(referer, "//")+2:]

		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "/"
		}

		path = rest[slash:]
	}

	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return fallback
	}

	return path
}

// Location takes the browser out of the single-page app and loads a URL for
// real. This is the only correct answer to an Inertia request that cannot be
// given a page: an expired session, a sign-in that is now required, a frontend
// that has been rebuilt underneath the tab.
func Location(w http.ResponseWriter, r *http.Request, url string) {
	if !IsInertia(r) {
		// A plain browser request has no client-side router to instruct.
		Redirect(w, r, url)
		return
	}

	w.Header().Set("X-Inertia-Location", url)
	w.WriteHeader(http.StatusConflict)
}

// IsInertia says whether the request came from the frontend's router rather
// than from the address bar.
func IsInertia(r *http.Request) bool {
	return r.Header.Get("X-Inertia") != ""
}

// StaleAssets says whether the tab was loaded against an older frontend build.
func (rn *Renderer) StaleAssets(r *http.Request) bool {
	if !IsInertia(r) || r.Method != http.MethodGet {
		return false
	}

	sent := r.Header.Get("X-Inertia-Version")

	return sent != "" && sent != rn.Version()
}

// partialProps is the set the client asked for on a partial reload, or nil
// when it wants the whole page.
func partialProps(r *http.Request, component string) map[string]bool {
	only := r.Header.Get("X-Inertia-Partial-Data")

	// The header only applies to a reload of the component already on screen.
	if only == "" || r.Header.Get("X-Inertia-Partial-Component") != component {
		return nil
	}

	wanted := map[string]bool{}

	for _, name := range strings.Split(only, ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}

	if len(wanted) == 0 {
		return nil
	}

	return wanted
}

// readManifest turns Vite's manifest into the script and stylesheet tags the
// shell needs, and hashes it into the asset version.
func (rn *Renderer) readManifest() {
	// Vite 5 moved the manifest under .vite/; older versions leave it at the
	// top of the output directory. Both are read so the build tooling can move
	// without the panel needing to know.
	contents, err := fs.ReadFile(rn.assets, "build/.vite/manifest.json")
	if err != nil {
		contents, err = fs.ReadFile(rn.assets, "build/manifest.json")
	}

	if err != nil {
		rn.log.Warn("no built frontend found — run `npm run build`", "error", err)
		rn.version = "dev"
		return
	}

	digest := sha256.Sum256(contents)
	rn.version = hex.EncodeToString(digest[:])

	var manifest map[string]struct {
		File    string   `json:"file"`
		IsEntry bool     `json:"isEntry"`
		CSS     []string `json:"css"`
	}

	if err := json.Unmarshal(contents, &manifest); err != nil {
		rn.log.Error("the frontend manifest is unreadable", "error", err)
		return
	}

	var tags strings.Builder

	// Only entry chunks get a tag; everything else is pulled in by the module
	// graph. Their stylesheets have to be linked explicitly, though, or the
	// page flashes unstyled while the JavaScript loads them.
	for _, chunk := range manifest {
		if !chunk.IsEntry {
			continue
		}

		if chunk.File != "" {
			tags.WriteString(`<script type="module" src="/build/` + html.EscapeString(chunk.File) + "\"></script>\n")
		}

		for _, sheet := range chunk.CSS {
			tags.WriteString(`<link rel="stylesheet" href="/build/` + html.EscapeString(sheet) + "\">\n")
		}
	}

	rn.tags = tags.String()
}
