package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/mcp"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
)

// MCPPath is where an assistant connects. It is deliberately not under /api:
// the whole of the panel's machine-facing surface is this one endpoint, and the
// address somebody pastes into a client should be short enough to type.
const MCPPath = "/mcp"

// mcpInstructions is what the assistant is told on connection. It is the only
// prose it gets before it starts choosing tools, so it says what the panel is,
// what work is asynchronous, and how to find out whether something worked.
const mcpInstructions = `This server is Ubuntu Panel, a control panel running on one Ubuntu server. ` +
	`Through it you can create and manage websites, databases and the applications the server needs.

Creating or deleting a site is asynchronous: the tool returns immediately with a site id, and the ` +
	`work — cloning the repository, installing the runtime, writing the nginx vhost, requesting the ` +
	`TLS certificate — runs on the server's queue and takes anywhere from thirty seconds to several ` +
	`minutes. Poll get_site for the site's status and its latest task, and get_task for the console ` +
	`output of a task that failed. Do not report a site as ready until its status is "active".

Before creating a site, check list_site_types: it says what this machine can host right now and ` +
	`which apps are missing for the rest. Missing apps can be installed with install_apps, which is ` +
	`also asynchronous.

Every domain must point at this server for TLS to be issued. server_status has the public IP.`

// mcpUserKey carries the account a token belongs to into the tools.
type mcpUserKey struct{}

func mcpUser(ctx context.Context) *store.User {
	user, _ := ctx.Value(mcpUserKey{}).(*store.User)

	return user
}

// withMCP puts the MCP endpoint in front of the session layer.
//
// An assistant sends a bearer token and no cookie, and every request without
// one makes the session middleware write a row. A client polling a deployment
// every two seconds would fill the sessions table with rows nobody will ever
// read, so the endpoint is routed before any of that: it authenticates itself,
// and it has no CSRF to check because it has no ambient credential to abuse.
func (a *App) withMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MCPPath && !strings.HasPrefix(r.URL.Path, MCPPath+"/") {
			next.ServeHTTP(w, r)

			return
		}

		a.serveMCP(w, r)
	})
}

func (a *App) serveMCP(w http.ResponseWriter, r *http.Request) {
	secret := bearerToken(r)

	if secret == "" {
		a.refuseMCP(w, "This endpoint needs an access key. Create one on the panel's AI access page "+
			"and send it as `Authorization: Bearer <key>`.")

		return
	}

	token, err := a.Store.FindAPIToken(r.Context(), store.HashAPIToken(secret))
	if err != nil {
		a.Log.Warn("an MCP request used a key the panel does not know", "ip", r.RemoteAddr)
		a.refuseMCP(w, "That access key is not one this panel issued, or it has been revoked.")

		return
	}

	user, err := a.Store.FindUser(r.Context(), token.UserID)
	if err != nil {
		a.refuseMCP(w, "The account this key belongs to no longer exists.")

		return
	}

	a.Store.TouchAPIToken(token.ID)

	a.MCP.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpUserKey{}, user)))
}

// refuseMCP answers an unauthenticated call the way the protocol expects: a 401
// with a JSON-RPC error inside it, so a client shows the reason rather than
// "the server returned 401".
func (a *App) refuseMCP(w http.ResponseWriter, why string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="Ubuntu Panel"`)
	w.WriteHeader(http.StatusUnauthorized)

	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":%q}}`, why)
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))

	if value, found := strings.CutPrefix(header, "Bearer "); found {
		return strings.TrimSpace(value)
	}

	// Some clients can only set a plain header. The token is a bearer
	// credential either way, and refusing the request over the word in front of
	// it helps nobody.
	if header != "" && !strings.Contains(header, " ") {
		return header
	}

	return strings.TrimSpace(r.Header.Get("X-Panel-Token"))
}

// buildMCP fills the tool registry. Every tool is a thin call onto the same
// service the browser's own pages use — nothing here reaches the machine by a
// route the panel does not already own.
func (a *App) buildMCP() *mcp.Server {
	server := mcp.NewServer(config.Name, config.Version, mcpInstructions, a.Log)

	server.Add(
		a.mcpServerStatus(),
		a.mcpListSiteTypes(),
		a.mcpListSites(),
		a.mcpGetSite(),
		a.mcpCreateSite(),
		a.mcpDeleteSite(),
		a.mcpDeploySite(),
		a.mcpPullSite(),
		a.mcpRestartSite(),
		a.mcpSiteLogs(),
		a.mcpListFiles(),
		a.mcpReadFile(),
		a.mcpWriteFile(),
		a.mcpGetTask(),
		a.mcpListTasks(),
		a.mcpListDatabases(),
		a.mcpCreateDatabase(),
		a.mcpListApps(),
		a.mcpInstallApps(),
	)

	return server
}

// ------------------------------------------------------------------ helpers

// mcpSite resolves the `site` argument, which is an id or a domain: an
// assistant that has just created a site holds the id, and one working from
// what a person typed holds the domain.
func (a *App) mcpSite(ctx context.Context, args mcp.Args) (*store.Site, error) {
	reference := args.String("site")

	if reference == "" {
		reference = args.String("domain")
	}

	if reference == "" && args.Int("site_id") > 0 {
		reference = strconv.FormatInt(args.Int("site_id"), 10)
	}

	if reference == "" {
		return nil, errors.New("name the site, by id or by domain, in the `site` argument")
	}

	if id, err := strconv.ParseInt(reference, 10, 64); err == nil {
		site, err := a.Store.FindSite(ctx, id)
		if err != nil || site.Status == "deleting" {
			return nil, fmt.Errorf("no site with id %d", id)
		}

		return site, nil
	}

	site, err := a.Store.FindSiteByDomain(ctx, strings.ToLower(reference))
	if err != nil {
		return nil, fmt.Errorf("no site for %s — list_sites has the ones this panel hosts", reference)
	}

	return site, nil
}

// siteReport is what every site-shaped answer looks like: the row, plus where
// its deployment got to, because "did it work" is the next question every time.
func (a *App) siteReport(ctx context.Context, site *store.Site, detailed bool) map[string]any {
	report := site.Summary(detailed)

	if latest := a.Store.LatestTaskForSite(ctx, site.ID, false); latest > 0 {
		if task, err := a.Store.FindTask(ctx, latest); err == nil {
			report["latest_task"] = map[string]any{
				"id":           task.ID,
				"action":       task.Action,
				"status":       task.Status,
				"message":      task.Message,
				"current_step": task.CurrentStep,
				"progress":     task.Progress,
				"running":      task.Status == "running",
				"finished_at":  task.FinishedAt,
			}
		}
	}

	if detailed && site.DatabaseID > 0 {
		if database, err := a.Store.FindDatabase(ctx, site.DatabaseID); err == nil {
			report["database"] = database.Summary()
		}
	}

	return report
}

// tail keeps a long console readable in an assistant's context: the end of it,
// which is where the failure is, rather than the whole apt run.
func tail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}

	return "…(trimmed)…\n" + text[len(text)-limit:]
}

// ------------------------------------------------------------------- tools

func (a *App) mcpServerStatus() *mcp.Tool {
	return &mcp.Tool{
		Name:     "server_status",
		Title:    "Server status",
		ReadOnly: true,
		Description: "What this machine is and how it is doing: hostname, public IP, operating " +
			"system, installed apps, CPU, memory and disk. Read this before promising that a " +
			"domain can point here, or that a site type can be hosted.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			return map[string]any{
				"panel":      map[string]any{"name": config.Name, "version": config.Version},
				"system":     a.systemSummary(ctx),
				"resources":  system.Read().Summary(),
				"sites":      a.Store.SiteCount(ctx),
				"sites_root": strings.TrimRight(a.Config.SitesRoot, "/"),
			}, nil
		},
	}
}

func (a *App) mcpListSiteTypes() *mcp.Tool {
	return &mcp.Tool{
		Name:     "list_site_types",
		Title:    "List site types",
		ReadOnly: true,
		Description: "The kinds of site this panel deploys — WordPress, Laravel, Next.js, Node.js, " +
			"plain PHP, static HTML and the managed applications — and, for each, whether this " +
			"machine has what it needs right now. A type whose `ready` is false names the apps " +
			"install_apps has to put on the machine first.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			// Asked of the machine rather than of the rows: a stale `failed`
			// from a half-finished install would report a type as impossible on
			// a server that hosts it perfectly well.
			a.Catalog.DetectFresh(siteApps, 30*time.Second)

			types := make([]map[string]any, 0, len(config.SiteTypes))

			for _, kind := range config.SiteTypes {
				problem := a.missingRequirement(kind.Key)

				types = append(types, map[string]any{
					"key":           kind.Key,
					"label":         kind.Label,
					"description":   kind.Description,
					"runtime":       kind.Runtime,
					"web_directory": kind.WebDirectory,
					"database":      kind.Database,
					"proxied":       kind.Proxied,
					"managed":       kind.Managed,
					"build_command": kind.Build,
					"start_command": kind.Start,
					"ready":         problem == "",
					"blocked_by":    nullable(problem),
				})
			}

			return map[string]any{
				"types":            types,
				"php_versions":     config.PHPVersions,
				"default_php":      a.Store.PHPVersion(),
				"default_node":     a.Store.NodeVersion(),
				"installed_apps":   a.Store.InstalledServiceKeys(ctx),
				"public_ip":        system.PublicIP(),
				"dns_record_types": config.SiteDNSTypes,
			}, nil
		},
	}
}

func (a *App) mcpListSites() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_sites",
		Title:       "List sites",
		ReadOnly:    true,
		Description: "Every site this panel hosts, with its domain, type, status and URL.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			rows, err := a.Store.Sites(ctx)
			if err != nil {
				return nil, errors.New("the panel could not read its site list")
			}

			sites := make([]map[string]any, 0, len(rows))

			for _, site := range rows {
				sites = append(sites, site.Summary(false))
			}

			return map[string]any{"sites": sites, "count": len(sites)}, nil
		},
	}
}

func (a *App) mcpGetSite() *mcp.Tool {
	return &mcp.Tool{
		Name:     "get_site",
		Title:    "Get a site",
		ReadOnly: true,
		Description: "Everything the panel knows about one site: where its files are, what it " +
			"runs, whether it has a certificate, its database, the login the panel created for a " +
			"managed application, and how its most recent deployment ended. This is what to poll " +
			"after create_site or deploy_site.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			return a.siteReport(ctx, site, true), nil
		},
	}
}

func (a *App) mcpCreateSite() *mcp.Tool {
	return &mcp.Tool{
		Name:  "create_site",
		Title: "Create a site",
		Description: "Create and deploy a site: nginx vhost, runtime, database where the type " +
			"needs one, a Let's Encrypt certificate, and — for a git-backed site — a clone of the " +
			"repository followed by its build. Returns as soon as the work is queued, so poll " +
			"get_site until its status is `active` or `failed`. The domain must already resolve " +
			"to this server, or the certificate step fails and the site is served over HTTP.",
		Properties: map[string]mcp.Property{
			"type": {
				Description: "The site type. list_site_types has them and says which this machine can host.",
				Enum: []string{"wordpress", "laravel", "nextcloud", "n8n", "php", "nodejs",
					"nextjs", "static"},
			},
			"domain":      {Description: "The hostname the site answers on, like app.example.com."},
			"aliases":     {Type: "array", Description: "Extra hostnames the same site answers on."},
			"repository":  {Description: "A git URL to deploy from, for the types that are not installed by the panel. HTTPS, and public — the panel clones without credentials."},
			"branch":      {Description: "The branch to deploy.", Default: "main"},
			"php_version": {Description: "For a PHP site. Defaults to the panel's own default."},
			"web_directory": {Description: "The directory inside the repository nginx serves, " +
				"like `public`. Defaults to what the type uses."},
			"build_command": {Description: "For a Node.js or Next.js site. Defaults to the type's own."},
			"start_command": {Description: "For a Node.js or Next.js site. Defaults to the type's own."},
			"ssl":           {Type: "boolean", Description: "Request a certificate on deployment.", Default: true},
			"admin_email": {Description: "The administrator address for a managed application " +
				"like WordPress or Nextcloud. Defaults to the account this key belongs to."},
			"admin_user": {Description: "The administrator username for a managed application.", Default: "admin"},
			"admin_password": {Description: "The administrator password for a managed application. " +
				"Left out, the panel generates one and get_site hands it back."},
			"title": {Description: "The site title, for a managed application."},
		},
		Required: []string{"type", "domain"},
		Run:      a.mcpProvisionSite,
	}
}

// mcpProvisionSite is create_site's body, kept apart because it is the one tool
// that validates as much as a form does.
func (a *App) mcpProvisionSite(ctx context.Context, args mcp.Args) (any, error) {
	user := mcpUser(ctx)
	if user == nil {
		return nil, errors.New("this key is not attached to an account")
	}

	kind := strings.ToLower(args.String("type"))
	domain := strings.ToLower(args.String("domain"))

	meta := config.FindSiteType(kind)
	if meta == nil {
		return nil, fmt.Errorf("%q is not a site type this panel deploys — list_site_types has them", kind)
	}

	if !validDomain(domain) {
		return nil, fmt.Errorf("%q is not a domain — it has to look like example.com", domain)
	}

	if _, err := a.Store.FindSiteByDomain(ctx, domain); err == nil {
		return nil, fmt.Errorf("a site for %s already exists", domain)
	}

	php := args.StringOr("php_version", a.Store.PHPVersion())

	if !config.IsPHPVersion(php) {
		return nil, fmt.Errorf("%q is not a PHP version this panel installs; it has %v",
			php, config.PHPVersions)
	}

	// Asked of the machine rather than of the rows, for the same reason the
	// create form asks: a stale row would refuse a site this server can host.
	a.Catalog.Detect([]string{"node", "mysql", "wpcli", "php"})

	if problem := a.missingRequirement(kind); problem != "" {
		return nil, errors.New(problem + " (install_apps does that)")
	}

	webDirectory := strings.Trim(args.StringOr("web_directory", meta.WebDirectory), "/")
	if webDirectory != "" {
		webDirectory = "/" + webDirectory
	}

	aliases := []string{}

	for _, alias := range args.List("aliases") {
		alias = strings.ToLower(strings.TrimSpace(alias))

		if validDomain(alias) && alias != domain {
			aliases = append(aliases, alias)
		}
	}

	site := &store.Site{
		UserID:       user.ID,
		Domain:       domain,
		Type:         kind,
		Aliases:      aliases,
		RootPath:     strings.TrimRight(a.Config.SitesRoot, "/") + "/" + domain,
		WebDirectory: webDirectory,
		PHPVersion:   php,
		NodeVersion:  a.Store.NodeVersion(),
		SSL:          args.Bool("ssl", true),
		Repository:   args.String("repository"),
		Branch:       args.StringOr("branch", "main"),
		DNSType:      "A",
		DNSContent:   system.PublicIP(),
	}

	if meta.Managed && site.Repository != "" {
		return nil, fmt.Errorf("%s is installed by the panel itself, so it takes no repository",
			meta.Label)
	}

	if meta.Proxied {
		site.AppPort = a.Store.NextFreePort(ctx)
		site.StartCommand = args.StringOr("start_command", orDefault(meta.Start, "npm run start"))
		site.BuildCommand = args.StringOr("build_command", meta.Build)
	}

	if meta.AdminAccount {
		site.WPTitle = args.StringOr("title", store.DefaultWPTitle)
		site.WPAdminEmail = args.StringOr("admin_email", user.Email)
		site.WPAdminUser = args.StringOr("admin_user", "admin")
		site.WPAdminPassword = args.String("admin_password")
	}

	id, err := a.Store.CreateSite(ctx, site)
	if err != nil || id <= 0 {
		a.Log.Error("could not create a site record for an MCP call", "error", err)

		return nil, errors.New("the panel could not store the site")
	}

	if _, err := a.Queue.PushID(queue.SiteDeploy, "site", id); err != nil {
		a.Log.Error("could not queue an MCP deployment", "site", id, "error", err)

		return nil, errors.New("the site was stored but its deployment could not be queued")
	}

	return map[string]any{
		"site_id": id,
		"domain":  domain,
		"type":    kind,
		"status":  "pending",
		"url":     site.URL(),
		"queued":  true,
		"next": fmt.Sprintf("The deployment is running on the server's queue. Poll get_site with "+
			"site %d until its status is active or failed; get_task has the console if it fails.", id),
	}, nil
}

func (a *App) mcpDeleteSite() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_site",
		Title:       "Delete a site",
		Destructive: true,
		Description: "Remove a site: its vhost, its service, its DNS records, its database and — " +
			"unless told otherwise — its files. This cannot be undone, so confirm with the person " +
			"asking before calling it.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"delete_files": {Type: "boolean", Default: true,
				Description: "Remove the site's directory as well as its configuration."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			a.Store.MarkSiteDeleted(site.ID)

			a.Queue.Push(queue.SiteDelete, map[string]any{
				"site":  site.ID,
				"files": args.Bool("delete_files", true),
			})

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"deleted": true,
				"next":    "The teardown is running on the queue. The site is already gone from the panel.",
			}, nil
		},
	}
}

func (a *App) mcpDeploySite() *mcp.Tool {
	return &mcp.Tool{
		Name:  "deploy_site",
		Title: "Deploy a site again",
		Description: "Run a site's deployment from the start: write its vhost, install its runtime, " +
			"build it, and request its certificate. This is what fixes a site whose first " +
			"deployment failed, and what applies a change to its configuration.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			a.Queue.PushID(queue.SiteDeploy, "site", site.ID)

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"queued":  true,
				"next":    "Poll get_site until its status settles.",
			}, nil
		},
	}
}

func (a *App) mcpPullSite() *mcp.Tool {
	return &mcp.Tool{
		Name:  "pull_site",
		Title: "Pull a site's repository",
		Description: "Fetch the site's git repository again and rebuild it, without touching its " +
			"nginx configuration or its certificate. This is the ordinary way to ship a change.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if strings.TrimSpace(site.Repository) == "" {
				return nil, fmt.Errorf("%s is not backed by a git repository", site.Domain)
			}

			a.Queue.PushID(queue.SitePull, "site", site.ID)

			return map[string]any{
				"site_id":    site.ID,
				"domain":     site.Domain,
				"repository": site.Repository,
				"branch":     site.Branch,
				"queued":     true,
			}, nil
		},
	}
}

func (a *App) mcpRestartSite() *mcp.Tool {
	return &mcp.Tool{
		Name:  "restart_site",
		Title: "Restart a site's service",
		Description: "Restart the systemd service behind a Node.js or Next.js site. Sites served " +
			"straight by nginx and PHP-FPM have no service to restart.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if !site.IsProxied() {
				return nil, fmt.Errorf("%s runs through PHP-FPM or is served from disk, so it has "+
					"no service to restart", site.Domain)
			}

			a.Queue.PushID(queue.SiteRestart, "site", site.ID)

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"service": site.ServiceName(),
				"queued":  true,
			}, nil
		},
	}
}

func (a *App) mcpSiteLogs() *mcp.Tool {
	return &mcp.Tool{
		Name:     "site_logs",
		Title:    "Read a site's logs",
		ReadOnly: true,
		Description: "The tail of nginx's access and error logs for a site, and of the " +
			"application's own log where the site runs as a service. This is where the reason for " +
			"a 500 or a 502 is.",
		Properties: map[string]mcp.Property{
			"site":  {Description: "The site's id, or its domain."},
			"lines": {Type: "integer", Description: "How many lines of each log to read.", Default: 100},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			lines := int(args.Int("lines"))
			if lines <= 0 || lines > 500 {
				lines = 100
			}

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"logs":    a.siteLogFiles(site, lines),
			}, nil
		},
	}
}

func (a *App) mcpListFiles() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_files",
		Title:       "List a site's files",
		ReadOnly:    true,
		Description: "List one directory inside a site. Paths are relative to the site's own root, so `/` is the site itself.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"path": {Description: "The directory to list, relative to the site root.", Default: "/"},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			entries, where, err := a.Sites.ListFiles(site, args.StringOr("path", "/"))
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id": site.ID,
				"root":    site.RootPath,
				"path":    where,
				"entries": entries,
			}, nil
		},
	}
}

func (a *App) mcpReadFile() *mcp.Tool {
	return &mcp.Tool{
		Name:        "read_file",
		Title:       "Read a site's file",
		ReadOnly:    true,
		Description: "Read one text file from inside a site — a .env, a configuration file, a template.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"path": {Description: "The file, relative to the site root."},
		},
		Required: []string{"site", "path"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			contents, err := a.Sites.ReadFile(site, args.String("path"))
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"site_id":  site.ID,
				"path":     args.String("path"),
				"contents": contents,
			}, nil
		},
	}
}

func (a *App) mcpWriteFile() *mcp.Tool {
	return &mcp.Tool{
		Name:  "write_file",
		Title: "Write a site's file",
		Description: "Write a text file inside a site, creating it or replacing it whole. The " +
			"file is written as the site's own user, so the application can read it.",
		Properties: map[string]mcp.Property{
			"site":     {Description: "The site's id, or its domain."},
			"path":     {Description: "The file, relative to the site root."},
			"contents": {Description: "The whole of the file's new contents."},
		},
		Required: []string{"site", "path", "contents"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			path := args.String("path")

			if !args.Has("contents") {
				return nil, errors.New("`contents` is the whole of the new file and has to be sent")
			}

			if err := a.Sites.WriteFile(site, path, args.String("contents")); err != nil {
				return nil, err
			}

			return map[string]any{"site_id": site.ID, "path": path, "written": true}, nil
		},
	}
}

func (a *App) mcpGetTask() *mcp.Tool {
	return &mcp.Tool{
		Name:     "get_task",
		Title:    "Get a task",
		ReadOnly: true,
		Description: "One task — a deployment, an install, a teardown — with its steps and the " +
			"tail of its console output. This is where the reason a deployment failed is written.",
		Properties: map[string]mcp.Property{
			"task_id": {Type: "integer", Description: "The task's id, from get_site or list_tasks."},
			"output_bytes": {Type: "integer", Default: 4000,
				Description: "How much of the end of the console to return."},
		},
		Required: []string{"task_id"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			id := args.Int("task_id")

			task, err := a.Store.FindTask(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("no task with id %d", id)
			}

			limit := int(args.Int("output_bytes"))
			if limit <= 0 {
				limit = 4000
			}

			console := task.Console(0)
			console["output"] = tail(task.Output, limit)

			return console, nil
		},
	}
}

func (a *App) mcpListTasks() *mcp.Tool {
	return &mcp.Tool{
		Name:     "list_tasks",
		Title:    "List recent tasks",
		ReadOnly: true,
		Description: "The panel's recent work, newest first: deployments, installs, certificates " +
			"and teardowns, with how each ended.",
		Properties: map[string]mcp.Property{
			"limit": {Type: "integer", Description: "How many to return.", Default: 20},
			"site":  {Description: "Only this site's tasks, by id or domain."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			limit := int(args.Int("limit"))
			if limit <= 0 || limit > 100 {
				limit = 20
			}

			var (
				tasks []*store.Task
				err   error
			)

			if args.String("site") != "" {
				site, siteErr := a.mcpSite(ctx, args)
				if siteErr != nil {
					return nil, siteErr
				}

				tasks, err = a.Store.TasksForSite(ctx, site.ID, limit)
			} else {
				tasks, err = a.Store.RecentTasks(ctx, limit)
			}

			if err != nil {
				return nil, errors.New("the panel could not read its task list")
			}

			return map[string]any{"tasks": tasks, "count": len(tasks)}, nil
		},
	}
}

func (a *App) mcpListDatabases() *mcp.Tool {
	return &mcp.Tool{
		Name:     "list_databases",
		Title:    "List databases",
		ReadOnly: true,
		Description: "Every database the panel manages, with its engine, its user and its " +
			"password — the ones it created for a site included.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			rows, err := a.Store.Databases(ctx)
			if err != nil {
				return nil, errors.New("the panel could not read its database list")
			}

			list := make([]map[string]any, 0, len(rows))

			for _, database := range rows {
				list = append(list, database.Summary())
			}

			return map[string]any{"databases": list, "count": len(list)}, nil
		},
	}
}

func (a *App) mcpCreateDatabase() *mcp.Tool {
	return &mcp.Tool{
		Name:  "create_database",
		Title: "Create a database",
		Description: "Create a database and a user with every privilege on it. A site the panel " +
			"deploys gets one of these by itself, so this is for an application that needs a " +
			"second one, or for one the panel is not deploying.",
		Properties: map[string]mcp.Property{
			"name":     {Description: "The database name: letters, digits and underscore."},
			"engine":   {Description: "The engine.", Enum: []string{"mysql", "mongodb"}, Default: "mysql"},
			"username": {Description: "The user to create. Derived from the name when left out."},
			"password": {Description: "At least eight characters. Generated when left out."},
		},
		Required: []string{"name"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			engine := args.StringOr("engine", "mysql")
			name := args.String("name")

			if config.FindEngine(engine) == nil {
				return nil, fmt.Errorf("%q is not a database engine this panel manages", engine)
			}

			if !validIdentifier(name, 60) {
				return nil, errors.New("a database name is letters, digits and underscore, up to 60 characters")
			}

			username := args.String("username")

			if username != "" && !validIdentifier(username, 32) {
				return nil, errors.New("a username is letters, digits and underscore, up to 32 characters")
			}

			password := args.String("password")

			if password != "" && len(password) < 8 {
				return nil, errors.New("a password of fewer than eight characters is not stored")
			}

			if !a.Store.ServiceInstalled(engine) {
				return nil, fmt.Errorf("%s is not installed on this machine — install_apps does that",
					config.EngineLabel(engine))
			}

			if a.Store.DatabaseExists(ctx, engine, name) {
				return nil, fmt.Errorf("a %s database called %s already exists", engine, name)
			}

			if username == "" {
				username = truncate(name, 26) + "_u"
			}

			if password == "" {
				password = crypto.Password(24)
			}

			charset := ""
			if engine == "mysql" {
				charset = "utf8mb4"
			}

			id, err := a.Store.CreateDatabase(ctx, &store.Database{
				UserID:   user.ID,
				Engine:   engine,
				Name:     name,
				Username: username,
				Password: password,
				Charset:  charset,
			})
			if err != nil || id <= 0 {
				a.Log.Error("could not create a database record for an MCP call", "error", err)

				return nil, errors.New("the panel could not store the database")
			}

			a.Queue.PushID(queue.DatabaseCreate, "database", id)

			return map[string]any{
				"database_id": id,
				"engine":      engine,
				"name":        name,
				"username":    username,
				"password":    password,
				"host":        "127.0.0.1",
				"queued":      true,
				"next":        "The database is being created on the queue; list_databases has its status.",
			}, nil
		},
	}
}

func (a *App) mcpListApps() *mcp.Tool {
	return &mcp.Tool{
		Name:     "list_apps",
		Title:    "List the server's apps",
		ReadOnly: true,
		Description: "What the panel can install on this machine — nginx, PHP, MariaDB, Node.js, " +
			"Redis, Certbot and the rest — and the state each is in.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			a.Catalog.SyncRows()

			return map[string]any{"apps": a.Catalog.Summary(ctx)}, nil
		},
	}
}

func (a *App) mcpInstallApps() *mcp.Tool {
	return &mcp.Tool{
		Name:  "install_apps",
		Title: "Install apps",
		Description: "Install one or more of the apps from list_apps. This is what a site type " +
			"whose `ready` is false needs first. Asynchronous: the installs run on the queue, and " +
			"list_apps says when they are done.",
		Properties: map[string]mcp.Property{
			"keys": {Type: "array", Description: "The app keys to install, from list_apps."},
			"force": {Type: "boolean", Default: false,
				Description: "Install again even where the panel believes the app is already there."},
		},
		Required: []string{"keys"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			keys := []string{}
			unknown := []string{}

			for _, key := range args.List("keys") {
				if catalog.Exists(key) {
					keys = append(keys, key)
				} else {
					unknown = append(unknown, key)
				}
			}

			if len(unknown) > 0 {
				return nil, fmt.Errorf("this panel has no app called %s — list_apps has the keys",
					strings.Join(unknown, ", "))
			}

			if len(keys) == 0 {
				return nil, errors.New("name at least one app to install")
			}

			queued := a.Catalog.Queue(keys, args.Bool("force", false))

			if queued == 0 {
				return map[string]any{
					"queued": 0,
					"note":   "Everything asked for is already installed or already queued.",
				}, nil
			}

			a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

			return map[string]any{
				"queued": queued,
				"apps":   keys,
				"next":   "The installs are running on the queue; list_apps says when they are done.",
			}, nil
		},
	}
}

// ------------------------------------------------------------ the panel page

// panelURL is where this panel answers from outside the machine, which is what
// a client is pointed at.
//
// The hostname somebody set on the Settings page when there is one, because
// that is the name with a certificate on it. Otherwise the public IP and the
// port the panel binds — an assistant's client cannot reach 127.0.0.1, and an
// address without the port is one that connects to nothing.
func (a *App) panelURL() string {
	domain := a.Store.Setting("panel_domain", "")

	if domain != "" {
		if a.Store.SettingBool("panel_domain_tls", false) {
			return "https://" + domain
		}

		return "http://" + domain
	}

	host := orDefault(system.PublicIP(), a.Config.BindHost)

	return "http://" + host + ":" + strconv.Itoa(a.Config.BindPort)
}

// aiIndex is the page that issues keys and says how to connect a client to
// them.
func (a *App) aiIndex(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	tokens, err := a.Store.APITokens(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, "could not list the access keys", err)

		return
	}

	list := make([]map[string]any, 0, len(tokens))

	for _, token := range tokens {
		list = append(list, map[string]any{
			"id":           token.ID,
			"name":         token.Name,
			"prefix":       token.Prefix,
			"last_used_at": nullable(token.LastUsedAt),
			"created_at":   token.CreatedAt,
		})
	}

	tools := make([]map[string]any, 0)

	for _, tool := range a.MCP.Tools() {
		tools = append(tools, map[string]any{
			"name":        tool.Name,
			"title":       tool.Title,
			"description": tool.Description,
			"read_only":   tool.ReadOnly,
		})
	}

	issued := ""
	if sess := session.FromContext(r.Context()); sess != nil {
		issued = sess.Flashed("issued_token")
	}

	a.Render.Render(w, r, "System/Ai", inertia.Props{
		"tokens": list,
		"tools":  tools,
		// Where a client has to point. The panel's own hostname when one was
		// set, and the public IP with the port when not — which is what a panel
		// nobody has put a domain in front of actually answers on.
		"endpoint": a.panelURL() + MCPPath,
		// Shown once, on the response that created it. The panel keeps a hash,
		// so this is the only time the key exists anywhere it can be read.
		"issued": nullable(issued),
	})
}

func (a *App) aiStoreToken(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)

		return
	}

	name := stringInput(input, "name", "AI assistant")

	if len(name) > 60 {
		name = name[:60]
	}

	secret, hash := store.NewAPIToken()

	if _, err := a.Store.CreateAPIToken(r.Context(), user.ID, name, hash,
		store.TokenLabel(secret)); err != nil {
		a.Log.Error("could not store an access key", "error", err)
		a.failBack(w, r, "The access key could not be stored.")

		return
	}

	// Carried across the redirect in the flash bag, because it is never
	// readable again after this response.
	sess.Flash("issued_token", secret)
	sess.Flash("success", "The key was created. Copy it now — the panel keeps only a hash of it.")

	inertia.Redirect(w, r, "/ai")
}

func (a *App) aiDestroyToken(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	a.Store.DeleteAPIToken(routeID(r, "token"), user.ID)

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("danger", "The key was revoked. Any assistant using it is signed out.")
	}

	inertia.Redirect(w, r, "/ai")
}
