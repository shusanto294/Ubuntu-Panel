// Package web is the panel's HTTP surface: the route table, the middleware
// that guards it, and the handlers behind it.
package web

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// App holds what every handler needs.
type App struct {
	Config   *config.Config
	Store    *store.Store
	Sessions *session.Manager
	Render   *inertia.Renderer
	Log      *slog.Logger

	assets fs.FS
	router *router
}

// New wires the application together. assets is the filesystem holding the
// built frontend: the embedded copy in a release build, a directory on disk
// when PANEL_DEBUG is set.
func New(cfg *config.Config, st *store.Store, assets fs.FS, log *slog.Logger) *App {
	app := &App{
		Config:   cfg,
		Store:    st,
		Sessions: session.New(st.DB(), log, false),
		Render:   inertia.NewRenderer(assets, config.Name, log),
		Log:      log,
		assets:   assets,
	}

	app.Render.SharedProps = app.sharedProps
	app.router = app.routes()

	return app
}

// Handler is the whole server: the route table wrapped in the middleware every
// request passes through, outermost first.
func (a *App) Handler() http.Handler {
	var handler http.Handler = a.router.mux

	// Read outwards: a request is recovered, then logged, then given a
	// session, then given its user, then routed.
	handler = a.withUser(handler)
	handler = a.Sessions.Middleware(handler)
	handler = a.withLogging(handler)
	handler = a.withRecovery(handler)

	return handler
}

// sharedProps are written into every page. The names and shapes match what the
// existing Vue pages read, which is what lets the frontend come across
// untouched.
func (a *App) sharedProps(r *http.Request) inertia.Props {
	sess := session.FromContext(r.Context())

	props := inertia.Props{
		"panel": map[string]any{
			"name":    config.Name,
			"version": config.Version,
		},
		"ziggy": map[string]any{
			"routes":   a.router.manifest(),
			"url":      "",
			"location": r.URL.Path,
		},
	}

	if user := userFrom(r.Context()); user != nil {
		props["auth"] = map[string]any{"user": user}
	} else {
		props["auth"] = map[string]any{"user": nil}
	}

	if sess == nil {
		props["flash"] = map[string]any{}
		props["errors"] = map[string]string{}

		return props
	}

	// The four kinds the FlashMessages component knows about, always present
	// so the component does not have to guard every read. `danger` is for
	// something that was removed on purpose: red, but nothing went wrong.
	props["flash"] = map[string]any{
		"success": nullable(sess.Flashed("success")),
		"error":   nullable(sess.Flashed("error")),
		"danger":  nullable(sess.Flashed("danger")),
		"status":  nullable(sess.Flashed("status")),
	}

	props["errors"] = sess.Errors()

	return props
}

// nullable turns an empty flash into a JSON null, which is what the frontend
// checks for.
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
