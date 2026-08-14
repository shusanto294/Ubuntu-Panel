package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
)

// siteApps is everything any site type can want, so the create form can offer
// to install whatever the chosen type is missing without another round trip.
var siteApps = []string{"nginx", "php", "composer", "certbot", "mysql", "wpcli", "node"}

func (a *App) sitesIndex(w http.ResponseWriter, r *http.Request) {
	sites, err := a.Store.Sites(r.Context())
	if err != nil {
		a.serverError(w, r, "could not list the sites", err)
		return
	}

	summaries := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		summaries = append(summaries, site.Summary(false))
	}

	a.Render.Render(w, r, "Sites/Index", inertia.Props{
		"sites":      summaries,
		"activeTask": a.activeTask(r, "site"),
	})
}

func (a *App) sitesCreate(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	// Cached: this page reloads itself every few seconds while an install
	// runs, and re-probing seven apps each time is what makes the machine too
	// busy to answer the reload after it.
	a.Catalog.DetectFresh(siteApps, 30*time.Second)

	accounts, _ := a.Store.DNSAccounts(r.Context(), user.ID)

	dnsAccounts := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		dnsAccounts = append(dnsAccounts, dnsAccountSummary(account))
	}

	services, _ := a.Store.Services(r.Context())

	pending := []string{}
	for key, service := range services {
		if service.Status == store.ServiceQueued || service.Status == store.ServiceInstalling {
			pending = append(pending, key)
		}
	}

	siteTypes := make([]map[string]any, 0, len(config.SiteTypes))
	for _, kind := range config.SiteTypes {
		siteTypes = append(siteTypes, map[string]any{
			"key":           kind.Key,
			"label":         kind.Label,
			"description":   kind.Description,
			"web_directory": kind.WebDirectory,
			"runtime":       kind.Runtime,
			"database":      kind.Database,
			"proxied":       kind.Proxied,
			"build":         kind.Build,
			"start":         kind.Start,
			"managed":       kind.Managed,
			"admin_account": kind.AdminAccount,
		})
	}

	a.Render.Render(w, r, "Sites/Create", inertia.Props{
		"installedServices":    a.Store.InstalledServiceKeys(r.Context()),
		"requirements":         a.Catalog.Requirements(r.Context(), siteApps),
		"pendingServices":      pending,
		"phpVersion":           a.Store.PHPVersion(),
		"nodeVersion":          a.Store.NodeVersion(),
		"publicIp":             system.PublicIP(),
		"dnsAccounts":          dnsAccounts,
		"phpVersions":          config.PHPVersions,
		"installedPhpVersions": system.InstalledPHPVersions(),
		"dnsTypes":             config.SiteDNSTypes,
		"sitesRoot":            a.Config.SitesRoot,
		"siteTypes":            siteTypes,
	})
}

// missingRequirement is what this machine is missing for a site type.
func (a *App) missingRequirement(kind string) string {
	meta := config.FindSiteType(kind)
	if meta == nil {
		return "That is not a site type the panel can deploy."
	}

	if meta.Proxied && !a.Store.ServiceInstalled("node") {
		return "Node.js is not installed. Install it from the Apps page first."
	}

	if kind == "wordpress" {
		if !a.Store.ServiceInstalled("mysql") {
			return "WordPress needs MariaDB. Install it from the Apps page first."
		}

		if !a.Store.ServiceInstalled("wpcli") {
			return "WP-CLI is not installed. Install it from the Apps page first."
		}
	}

	if meta.Database && !a.Store.ServiceInstalled("mysql") {
		return meta.Label + " needs MariaDB for its database. Install it from the Apps page first."
	}

	if meta.Runtime == "php" && !a.Store.ServiceInstalled("php") {
		return meta.Label + " needs PHP. Install it from the Apps page first."
	}

	return ""
}

func (a *App) sitesStore(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	kind := input["type"]
	domain := strings.ToLower(strings.TrimSpace(input["domain"]))
	php := input["php_version"]
	dnsType := stringInput(input, "dns_type", "A")

	meta := config.FindSiteType(kind)

	if meta == nil {
		sess.Error("type", "That is not a site type the panel can deploy.")
	}
	if !validDomain(domain) {
		sess.Error("domain", "Enter a domain, like example.com.")
	}
	if !config.IsPHPVersion(php) {
		sess.Error("php_version", "That is not a PHP version the panel installs.")
	}
	if !config.IsDNSRecordType(dnsType) {
		sess.Error("dns_type", "That is not a record type this form offers.")
	}

	manageDNS := boolInput(input, "manage_dns", false)
	ssl := boolInput(input, "ssl", true)

	var dnsAccountID int64

	if manageDNS {
		dnsAccountID = intInput(input, "dns_account_id", 0)

		if dnsAccountID <= 0 {
			sess.Error("dns_account_id", "Pick a DNS credential the panel can write with.")
		} else if _, err := a.Store.FindDNSAccount(r.Context(), dnsAccountID, user.ID); err != nil {
			sess.Error("dns_account_id", "Pick a DNS credential the panel can write with.")
		}
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/sites/create")
		return
	}

	if _, err := a.Store.FindSiteByDomain(r.Context(), domain); err == nil {
		sess.Error("domain", "A site for this domain already exists.")
		inertia.Back(w, r, "/sites/create")

		return
	}

	// Asked of the machine rather than the row: a stale `failed` from a
	// half-finished install would refuse a site this server can host perfectly
	// well.
	a.Catalog.Detect([]string{"node", "mysql", "wpcli", "php"})

	if problem := a.missingRequirement(kind); problem != "" {
		sess.Error("type", problem)
		inertia.Back(w, r, "/sites/create")

		return
	}

	// An explicit web directory wins; otherwise the type's own default.
	webDirectory := stringInput(input, "web_directory", meta.WebDirectory)
	webDirectory = strings.Trim(webDirectory, "/")

	if webDirectory != "" {
		webDirectory = "/" + webDirectory
	}

	// An alias that is not a hostname would go straight into an nginx
	// server_name, so it is dropped rather than written.
	aliases := []string{}

	for _, alias := range listInput(input, "aliases") {
		alias = strings.ToLower(strings.TrimSpace(alias))

		if validDomain(alias) && alias != domain {
			aliases = append(aliases, alias)
		}
	}

	dnsContent := stringInput(input, "dns_content", system.PublicIP())

	site := &store.Site{
		UserID:       user.ID,
		DNSAccountID: dnsAccountID,
		Domain:       domain,
		Type:         kind,
		Aliases:      aliases,
		RootPath:     strings.TrimRight(a.Config.SitesRoot, "/") + "/" + domain,
		WebDirectory: webDirectory,
		PHPVersion:   php,
		NodeVersion:  a.Store.NodeVersion(),
		SSL:          ssl,
		Repository:   strings.TrimSpace(input["repository"]),
		Branch:       stringInput(input, "branch", "main"),
		ManageDNS:    manageDNS,
		DNSType:      dnsType,
		DNSContent:   dnsContent,
		DNSProxied:   boolInput(input, "dns_proxied", true),
	}

	if meta.Proxied {
		site.AppPort = a.Store.NextFreePort(r.Context())
		site.StartCommand = stringInput(input, "start_command", orDefault(meta.Start, "npm run start"))
		site.BuildCommand = stringInput(input, "build_command", meta.Build)
	}

	// The application's own administrator, for the types where the panel
	// creates one. The panel
	// puts one in front of it: same username, and a password it generates.
	if meta.AdminAccount {
		site.WPTitle = stringInput(input, "wp_title", store.DefaultWPTitle)
		site.WPAdminEmail = stringInput(input, "wp_admin_email", user.Email)
		site.WPAdminPassword = strings.TrimSpace(input["wp_admin_password"])

		site.WPAdminUser = stringInput(input, "wp_admin_user", "admin")
	}

	id, err := a.Store.CreateSite(r.Context(), site)
	if err != nil || id <= 0 {
		a.Log.Error("could not create the site record", "error", err)
		a.failBack(w, r, "Could not create the site record.")

		return
	}

	if _, err := a.Queue.PushID(queue.SiteDeploy, "site", id); err != nil {
		a.Log.Error("could not queue the deployment", "site", id, "error", err)
	}

	// No flash. The page this redirects to opens with the build running on it,
	// step by step — a sentence above that saying the same thing is one more
	// thing to read and then dismiss.
	inertia.Redirect(w, r, "/sites/"+itoa(id))
}

func (a *App) sitesShow(w http.ResponseWriter, r *http.Request) {
	id := routeID(r, "site")

	site, err := a.Store.FindSite(r.Context(), id)

	// A site being torn down still has its row — the worker is using it — but
	// it is gone as far as the panel is concerned, and its page with it.
	if err != nil || site.Status == "deleting" {
		a.notFoundError(w, r, "No such site.")
		return
	}

	// Whether a certificate is even possible here. The notice about a missing
	// one used to tell everybody to install Certbot, including the people who
	// already had — and for them the real answer is in the log.
	a.Catalog.DetectFresh([]string{"certbot"}, 30*time.Second)

	// This site's history and nobody else's, and without the output column:
	// twenty-five deployments of a Next.js site is twenty-five npm builds'
	// worth of it in one page payload, and the page renders a list of what
	// happened — the output is one click away on the log's own page.
	tasks, _ := a.Store.TasksForSite(r.Context(), id, 25)

	logs := []map[string]any{}

	for _, task := range tasks {
		logs = append(logs, map[string]any{
			"id":         task.ID,
			"type":       task.Type,
			"action":     task.Action,
			"status":     task.Status,
			"message":    task.Message,
			"created_at": task.CreatedAt,
		})
	}

	// Whether a visitor can get here at all. Everything else on this page is
	// about the machine, and all of it can be right while the domain resolves
	// to nothing — which is what the browser calls
	// DNS_PROBE_FINISHED_NXDOMAIN and the operator calls a broken panel.
	//
	// Not while the site is being built: the page polls every three seconds
	// then, and the answer is not settled until the deployment has published
	// the record anyway.
	resolution := map[string]any{"checked": false}

	if site.Status != "pending" && site.Status != "deploying" {
		expected := system.PublicIP()
		answer := system.Lookup(r.Context(), site.Domain, expected)

		addresses := answer.Addresses
		if addresses == nil {
			addresses = []string{}
		}

		resolution = map[string]any{
			"checked":   true,
			"resolves":  answer.Resolves(),
			"matches":   answer.Matches,
			"addresses": addresses,
			"expected":  expected,
		}
	}

	// What the PHP tab offers, with the site's own value against each and the
	// value PHP would use where the site has none. Sent together so the form is
	// one loop and the panel decides what is settable, not the page.
	directives := make([]map[string]any, 0, len(sites.PHPDirectives))
	overrides := site.PHPOverrides()

	for _, directive := range sites.PHPDirectives {
		directives = append(directives, map[string]any{
			"key":     directive.Key,
			"label":   directive.Label,
			"help":    directive.Help,
			"default": directive.Default,
			"value":   overrides[directive.Key],
		})
	}

	// The database the panel made for this site. Not in the site's own summary
	// because the row only holds its id — and without this the Database tab
	// told a WordPress site with a database that it did not have one.
	var database map[string]any

	if site.DatabaseID > 0 {
		if row, err := a.Store.FindDatabase(r.Context(), site.DatabaseID); err == nil {
			database = row.Summary()
		}
	}

	a.Render.Render(w, r, "Sites/Show", inertia.Props{
		"certbot":       a.Store.ServiceInstalled("certbot"),
		"database":      database,
		"logFiles":      a.siteLogFiles(site, 200),
		"resolution":    resolution,
		"phpDirectives": directives,
		"accessUsers":   a.Sites.AccessUsers(site),
		"cache":         a.cacheSummary(site),
		"cronJobs":      a.Sites.CronJobs(site),
		"certificate":   a.certificateSummary(site),
		"site":          site.Summary(true),
		"logs":          logs,
		"activeTask":    a.Store.TaskConsole(r.Context(), a.Store.LatestTaskForSite(r.Context(), id, true), 0),
		"latestTask":    a.Store.TaskConsole(r.Context(), a.Store.LatestTaskForSite(r.Context(), id, false), 0),
	})
}

func (a *App) sitesDestroy(w http.ResponseWriter, r *http.Request) {
	id := routeID(r, "site")

	site, err := a.Store.FindSite(r.Context(), id)
	if err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, _ := a.input(r)

	// Gone from the panel now; taken apart on the machine afterwards. Removing
	// a site means a vhost, a systemd unit, DNS records at a provider, a
	// database and a directory tree — and the operator has already decided all
	// of it. The stamp hides it from every list immediately; the worker still
	// loads the row by id, and deletes it for good when the teardown is done.
	a.Store.MarkSiteDeleted(site.ID)

	a.Queue.Push(queue.SiteDelete, map[string]any{
		"site":  site.ID,
		"files": boolInput(input, "delete_files", true),
	})

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", "Site deleted.")
	}

	inertia.Redirect(w, r, "/sites")
}

func (a *App) sitesRedeploy(w http.ResponseWriter, r *http.Request) {
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	a.Queue.PushID(queue.SiteDeploy, "site", id)

	inertia.Back(w, r, "/sites")
}

func (a *App) sitesPull(w http.ResponseWriter, r *http.Request) {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	if strings.TrimSpace(site.Repository) == "" {
		a.failBack(w, r, "This site is not backed by a git repository.")
		return
	}

	a.Queue.PushID(queue.SitePull, "site", site.ID)

	inertia.Back(w, r, "/sites")
}

func (a *App) sitesRestart(w http.ResponseWriter, r *http.Request) {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	if !site.IsProxied() {
		a.failBack(w, r, "Only Node.js and Next.js sites run a restartable service.")
		return
	}

	a.Queue.PushID(queue.SiteRestart, "site", site.ID)

	inertia.Back(w, r, "/sites")
}

func (a *App) sitesSyncDNS(w http.ResponseWriter, r *http.Request) {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	if !site.ManageDNS {
		a.failBack(w, r, "DNS management is not enabled for this site.")
		return
	}

	if _, err := dns.SyncForSite(r.Context(), a.Store, site); err != nil {
		a.failBack(w, r, "DNS sync failed: "+err.Error())
		return
	}

	a.okBack(w, r, "DNS records synced.")
}

// activeTask is the running task of a type, or null.
func (a *App) activeTask(r *http.Request, kind string) map[string]any {
	return a.Store.TaskConsole(r.Context(), a.Store.LatestTaskID(r.Context(), kind, true), 0)
}

// dnsAccountSummary never includes the credential itself.
func dnsAccountSummary(account *store.DNSAccount) map[string]any {
	verified := any(nil)
	if account.VerifiedAt != "" {
		verified = account.VerifiedAt
	}

	return map[string]any{
		"id":             account.ID,
		"provider":       account.Provider,
		"provider_label": dns.ProviderLabel(account.Provider),
		"label":          account.Label,
		"supports_proxy": dns.SupportsProxy(account.Provider),
		"verified_at":    verified,
		"sites_count":    account.SitesCount,
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

// certificateSummary is what the SSL/TLS tab shows: whether Let's Encrypt has
// issued anything for this site, and when it stops being valid.
//
// Read from the certificate on disk rather than from the site's `ssl` column.
// That column is what the site asked for; this is what it got, and the two
// differ precisely when somebody needs to look at this tab.
func (a *App) certificateSummary(site *store.Site) map[string]any {
	path := sites.CertificatePath(site) + "/fullchain.pem"

	summary := map[string]any{
		"wanted":  site.SSL,
		"issued":  false,
		"certbot": a.Store.ServiceInstalled("certbot"),
		"path":    path,
		"expires": nil,
		"issuer":  nil,
		// Whose certificate the visitor is shown. A site behind Cloudflare's
		// proxy on Flexible has none here on purpose, and a tab that only said
		// "no certificate" about a site serving perfectly good HTTPS is a tab
		// that sends people looking for a fault.
		"edge": site.EdgeTLS(),
	}

	if !proc.Ok("sudo test -f " + proc.Quote(path)) {
		return summary
	}

	summary["issued"] = true

	// -noout, so what comes back is the two lines this shows and not a
	// kilobyte of base64 in a page payload.
	details := proc.Run("sudo openssl x509 -in "+proc.Quote(path)+
		" -noout -enddate -issuer 2>/dev/null", 30*time.Second).Output

	for _, line := range strings.Split(details, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "notAfter="); found {
			summary["expires"] = value
		}

		if value, found := strings.CutPrefix(strings.TrimSpace(line), "issuer="); found {
			summary["issuer"] = value
		}
	}

	return summary
}

// sitesSettings changes the document root.
func (a *App) sitesSettings(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if err := a.Sites.SetWebDirectory(r.Context(), id, input["web_directory"]); err != nil {
		a.failBack(w, r, err.Error())
		return
	}

	sess.Flash("success", "The root directory was changed and nginx reloaded.")

	inertia.Back(w, r, "/sites")
}

// sitesPHP writes the site's php.ini overrides.
func (a *App) sitesPHP(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	// Only the directives the panel offers. The form posts them flat, so
	// anything else in the body is somebody's idea rather than the form's.
	settings := map[string]string{}

	for name, value := range input {
		if sites.IsPHPDirective(name) {
			settings[name] = value
		}
	}

	if err := a.Sites.SetPHPSettings(r.Context(), id, settings); err != nil {
		a.failBack(w, r, err.Error())
		return
	}

	sess.Flash("success", "The PHP settings were applied.")

	inertia.Back(w, r, "/sites")
}

// cacheSummary is what the Cache tab reads: what is set, what the panel will
// take, and where the pages are kept.
//
// `supported` rather than hiding the tab: a static site has a Cache tab that
// says why it has no cache, which is an answer. A missing tab is a question.
func (a *App) cacheSummary(site *store.Site) map[string]any {
	lifetimes := make([]map[string]string, 0, len(sites.CacheTTLs))

	for _, entry := range sites.CacheTTLs {
		lifetimes = append(lifetimes, map[string]string{
			"value": entry.Value,
			"label": entry.Label,
		})
	}

	return map[string]any{
		"mode":            site.CacheMode,
		"enabled":         site.CacheEnabled,
		"ttl":             sites.CacheTTLOf(site),
		"supported":       sites.Cacheable(site),
		"redis_supported": sites.RedisSupported(site),
		"redis_installed": a.Store.ServiceInstalled("redis"),
		"redis_prefix":    sites.RedisPrefix(site),
		"proxied":         site.IsProxied(),
		"path":            sites.CacheDir(site),
		"lifetimes":       lifetimes,
	}
}

// sitesCache turns the page cache on or off, and empties it.
//
// One endpoint for both, because emptying is the thing somebody does straight
// after changing it — and a purge that lived somewhere else would be a second
// place to get the site lookup wrong.
func (a *App) sitesCache(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if input["action"] == "purge" {
		if err := a.Sites.PurgeCache(r.Context(), id); err != nil {
			a.failBack(w, r, err.Error())
			return
		}

		sess.Flash("success", "The cache was emptied. The next visitor gets a freshly built page.")

		inertia.Back(w, r, "/sites")

		return
	}

	mode := input["mode"]

	if err := a.Sites.SetCache(r.Context(), id, mode, input["ttl"]); err != nil {
		a.failBack(w, r, err.Error())
		return
	}

	switch mode {
	case "nginx":
		sess.Flash("success", "The page cache is on and nginx was reloaded.")
	case "redis":
		sess.Flash("success", "This site's object cache is Redis. WordPress was pointed at it "+
			"and the drop-in installed.")
	default:
		sess.Flash("success", "Caching is off. Every request is built by the site again.")
	}

	inertia.Back(w, r, "/sites")
}

// sitesCertificate asks certbot for the site's certificate, now.
func (a *App) sitesCertificate(w http.ResponseWriter, r *http.Request) {
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	a.Queue.PushID(queue.SiteCertificate, "site", id)

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("status", "Requesting the certificate. The Logs tab has the run.")
	}

	inertia.Back(w, r, "/sites")
}

// sitesCron writes the whole of a site's schedule.
func (a *App) sitesCron(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	// The page posts the whole list as JSON in one field, because a schedule
	// and a command are two halves of one line and a flat form would have to
	// number them.
	var jobs []sites.CronJob

	if encoded := strings.TrimSpace(input["jobs"]); encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &jobs); err != nil {
			a.failBack(w, r, "The schedule could not be read.")

			return
		}
	}

	if err := a.Sites.SetCronJobs(r.Context(), id, jobs); err != nil {
		a.failBack(w, r, err.Error())
		return
	}

	sess.Flash("success", "The schedule was written.")

	inertia.Back(w, r, "/sites")
}

// siteLogFiles is the last of nginx's own logs for this site.
//
// The Logs tab used to show the panel's task history, which is a list of what
// the panel did — deployments, restarts, certificates. That is worth keeping,
// but it is not what "the site's logs" means to anybody looking for a 500: the
// site's logs are the two files nginx writes for it, which every vhost the
// panel renders already names.
//
// The tail, not the file. An error log on a busy site is megabytes, and the
// question being asked of it is always about the last few minutes.
func (a *App) siteLogFiles(site *store.Site, lines int) []map[string]any {
	files := []struct {
		key, label, path string
	}{
		{"access", "Access log", "/var/log/nginx/" + site.Domain + "-access.log"},
		{"error", "Error log", "/var/log/nginx/" + site.Domain + "-error.log"},
	}

	// A proxied site's application writes its own, which is where the reason
	// for a 502 actually is — nginx only knows it could not connect.
	if site.IsProxied() {
		files = append(files, struct{ key, label, path string }{
			"app", site.TypeLabel() + " log", "/var/log/" + site.ServiceName() + ".log",
		})
	}

	summaries := make([]map[string]any, 0, len(files))

	for _, file := range files {
		contents := proc.Run(fmt.Sprintf("sudo tail -n %d %s 2>/dev/null", lines,
			proc.Quote(file.path)), 30*time.Second).Output

		summaries = append(summaries, map[string]any{
			"key":   file.key,
			"label": file.label,
			"path":  file.path,
			"lines": nullable(strings.TrimRight(contents, "\n")),
		})
	}

	return summaries
}

// ------------------------------------------------------------ file manager

// siteFor loads the site a file-manager request names.
func (a *App) siteFor(w http.ResponseWriter, r *http.Request) *store.Site {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil {
		a.notFoundError(w, r, "No such site.")

		return nil
	}

	return site
}

// sitesFiles lists one folder.
func (a *App) sitesFiles(w http.ResponseWriter, r *http.Request) {
	site := a.siteFor(w, r)
	if site == nil {
		return
	}

	entries, where, err := a.Sites.ListFiles(site, r.URL.Query().Get("path"))
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"path":    where,
		"entries": entries,
		"root":    site.RootPath,
	})
}

// sitesFileRead opens a file in the editor.
func (a *App) sitesFileRead(w http.ResponseWriter, r *http.Request) {
	site := a.siteFor(w, r)
	if site == nil {
		return
	}

	contents, err := a.Sites.ReadFile(site, r.URL.Query().Get("path"))
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"path":     r.URL.Query().Get("path"),
		"contents": contents,
	})
}

// sitesFileWrite saves it back.
func (a *App) sitesFileWrite(w http.ResponseWriter, r *http.Request) {
	site := a.siteFor(w, r)
	if site == nil {
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	if err := a.Sites.WriteFile(site, input["path"], input["contents"]); err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

// sitesFileAction is everything else the file manager does: new, rename,
// delete. One endpoint, because they are one form each and three routes for
// three verbs is three places to get the path check wrong.
func (a *App) sitesFileAction(w http.ResponseWriter, r *http.Request) {
	site := a.siteFor(w, r)
	if site == nil {
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	switch input["action"] {
	case "folder":
		err = a.Sites.CreateEntry(site, input["path"], true)
	case "file":
		err = a.Sites.CreateEntry(site, input["path"], false)
	case "rename":
		err = a.Sites.RenameEntry(site, input["path"], input["to"])
	case "delete":
		err = a.Sites.DeleteEntry(site, input["path"])
	case "zip":
		var names []string

		if encoded := strings.TrimSpace(input["names"]); encoded != "" {
			if err := json.Unmarshal([]byte(encoded), &names); err != nil {
				a.writeJSON(w, http.StatusOK, map[string]any{"error": "the selection could not be read"})

				return
			}
		}

		err = a.Sites.Compress(site, input["path"], names, input["name"])
	case "unzip":
		err = a.Sites.Extract(site, input["path"])
	default:
		err = errors.New("that is not something the file manager does")
	}

	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})

		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"done": true})
}

// sitesFileDownload sends a file, or a folder as a tar.gz.
//
// Streamed rather than read: a site directory is measured in hundreds of
// megabytes, and holding one in memory to hand it to a browser is how a panel
// with 1 GB of RAM stops answering.
func (a *App) sitesFileDownload(w http.ResponseWriter, r *http.Request) {
	site := a.siteFor(w, r)
	if site == nil {
		return
	}

	download, err := a.Sites.DownloadFor(site, r.URL.Query().Get("path"))
	if err != nil {
		a.notFoundError(w, r, err.Error())

		return
	}

	// The filename is quoted and its quotes stripped: a name is whatever the
	// filesystem allowed, and one containing a quote would otherwise end the
	// header and start a second one.
	safe := strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(download.Name)

	w.Header().Set("Content-Type", download.Type)
	w.Header().Set("Content-Disposition", `attachment; filename="`+safe+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	flusher, _ := w.(http.Flusher)

	proc.Stream(download.Command, 2*time.Hour, func(chunk []byte) {
		if _, err := w.Write(chunk); err != nil {
			return
		}

		if flusher != nil {
			flusher.Flush()
		}
	})
}

// sitesFileUpload receives files into the folder the page is showing.
func (a *App) sitesFileUpload(w http.ResponseWriter, r *http.Request) {
	site := a.siteFor(w, r)
	if site == nil {
		return
	}

	// Streamed through a reader rather than parsed into memory: ParseMultipartForm
	// keeps everything under its limit in RAM, and an upload is exactly the
	// request that is larger than the limit somebody chose.
	reader, err := r.MultipartReader()
	if err != nil {
		a.badRequest(w, r, err)

		return
	}

	folder := "/"
	uploaded := 0

	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}

		if part.FormName() == "path" {
			where, _ := io.ReadAll(io.LimitReader(part, 4096))
			folder = string(where)

			continue
		}

		if part.FileName() == "" {
			continue
		}

		staged, err := os.CreateTemp("", "ubuntu-panel-upload-*")
		if err != nil {
			a.writeJSON(w, http.StatusOK, map[string]any{"error": "the upload could not be staged"})

			return
		}

		_, copyErr := io.Copy(staged, part)
		staged.Close()

		if copyErr != nil {
			os.Remove(staged.Name())
			a.writeJSON(w, http.StatusOK, map[string]any{"error": "the upload did not finish"})

			return
		}

		// Readable by root, which is what moves it into place.
		os.Chmod(staged.Name(), 0o644)

		if err := a.Sites.PlaceUpload(site, folder, part.FileName(), staged.Name()); err != nil {
			os.Remove(staged.Name())
			a.writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})

			return
		}

		uploaded++
	}

	a.writeJSON(w, http.StatusOK, map[string]any{"uploaded": uploaded})
}

// sitesWPAdmin sends the browser into a site's WordPress dashboard, signed in.
//
// A redirect rather than a link the page could build: the sign-in token is
// minted per click, spent on arrival, and valid for two minutes — so it cannot
// be something rendered into a page that might sit open on a screen.
func (a *App) sitesWPAdmin(w http.ResponseWriter, r *http.Request) {
	site, err := a.Store.FindSite(r.Context(), routeID(r, "site"))
	if err != nil {
		a.notFoundError(w, r, "No such site.")

		return
	}

	link, err := a.Sites.WordPressLoginURL(r.Context(), site.ID)
	if err != nil {
		// Straight to wp-admin, which is where they were going: a sign-in that
		// could not be arranged is a login form, not a dead end.
		a.Log.Warn("could not mint a WordPress sign-in link",
			"site", site.Domain, "error", err)

		http.Redirect(w, r, site.URL()+"/wp-admin/", http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, link, http.StatusSeeOther)
}

// sitesAccess is the SSH/SFTP tab's one endpoint: add an account, change a
// password, remove one.
func (a *App) sitesAccess(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	id := routeID(r, "site")

	if _, err := a.Store.FindSite(r.Context(), id); err != nil {
		a.notFoundError(w, r, "No such site.")
		return
	}

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	name := strings.TrimSpace(strings.ToLower(input["username"]))
	password := input["password"]

	// Generated when none was typed: this account opens a session on the
	// server, and one somebody invents at the keyboard is the weakest thing
	// standing in front of it.
	if strings.TrimSpace(password) == "" {
		password = crypto.Password(24)
	}

	switch input["action"] {
	case "create":
		err = a.Sites.CreateAccessUser(r.Context(), id, name, password,
			input["sftp_only"] == "1" || input["sftp_only"] == "true")

		if err == nil {
			sess.Flash("success", name+" was created. Its password is "+password+
				" — the panel does not store it, so copy it now.")
		}

	case "password":
		err = a.Sites.SetAccessPassword(r.Context(), id, name, password)

		if err == nil {
			sess.Flash("success", "The password for "+name+" is now "+password+
				" — copy it before leaving this page.")
		}

	case "delete":
		err = a.Sites.DeleteAccessUser(r.Context(), id, name)

		if err == nil {
			sess.Flash("danger", name+" can no longer sign in. The site's files are untouched.")
		}

	default:
		err = errors.New("that is not something the panel does to an account")
	}

	if err != nil {
		a.failBack(w, r, err.Error())

		return
	}

	inertia.Back(w, r, "/sites")
}
