package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
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
	// creates one. OpenClaw has no username — its gateway takes a shared
	// secret — so only the password half is used there, and it is generated.
	if meta.AdminAccount {
		site.WPTitle = stringInput(input, "wp_title", store.DefaultWPTitle)
		site.WPAdminEmail = stringInput(input, "wp_admin_email", user.Email)
		site.WPAdminPassword = strings.TrimSpace(input["wp_admin_password"])

		if kind != "openclaw" {
			site.WPAdminUser = stringInput(input, "wp_admin_user", "admin")
		}
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

	a.Render.Render(w, r, "Sites/Show", inertia.Props{
		"certbot":    a.Store.ServiceInstalled("certbot"),
		"site":       site.Summary(true),
		"logs":       logs,
		"activeTask": a.Store.TaskConsole(r.Context(), a.Store.LatestTaskForSite(r.Context(), id, true), 0),
		"latestTask": a.Store.TaskConsole(r.Context(), a.Store.LatestTaskForSite(r.Context(), id, false), 0),
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
