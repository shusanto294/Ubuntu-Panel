package web

import (
	"net/http"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
)

// routes is the whole route table.
//
// Names match the C panel's exactly, because they are what the Vue pages build
// every URL from through the route() helper. Keeping them identical is what
// lets the frontend come across unmodified and lets a page be ported one at a
// time rather than all at once.
func (a *App) routes() *router {
	rt := newRouter(a)

	// Assets and the root URL, which only points somewhere useful.
	rt.get("/", "home", public, a.home)
	rt.get("/build/", "", public, a.serveAssets)
	rt.get("/favicon.ico", "", public, a.serveAssets)
	rt.get("/robots.txt", "", public, a.serveAssets)
	rt.get("/up", "", public, a.health)

	// Auth
	rt.get("/login", "login", guest, a.showLogin)
	rt.post("/login", "", guest, a.login)
	rt.post("/logout", "logout", authenticated, a.logout)

	rt.get("/setup", "setup", guest, a.showSetup)
	rt.post("/setup", "", guest, a.setup)

	// This machine
	rt.get("/dashboard", "dashboard", authenticated, a.dashboard)

	// Profile
	rt.get("/profile", "profile.edit", authenticated, a.editProfile)
	rt.patch("/profile", "profile.update", authenticated, a.updateProfile)
	rt.patch("/password", "password.update", authenticated, a.updatePassword)

	// Not ported from the C panel yet. They are registered so that the
	// navigation renders and route() resolves — an unknown name makes the
	// helper throw and takes the whole page with it — and each answers with a
	// page saying so rather than a broken one.
	for _, pending := range []struct{ pattern, name, title string }{
		{"/sites", "sites.index", "Sites"},
		{"/databases", "databases.index", "Databases"},
		{"/email", "email.index", "Email"},
		{"/terminal", "terminal", "Terminal"},
		{"/apps", "services.index", "Apps"},
		{"/dns", "dns.index", "DNS"},
		{"/logs", "logs.index", "Logs"},
		{"/settings", "settings", "Settings"},
	} {
		rt.get(pending.pattern, pending.name, authenticated, a.notPorted(pending.title))
	}

	return rt
}

// home sends people to the page that is useful to them.
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	// Go's ServeMux treats "/" as a catch-all, so anything unmatched lands
	// here. Only the root itself is a redirect; the rest is a 404.
	if r.URL.Path != "/" {
		a.notFound(w, r)
		return
	}

	if userFrom(r.Context()) != nil {
		inertia.Redirect(w, r, "/dashboard")
		return
	}

	count, err := a.Store.UserCount(r.Context())
	if err != nil {
		a.Log.Error("could not count accounts", "error", err)
		inertia.Redirect(w, r, "/login")
		return
	}

	if count == 0 {
		inertia.Redirect(w, r, "/setup")
		return
	}

	inertia.Redirect(w, r, "/login")
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	if inertia.IsInertia(r) {
		inertia.Location(w, r, "/")
		return
	}

	http.Error(w, "No route matches this URL.", http.StatusNotFound)
}

// health is what the installer curls to decide the panel came up.
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"status":"ok"}`))
}

// serveAssets serves the built frontend. In a release build these come from
// the binary itself; with PANEL_DEBUG set they come from disk, so a rebuild
// shows up on the next reload.
func (a *App) serveAssets(w http.ResponseWriter, r *http.Request) {
	// Hashed filenames are content-addressed: the name changes when the bytes
	// do, so the browser can be told to keep them for a year. Everything else
	// gets revalidated.
	if strings.HasPrefix(r.URL.Path, "/build/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	http.FileServerFS(a.assets).ServeHTTP(w, r)
}

// notPorted stands in for a page the Go rewrite has not reached yet.
func (a *App) notPorted(title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.Render.Render(w, r, "Placeholder", inertia.Props{
			"title": title,
		})
	}
}
