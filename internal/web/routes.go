package web

import (
	"net/http"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/inertia"
	"github.com/shusanto294/Ubuntu-Panel/internal/terminal"
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

	// The panel's mark. Served from the root rather than built by Vite: it is
	// the same image in the browser tab, in the sidebar and on the login page,
	// and a path that never changes is one a page, a bookmark and the tab icon
	// can all be pointed at.
	rt.get("/logo.png", "", public, a.serveAssets)
	rt.get("/favicon.png", "", public, a.serveAssets)
	rt.get("/up", "", public, a.health)

	// Auth
	rt.get("/login", "login", guest, a.showLogin)
	rt.post("/login", "", guest, a.login)
	rt.post("/logout", "logout", authenticated, a.logout)

	// The second step of a sign-in, for an account with two-factor on. Guest,
	// because nobody is signed in until it has been passed.
	rt.get("/login/two-factor", "two-factor.login", guest, a.showTwoFactorChallenge)
	rt.post("/login/two-factor", "", guest, a.twoFactorChallenge)

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
	rt.post("/logs/clear", "logs.clear", authenticated, a.logsClear)
	rt.post("/logs/retention", "logs.retention", authenticated, a.logsRetention)
	rt.get("/system/update/status", "system.update.status", authenticated, a.updateStatusEndpoint)
	rt.post("/system/update/automatic", "system.update.automatic", authenticated, a.updateAutomatic)

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
	rt.post("/sites/{site}/logs/clear", "sites.logs.clear", authenticated, a.sitesLogsClear)

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
	rt.post("/sites/{site}/cache", "sites.cache", authenticated, a.sitesCache)
	rt.post("/sites/{site}/access", "sites.access", authenticated, a.sitesAccess)

	// Staging. Two endpoints and two directions: the first is posted from a
	// production site's page and makes the copy, the second from the copy's own
	// page and puts it back.
	rt.post("/sites/{site}/git", "sites.git", authenticated, a.sitesGit)

	rt.post("/sites/{site}/staging", "sites.staging.store", authenticated, a.sitesStagingStore)
	rt.post("/sites/{site}/staging/push", "sites.staging.push", authenticated,
		a.sitesStagingPush)

	rt.post("/sites/{site}/backups", "sites.backups.store", authenticated, a.sitesBackupStore)
	rt.post("/sites/{site}/backups/schedule", "sites.backups.schedule", authenticated,
		a.sitesBackupSchedule)
	rt.post("/sites/{site}/backups/{backup}/restore", "sites.backups.restore", authenticated,
		a.sitesBackupRestore)
	rt.delete("/sites/{site}/backups/{backup}", "sites.backups.destroy", authenticated,
		a.sitesBackupDestroy)

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
	rt.post("/databases-mongo-connection", "databases.mongo-account", authenticated,
		a.databasesMongoAccount)

	// nginx asks this before it serves anything under /phpmyadmin. Public in
	// the route table because it answers for itself: signed in is 204, and
	// anything else is 401 — where `authenticated` would redirect to /login,
	// and a redirect is a 302, which auth_request reads as "allowed".
	rt.get(phpMyAdminGatePath, "", public, a.phpMyAdminGate)

	// The git webhook. Public because a git host sends no cookie, and it
	// authenticates itself on every request: a signature over the body, or the
	// secret in a header, both compared in constant time. Anything it cannot
	// verify is a 401, and the most a verified request can cause is a pull of
	// a repository somebody had already configured.
	rt.post("/webhooks/git/{site}", "webhooks.git", public, a.gitWebhook)
	rt.get("/webhooks/git/{site}", "", public, a.gitWebhook)

	// Email.
	//
	// The two lists are two addresses: /email/domains and /email/mailboxes, each
	// its own page with its own URL to bookmark and to be sent back to. /email
	// is the menu's entry and points at the first of them.
	rt.get("/email", "email.index", authenticated, a.emailIndex)
	rt.post("/email/repair", "email.repair", authenticated, a.emailRepair)
	rt.post("/email/test", "email.test", authenticated, a.emailTest)
	rt.get("/email/domains", "email.domains.index", authenticated, a.emailDomains)
	rt.get("/email/mailboxes", "email.mailboxes.index", authenticated, a.emailMailboxes)
	rt.get("/email/domains/create", "email.domains.create", authenticated, a.emailCreateDomain)
	rt.post("/email/domains", "email.domains.store", authenticated, a.emailStoreDomain)
	rt.get("/email/domains/{domain}", "email.domains.show", authenticated, a.emailShowDomain)
	rt.delete("/email/domains/{domain}", "email.domains.destroy", authenticated, a.emailDestroyDomain)
	rt.post("/email/domains/{domain}/dns", "email.domains.dns", authenticated, a.emailSyncDNS)
	rt.post("/email/domains/{domain}/retry", "email.domains.retry", authenticated, a.emailRetryDomain)
	rt.get("/email/mailboxes/create", "email.accounts.new", authenticated, a.emailNewMailbox)
	rt.get("/email/domains/{domain}/accounts/create", "email.accounts.create", authenticated,
		a.emailCreateAccount)
	rt.post("/email/domains/{domain}/accounts", "email.accounts.store", authenticated,
		a.emailStoreAccount)
	rt.get("/email/mailboxes/{account}", "email.accounts.show", authenticated, a.emailShowAccount)
	rt.post("/email/accounts/{account}", "email.accounts.update", authenticated,
		a.emailUpdateAccount)
	rt.post("/email/accounts/{account}/repair", "email.accounts.repair", authenticated,
		a.emailRepairAccount)
	rt.delete("/email/accounts/{account}", "email.accounts.destroy", authenticated,
		a.emailDestroyAccount)

	// DNS. This machine's own nameserver is what /dns opens on — the panel's
	// answer to "where do my records live" — and a provider is the
	// alternative, a tab along.
	rt.get("/dns", "dns.nameserver", authenticated, a.nameserverIndex)

	// DNS credentials
	rt.get("/dns/providers", "dns.index", authenticated, a.dnsIndex)
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

	// The panel's own nameserver: the zones this machine answers for itself,
	// and the setup that has to happen at a registrar before it answers for
	// anything. Its page moved to /dns itself; the old address still lands.
	rt.get("/dns/nameserver", "", authenticated, func(w http.ResponseWriter, r *http.Request) {
		inertia.Redirect(w, r, "/dns")
	})
	rt.post("/dns/nameserver", "dns.nameserver.settings", authenticated, a.nameserverSettings)
	rt.post("/dns/nameserver/resync", "dns.nameserver.resync", authenticated, a.nameserverResync)
	rt.post("/dns/zones", "dns.zones.store", authenticated, a.nameserverZoneStore)
	rt.get("/dns/zones/{zone}/check", "dns.zones.check", authenticated, a.nameserverZoneCheck)
	rt.delete("/dns/zones/{zone}", "dns.zones.destroy", authenticated, a.nameserverZoneDestroy)

	// AI access: the keys an assistant signs its MCP requests with. The MCP
	// endpoint itself is not in this table — it is routed before the session
	// middleware, in Handler(), because it authenticates itself.
	rt.get("/ai", "ai.index", authenticated, a.aiIndex)
	rt.post("/ai/settings", "ai.settings", authenticated, a.aiSettings)
	rt.post("/ai/tokens", "ai.tokens.store", authenticated, a.aiStoreToken)
	rt.delete("/ai/tokens/{token}", "ai.tokens.destroy", authenticated, a.aiDestroyToken)

	// Security: who is trying to get in, and what stops them.
	rt.get("/security", "security.index", authenticated, a.securityIndex)
	rt.get("/security/countries", "security.countries", authenticated, a.securityCountries)
	rt.post("/security/ssh-protection", "security.ssh-protection", authenticated, a.securitySSHProtection)
	rt.post("/security/ssh-protection/install", "security.ssh-protection.install", authenticated,
		a.securityInstallFail2ban)
	rt.post("/security/bans", "security.bans.store", authenticated, a.securityBan)
	rt.post("/security/bans/remove", "security.bans.destroy", authenticated, a.securityUnban)
	rt.post("/security/trust", "security.trust", authenticated, a.securityTrust)
	rt.post("/security/login-protection", "security.login-protection", authenticated,
		a.securityLoginProtection)
	rt.post("/security/logins/unlock", "security.logins.unlock", authenticated, a.securityUnlock)
	rt.post("/security/firewall/enable", "security.firewall.enable", authenticated, a.securityFirewallEnable)
	rt.post("/security/firewall/disable", "security.firewall.disable", authenticated,
		a.securityFirewallDisable)
	rt.post("/security/firewall/rules", "security.firewall.rules.store", authenticated,
		a.securityFirewallRule)
	rt.delete("/security/firewall/rules/{rule}", "security.firewall.rules.destroy", authenticated,
		a.securityFirewallDelete)
	rt.post("/security/firewall/block", "security.firewall.block", authenticated, a.securityFirewallBlock)
	rt.post("/security/hardening", "security.hardening", authenticated, a.securityHardening)
	rt.post("/security/ssh-service", "security.ssh-service", authenticated, a.securitySSHService)
	rt.post("/security/ssh-trap", "security.ssh-trap", authenticated, a.securitySSHTrap)
	rt.post("/security/ssh-trap/unban", "security.ssh-trap.unban", authenticated, a.securitySSHTrapUnban)
	rt.post("/security/updates/automatic", "security.updates.automatic", authenticated,
		a.securityAutoUpdates)
	rt.post("/security/updates/install", "security.updates.install", authenticated,
		a.securityInstallUpdates)
	rt.post("/security/alerts", "security.alerts", authenticated, a.securityAlerts)
	rt.delete("/security/sessions/{session}", "security.sessions.destroy", authenticated,
		a.securityRevokeSession)
	rt.post("/security/sessions/others", "security.sessions.others", authenticated,
		a.securityRevokeOthers)

	// The accounts that can log in over SSH.
	rt.post("/security/users", "security.users.store", authenticated, a.sshUserStore)
	rt.post("/security/users/{user}", "security.users.update", authenticated, a.sshUserUpdate)
	rt.delete("/security/users/{user}", "security.users.destroy", authenticated, a.sshUserDestroy)
	rt.post("/security/users/{user}/keys", "security.users.keys.store", authenticated, a.sshUserKeyStore)
	rt.post("/security/users/{user}/keys/remove", "security.users.keys.destroy", authenticated,
		a.sshUserKeyDestroy)

	// The relay the panel sends its own mail through.
	rt.patch("/settings/smtp", "settings.smtp", authenticated, a.smtpUpdate)
	rt.post("/settings/smtp/test", "settings.smtp.test", authenticated, a.smtpTest)
	rt.delete("/settings/smtp", "settings.smtp.destroy", authenticated, a.smtpDestroy)

	// Profile
	rt.get("/profile", "profile.edit", authenticated, a.editProfile)
	rt.patch("/profile", "profile.update", authenticated, a.updateProfile)
	rt.patch("/password", "password.update", authenticated, a.updatePassword)
	rt.post("/profile/two-factor", "two-factor.enable", authenticated, a.twoFactorStart)
	rt.post("/profile/two-factor/confirm", "two-factor.confirm", authenticated, a.twoFactorConfirm)
	rt.post("/profile/two-factor/recovery-codes", "two-factor.recovery-codes", authenticated,
		a.twoFactorRecoveryCodes)
	rt.post("/profile/two-factor/disable", "two-factor.disable", authenticated, a.twoFactorDisable)

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
