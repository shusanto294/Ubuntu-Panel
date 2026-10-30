// Package web is the panel's HTTP surface: the route table, the middleware
// that guards it, and the handlers behind it.
package web

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/shusanto294/Ubuntu-Panel/internal/catalog"
	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/databases"
	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/mail"
	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
	"github.com/shusanto294/Ubuntu-Panel/internal/queue"
	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/session"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/terminal"
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

	// MCP is the Model Context Protocol server an AI assistant connects to.
	// Its tools are the same services the handlers above call, so anything an
	// assistant can do is something the panel already does.
	MCP *mcp.Server

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

	// Which accounts belong to a site, for the SSH users tab to keep its
	// hands off them.
	security.SiteHomes = app.siteHomes
	catalog.ConfigureMail = app.Mail.Configure
	catalog.ConfigurePhpMyAdmin = app.EnsurePhpMyAdminReady
	catalog.ConfigureSSHProtection = app.applySSHProtection

	app.registerJobs()
	app.registerSecurityJobs()

	app.MCP = app.buildMCP()

	app.Render.SharedProps = app.sharedProps
	app.router = app.routes()

	return app
}

// Start brings up the background workers. Called after the server is wired but
// before it listens, so a deployment interrupted by a restart resumes as soon
// as the panel is back.
func (a *App) Start(ctx context.Context) {
	a.Queue.Start(ctx)

	// A fresh machine installs what a server needs without being asked, and a
	// machine whose mail server was configured by an older panel has it written
	// again. Both go on the queue that has just started, so the panel is
	// answering while they run.
	go func() {
		a.BootstrapApps(ctx)

		// Before the helpers, because this is what takes the panel off port 80
		// — and the catch-all the helpers write can only claim that port once
		// the panel's own vhost has let go of it.
		a.EnsurePanelAddress()

		a.EnsureNginxHelpers()
		a.EnsureMailConfigured()
		a.EnsureNameserverConfigured(ctx)
		a.EnsurePhpMyAdminReady(nil)
		a.EnsureMySQLReachable(ctx)

		// After the helpers, because it rewrites vhosts and reloads nginx, and
		// the configuration those write is what nginx is being asked to take.
		a.EnsureSiteAliases(ctx)

		// Before anything else touches site files: accounts made for one site
		// used to be able to open every site.
		a.EnsureSiteIsolation(ctx)

		// Last, so a first run's install is already queued and fail2ban goes
		// in with it rather than racing it for the dpkg lock.
		a.EnsureSSHProtection(ctx)

		// After SSH protection, which may have just queued an install of its
		// own: this one waits for a quiet moment rather than racing it.
		a.EnsureDNSServer(ctx)
	}()

	// Security alerts: the clock that emails sign-ins, lockouts and bans.
	go a.runSecurityAlerts(ctx)
	go a.warmSecurity(ctx)

	// Panel updates: how the last one went, if this is the panel it installed,
	// then the check for a newer one, and the automatic update when that is on.
	go func() {
		a.reconcilePanelUpdate(ctx)
		a.runPanelUpdates(ctx)
	}()

	// The sites that back themselves up. Its own goroutine rather than part of
	// the queue, because it is a clock rather than a piece of work: it does
	// nothing but look at the time every minute and push jobs onto the queue
	// that has just started.
	go a.runBackupSchedules(ctx)

	// And the sites that follow a branch. Its own clock for the same reason:
	// it looks at the time every minute, asks the remote whether anything
	// moved, and pushes a pull onto the queue only when it has.
	go a.runPullSchedules(ctx)
}

// Stop lets the workers finish.
func (a *App) Stop() { a.Queue.Stop() }

// Handler is the whole server: the route table wrapped in the middleware every
// request passes through, outermost first.
func (a *App) Handler() http.Handler {
	var handler http.Handler = a.router.mux

	// Read outwards: a request is recovered, then logged, then either answered
	// as MCP or given a session, then given its user, then routed.
	handler = a.withUser(handler)
	handler = a.Sessions.Middleware(handler)

	// Outside the session layer on purpose: an assistant sends a bearer token
	// and no cookie, and a client polling a deployment would otherwise write a
	// session row every couple of seconds for nobody to read.
	handler = a.withMCP(handler)

	handler = a.withSecurityHeaders(handler)
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

		// Whether a newer panel is out, for the badge beside Settings. From
		// memory: the hourly check is what asks GitHub, never a page.
		props["panelUpdate"] = a.updateBadge()
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
