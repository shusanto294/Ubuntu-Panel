// Package web is the panel's HTTP surface: the route table, the middleware
// that guards it, and the handlers behind it.
package web

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/mail"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/terminal"
)

// App holds what every handler needs.
type App struct {
	Config   *config.Config
	Store    *store.Store
	Sessions *session.Manager
	Render   *inertia.Renderer
	Log      *slog.Logger

	// The services a handler validates against and hands work to. Handlers do
	// three things and no more: validate input, call a service, render or
	// redirect. Anything that takes longer than a request goes on the queue.
	Queue     *queue.Queue
	Catalog   *catalog.Service
	Sites     *sites.Service
	Databases *databases.Service
	Mail      *mail.Service
	Terminal  *terminal.Service

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

	app.Queue = queue.New(st, log, cfg.Workers)
	app.Catalog = catalog.New(st)
	app.Databases = databases.New(st)
	app.Sites = sites.New(st, app.Databases)
	app.Mail = mail.New(st)
	app.Terminal = terminal.New(st, log, cfg.SystemUser)

	// Two services need each other and one of them would be an import cycle,
	// so the connection is made here: the catalogue's nginx step writes the
	// shared configuration the sites package owns, and its mail step
	// configures the mail server once the packages are down.
	catalog.SharedNginxConfig = app.Sites.WriteSharedConfig
	catalog.ConfigureMail = app.Mail.Configure

	app.registerJobs()

	app.Render.SharedProps = app.sharedProps
	app.router = app.routes()

	return app
}

// Start brings up the background workers. Called after the server is wired but
// before it listens, so a deployment interrupted by a restart resumes as soon
// as the panel is back.
func (a *App) Start(ctx context.Context) {
	a.Queue.Start(ctx)

	// A fresh machine installs what a server needs without being asked. The
	// work goes on the queue that has just started, so the panel is answering
	// while it runs.
	go a.BootstrapApps(ctx)
}

// Stop lets the workers finish.
func (a *App) Stop() { a.Queue.Stop() }

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
