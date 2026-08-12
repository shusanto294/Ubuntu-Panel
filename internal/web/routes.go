package web

import (
	"net/http"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/terminal"
)

// routes is the whole route table.
//
// Names match the C panel's exactly, because they are what the Vue pages build
// every URL from through the route() helper. Keeping them identical is what
// lets the frontend come across unmodified.
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
	rt.get("/dashboard", "dashboard", authenticated, a.overview)
	rt.get("/system/metrics", "system.metrics", authenticated, a.metricsEndpoint)
	rt.get("/system/metrics/history", "system.metrics.history", authenticated, a.metricHistory)
	rt.get("/system/credentials", "system.credentials", authenticated, a.credentials)
	rt.patch("/system/settings", "system.settings", authenticated, a.updateSettings)
	rt.get("/system/updates", "system.updates", authenticated, a.checkUpdates)
	rt.get("/settings", "settings", authenticated, a.settings)
	rt.post("/system/domain", "system.domain", authenticated, a.updateDomain)
	rt.post("/system/update", "system.update", authenticated, a.updatePanel)

	// Apps.
	//
	// The route *names* stay `services.*`: they are what the Vue pages call
	// route() with, and renaming them buys a different string for the same
	// endpoint. The URL and the label are what anybody actually sees, and those
	// say apps.
	rt.get("/apps", "services.index", authenticated, a.apps)
	rt.post("/apps/detect", "services.detect", authenticated, a.servicesDetect)
	rt.post("/apps/install", "services.install-many", authenticated, a.servicesInstallMany)
	rt.post("/apps/{service}/install", "services.install", authenticated, a.servicesInstallOne)
	rt.post("/apps/{service}/retry", "services.retry", authenticated, a.servicesRetry)
	rt.post("/apps/{service}/uninstall", "services.uninstall", authenticated, a.servicesUninstall)

	// The old path, so a bookmark or an open tab does not 404.
	rt.get("/services", "services.index-legacy", authenticated, a.apps)

	// Logs. Every queued job writes a task, and this is where they are read —
	// the pages that start the work show a line of progress and link here
	// rather than carrying a console each.
	rt.get("/logs", "logs.index", authenticated, a.logsIndex)
	rt.get("/logs/{task}", "logs.show", authenticated, a.logsShow)

	rt.get("/tasks/latest", "tasks.latest", authenticated, a.tasksLatest)
	rt.get("/tasks/{task}", "tasks.show", authenticated, a.tasksShow)

	// Sites
	rt.get("/sites", "sites.index", authenticated, a.sitesIndex)
	rt.get("/sites/create", "sites.create", authenticated, a.sitesCreate)
	rt.post("/sites", "sites.store", authenticated, a.sitesStore)
	rt.get("/sites/{site}", "sites.show", authenticated, a.sitesShow)
	rt.delete("/sites/{site}", "sites.destroy", authenticated, a.sitesDestroy)
	rt.post("/sites/{site}/dns-sync", "sites.dns-sync", authenticated, a.sitesSyncDNS)
	rt.post("/sites/{site}/redeploy", "sites.redeploy", authenticated, a.sitesRedeploy)
	rt.post("/sites/{site}/pull", "sites.pull", authenticated, a.sitesPull)
	rt.post("/sites/{site}/restart", "sites.restart", authenticated, a.sitesRestart)

	// Databases
	rt.get("/databases", "databases.index", authenticated, a.databasesIndex)
	rt.get("/databases/create", "databases.create", authenticated, a.databasesCreate)
	rt.post("/databases", "databases.store", authenticated, a.databasesStore)
	rt.get("/databases/{database}", "databases.show", authenticated, a.databasesShow)
	rt.post("/databases/{database}/password", "databases.password", authenticated,
		a.databasesPassword)
	rt.post("/databases/{database}/repair", "databases.repair", authenticated, a.databasesRepair)
	rt.post("/databases/{database}/users", "databases.users", authenticated, a.databasesUsers)
	rt.delete("/databases/{database}", "databases.destroy", authenticated, a.databasesDestroy)

	// The site page's own tabs.
	rt.post("/sites/{site}/settings", "sites.settings", authenticated, a.sitesSettings)
	rt.post("/sites/{site}/php", "sites.php", authenticated, a.sitesPHP)
	rt.post("/sites/{site}/certificate", "sites.certificate", authenticated, a.sitesCertificate)
	rt.post("/sites/{site}/cron", "sites.cron", authenticated, a.sitesCron)
	rt.post("/sites/{site}/access", "sites.access", authenticated, a.sitesAccess)

	rt.get("/sites/{site}/files", "sites.files", authenticated, a.sitesFiles)
	rt.get("/sites/{site}/file", "sites.file", authenticated, a.sitesFileRead)
	rt.post("/sites/{site}/file", "sites.file.save", authenticated, a.sitesFileWrite)
	rt.post("/sites/{site}/files", "sites.files.action", authenticated, a.sitesFileAction)
	rt.get("/sites/{site}/download", "sites.download", authenticated, a.sitesFileDownload)
	rt.get("/sites/{site}/wp-admin", "sites.wp-admin", authenticated, a.sitesWPAdmin)
	rt.post("/sites/{site}/upload", "sites.upload", authenticated, a.sitesFileUpload)
	rt.get("/databases/{database}/credentials", "databases.credentials", authenticated,
		a.databasesCredentials)
	rt.get("/databases-mongo-connection", "databases.mongo-connection", authenticated,
		a.databasesMongoConnection)

	// nginx asks this before it serves anything under /phpmyadmin. Public in
	// the route table because it answers for itself: signed in is 204, and
	// anything else is 401 — where `authenticated` would redirect to /login,
	// and a redirect is a 302, which auth_request reads as "allowed".
	rt.get(phpMyAdminGatePath, "", public, a.phpMyAdminGate)

	// Email
	rt.get("/email", "email.index", authenticated, a.emailIndex)
	rt.post("/email/repair", "email.repair", authenticated, a.emailRepair)
	rt.post("/email/test", "email.test", authenticated, a.emailTest)
	rt.get("/email/domains/create", "email.domains.create", authenticated, a.emailCreateDomain)
	rt.post("/email/domains", "email.domains.store", authenticated, a.emailStoreDomain)
	rt.get("/email/domains/{domain}", "email.domains.show", authenticated, a.emailShowDomain)
	rt.delete("/email/domains/{domain}", "email.domains.destroy", authenticated, a.emailDestroyDomain)
	rt.post("/email/domains/{domain}/dns", "email.domains.dns", authenticated, a.emailSyncDNS)
	rt.post("/email/domains/{domain}/retry", "email.domains.retry", authenticated, a.emailRetryDomain)
	rt.get("/email/domains/{domain}/accounts/create", "email.accounts.create", authenticated,
		a.emailCreateAccount)
	rt.post("/email/domains/{domain}/accounts", "email.accounts.store", authenticated,
		a.emailStoreAccount)
	rt.post("/email/accounts/{account}/repair", "email.accounts.repair", authenticated,
		a.emailRepairAccount)
	rt.delete("/email/accounts/{account}", "email.accounts.destroy", authenticated,
		a.emailDestroyAccount)

	// DNS credentials
	rt.get("/dns", "dns.index", authenticated, a.dnsIndex)
	rt.get("/dns/create", "dns.create", authenticated, a.dnsCreate)
	rt.post("/dns-accounts", "dns.store", authenticated, a.dnsStore)
	rt.patch("/dns-accounts/{account}", "dns.update", authenticated, a.dnsUpdate)
	rt.delete("/dns-accounts/{account}", "dns.destroy", authenticated, a.dnsDestroy)
	rt.post("/dns-accounts/{account}/verify", "dns.verify", authenticated, a.dnsVerify)
	rt.get("/dns-accounts/{account}/zones", "dns.zones", authenticated, a.dnsZones)
	rt.get("/dns-accounts/{account}/records", "dns.records", authenticated, a.dnsRecords)
	rt.post("/dns-accounts/{account}/records", "dns.records.store", authenticated, a.dnsStoreRecord)
	rt.delete("/dns-accounts/{account}/records", "dns.records.destroy", authenticated,
		a.dnsDestroyRecord)

	// Profile
	rt.get("/profile", "profile.edit", authenticated, a.editProfile)
	rt.patch("/profile", "profile.update", authenticated, a.updateProfile)
	rt.patch("/password", "password.update", authenticated, a.updatePassword)

	// Terminal. The websocket carries its own one-shot ticket, so it is a
	// public route: the session cookie cannot be relied on for a cross-scheme
	// upgrade, and the ticket grants nothing on its own.
	rt.get("/terminal", "terminal", authenticated, a.terminalPage)
	rt.post("/terminal/ticket", "terminal.ticket", authenticated, a.terminalTicket)
	rt.get(terminal.Path, "", public, a.Terminal.Handle)

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
