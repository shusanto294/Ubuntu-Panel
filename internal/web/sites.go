package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
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
	// creates one. OpenClaw's gateway has no login of its own, so the panel
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

	a.Render.Render(w, r, "Sites/Show", inertia.Props{
		"certbot":       a.Store.ServiceInstalled("certbot"),
		"resolution":    resolution,
		"phpDirectives": directives,
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
