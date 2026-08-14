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
	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/mcp"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
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

Use the panel's defaults. Every tool here does what the same action does in the panel's own pages, ` +
	`with the same defaults, and an argument you leave out is filled in the way a person pressing ` +
	`the button in the browser would get it. Send what the person asked for and nothing else: an ` +
	`argument invented to be cautious — DNS management off, TLS off, a different branch — produces ` +
	`a site that is not the one they asked for, and the panel cannot tell the difference between ` +
	`that and a deliberate choice.

Work the way the panel works. Anything the panel does, do with the panel's own tools — creating ` +
	`sites, deploying them, installing apps, creating databases, issuing certificates. The panel ` +
	`owns those files: the nginx vhosts under /etc/nginx/sites-available, the systemd units for ` +
	`proxied sites, the site directories under /var/www, the database rows that pair with a real ` +
	`database. A site put there by hand is invisible to the panel and will be overwritten or ` +
	`orphaned by the next deployment. run_command is for what no other tool covers — inspecting a ` +
	`service, tailing a log, running composer or wp inside a site — not for redoing the panel's job.

Creating or deleting a site is asynchronous: the tool returns immediately with a site id, and the ` +
	`work — cloning the repository, installing the runtime, writing the nginx vhost, requesting the ` +
	`TLS certificate — runs on the server's queue and takes anywhere from thirty seconds to several ` +
	`minutes. Poll get_site for the site's status and its latest task, and get_task for the console ` +
	`output of a task that failed. Do not report a site as ready until its status is "active".

Before creating a site, check list_site_types: it says what this machine can host right now and ` +
	`which apps are missing for the rest. Missing apps can be installed with install_apps, which is ` +
	`also asynchronous.

DNS is part of creating a site, not a separate errand. Leave manage_dns alone and the panel finds ` +
	`the connected provider that holds the domain, publishes the records during the deployment, and ` +
	`removes them when the site is deleted. create_site's answer says which provider it used — or, ` +
	`when no connected credential covers the domain, exactly which record the person has to add and ` +
	`where it has to point. Tell them that plainly; a domain that resolves somewhere else is why a ` +
	`certificate fails and why the site does not answer.

The person who owns this panel decides what you may do, on its AI access page. A tool that answers ` +
	`"not allowed" is a setting, not a fault — say so and stop rather than looking for another way ` +
	`round. Where a tool asks you to confirm first, ask the person in your own words and wait for a ` +
	`real answer before calling it again.`

// The two switches on the AI access page.
//
// Both default to the safe answer on a panel that has never been asked: the
// server is on, because a key that answers nothing is a support ticket, and
// destructive work asks first, because the alternative is an assistant that
// deletes a site it decided was unused.
const (
	mcpEnabledSetting = "mcp_enabled"
	mcpConfirmSetting = "mcp_confirm_destructive"
)

// mcpNameSetting is what a client introduces this panel as.
//
// One machine can have several of these connected — a staging box, a client's
// server, your own — and every one of them is called Ubuntu Panel until
// somebody says otherwise. The name is what the assistant reads on connection
// and what the configuration file keys on, so it is worth being able to change.
const mcpNameSetting = "mcp_server_name"

func (a *App) mcpEnabled() bool  { return a.Store.SettingBool(mcpEnabledSetting, true) }
func (a *App) mcpConfirms() bool { return a.Store.SettingBool(mcpConfirmSetting, true) }

func (a *App) mcpServerName() string {
	return a.Store.Setting(mcpNameSetting, config.Name)
}

// mcpServerSlug is the same name as a configuration key: lower case, words
// joined by dashes, and never empty.
func mcpServerSlug(name string) string {
	var slug strings.Builder

	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			slug.WriteRune(character)
		default:
			// Collapsed rather than repeated, so "My  panel!" is "my-panel".
			if text := slug.String(); text != "" && !strings.HasSuffix(text, "-") {
				slug.WriteByte('-')
			}
		}
	}

	trimmed := strings.Trim(slug.String(), "-")

	if trimmed == "" {
		return "ubuntu-panel"
	}

	return trimmed
}

// What an assistant is allowed to do, one line per tick box on the AI access
// page. Every tool names one of these, and a tool whose capability is not
// granted is neither offered nor run.
const (
	mcpAllowAllSetting    = "mcp_allow_all"
	mcpPermissionsSetting = "mcp_permissions"
	capSitesRead          = "sites.read"
	capSitesWrite         = "sites.write"
	capSitesDelete        = "sites.delete"
	capFilesRead          = "files.read"
	capFilesWrite         = "files.write"
	capDatabases          = "databases"
	capApps               = "apps"
	capShell              = "shell"
)

// mcpCapabilities is the checklist, in the order the page shows it: what it is
// called, what it means, and whether it is the kind of thing that cannot be
// undone.
var mcpCapabilities = []struct {
	Key, Label, Help string
	Destructive      bool
}{
	{Key: capSitesRead, Label: "Look around",
		Help: "List sites, read their status and deployment logs, and see how the server is doing."},
	{Key: capSitesWrite, Label: "Create and deploy sites",
		Help: "Create a site from a repository or a template, deploy it again, pull it, restart it."},
	{Key: capSitesDelete, Label: "Delete sites", Destructive: true,
		Help: "Remove a site: its files, its database, its DNS records and its certificate."},
	{Key: capFilesRead, Label: "Read a site's files",
		Help: "Open files inside a site — configuration, templates, a .env with its passwords in it."},
	{Key: capFilesWrite, Label: "Write a site's files", Destructive: true,
		Help: "Create and replace files inside a site."},
	{Key: capDatabases, Label: "Databases",
		Help: "List databases with their credentials, and create new ones."},
	{Key: capApps, Label: "Install apps",
		Help: "Install what the machine is missing — PHP, MariaDB, Node.js, Redis and the rest."},
	{Key: capShell, Label: "Run any command", Destructive: true,
		Help: "Run shell commands as the panel's own user, which has passwordless sudo. " +
			"This is everything the machine can do, so it is also every way it can be broken."},
}

// mcpAllows reports whether a capability is granted.
//
// A panel that has never been to this page allows everything: the assistant is
// the point of the feature, and a fresh install where every tool answers "not
// allowed" reads as broken rather than as careful. Narrowing it is one visit to
// the page; the confirm-first switch is what stands in front of the damage
// until then.
func (a *App) mcpAllows(capability string) bool {
	if capability == "" || a.Store.SettingBool(mcpAllowAllSetting, true) {
		return true
	}

	for _, granted := range strings.Split(a.Store.Setting(mcpPermissionsSetting, ""), ",") {
		if strings.TrimSpace(granted) == capability {
			return true
		}
	}

	return false
}

// mcpAllowsTool is what the registry asks before it offers or runs anything.
func (a *App) mcpAllowsTool(ctx context.Context, tool *mcp.Tool) bool {
	return a.mcpAllows(tool.Capability)
}

// requireConfirmation is the confirm-first switch.
//
// It cannot force a human to be asked — nothing on this side of the connection
// can — but it stops the one-step version, where a sentence about tidying up
// becomes a deleted site inside one tool call. The assistant has to come back a
// second time and say, in the call itself, that the person agreed.
func (a *App) requireConfirmation(args mcp.Args, what string) error {
	if !a.mcpConfirms() || args.Bool("confirm", false) {
		return nil
	}

	return fmt.Errorf("%s cannot be undone, and this panel is set to confirm first. Tell the "+
		"person exactly what will happen, in your own words, and wait for them to say yes — "+
		"then call this tool again with confirm: true. Do not set confirm yourself.", what)
}

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
	// The switch on the AI access page. Checked before the key, so turning the
	// server off is one decision that ends every connection rather than a race
	// with whoever still holds a key.
	if !a.mcpEnabled() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)

		fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,`+
			`"message":"This panel's MCP server is switched off. Whoever owns the panel can turn `+
			`it back on under AI access."}}`)

		return
	}

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
		a.mcpListDNSAccounts(),
		a.mcpSyncDNS(),
		a.mcpListApps(),
		a.mcpInstallApps(),
		a.mcpRunCommand(),
	)

	server.Allow = a.mcpAllowsTool
	server.NameFor = func(context.Context) string { return a.mcpServerName() }

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
		Name:       "server_status",
		Capability: capSitesRead,
		Title:      "Server status",
		ReadOnly:   true,
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
		Name:       "list_site_types",
		Capability: capSitesRead,
		Title:      "List site types",
		ReadOnly:   true,
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
		Capability:  capSitesRead,
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
		Name:       "get_site",
		Capability: capSitesRead,
		Title:      "Get a site",
		ReadOnly:   true,
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
		Name:       "create_site",
		Capability: capSitesWrite,
		Title:      "Create a site",
		Description: "Create and deploy a site: DNS records, nginx vhost, runtime, database where " +
			"the type needs one, a certificate, and — for a git-backed site — a clone of the " +
			"repository followed by its build. Returns as soon as the work is queued, so poll " +
			"get_site until its status is `active` or `failed`.\n\n" +
			"Send the type, the domain, and whatever else the person actually asked for. Leave " +
			"everything else out: every argument here has the same default as the panel's own " +
			"create form, and a site made through this tool with nothing but a type and a domain " +
			"is the same site a person gets by opening that form and pressing the button. Do not " +
			"turn things off to be careful — switching manage_dns or ssl off produces a site that " +
			"resolves nowhere or has no certificate, which is not a safer site, only a broken one.",
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
			"manage_dns": {Type: "boolean", Default: true,
				Description: "Publish this site's DNS records at whichever connected provider " +
					"holds the domain, and take them away again when the site is deleted. Left on, " +
					"the panel finds the credential itself — there is nothing to look up first."},
			"dns_account_id": {Type: "integer",
				Description: "Which DNS credential to publish with, from list_dns_accounts. Only " +
					"worth sending when the domain is in more than one of them."},
			"dns_proxied": {Type: "boolean", Default: true,
				Description: "For Cloudflare: serve the site through their proxy. Every other " +
					"provider ignores it."},
			"dns_type": {Description: "The record type to publish.", Default: "A",
				Enum: []string{"A", "AAAA", "CNAME"}},
			"dns_content": {Description: "What the record points at. Defaults to this server's public IP."},
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

	// The same request the create form builds, handed to the same place. An
	// argument the assistant did not send is left unset on purpose: prepareSite
	// then applies the panel's own default for it, which is what "do it the way
	// the panel does it" has to mean if it is to stay true.
	request := siteRequest{
		Type:          args.String("type"),
		Domain:        args.String("domain"),
		Aliases:       args.List("aliases"),
		PHPVersion:    args.String("php_version"),
		WebDirectory:  args.String("web_directory"),
		Repository:    args.String("repository"),
		Branch:        args.String("branch"),
		StartCommand:  args.String("start_command"),
		BuildCommand:  args.String("build_command"),
		DNSAccountID:  args.Int("dns_account_id"),
		DNSType:       args.String("dns_type"),
		DNSContent:    args.String("dns_content"),
		AdminUser:     args.String("admin_user"),
		AdminEmail:    args.String("admin_email"),
		AdminPassword: args.String("admin_password"),
		Title:         args.String("title"),
	}

	if args.Has("ssl") {
		request.SSL = boolPointer(args.Bool("ssl", true))
	}

	if args.Has("manage_dns") {
		request.ManageDNS = boolPointer(args.Bool("manage_dns", true))
	}

	if args.Has("dns_proxied") {
		request.DNSProxied = boolPointer(args.Bool("dns_proxied", true))
	}

	site, err := a.prepareSite(ctx, user, request)
	if err != nil {
		return nil, err
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

	answer := map[string]any{
		"site_id":    id,
		"domain":     site.Domain,
		"type":       site.Type,
		"status":     "pending",
		"url":        site.URL(),
		"queued":     true,
		"manage_dns": site.ManageDNS,
		"next": fmt.Sprintf("The deployment is running on the server's queue. Poll get_site with "+
			"site %d until its status is active or failed; get_task has the console if it fails.", id),
	}

	if site.ManageDNS && site.DNSAccountID > 0 {
		proxied := site.DNSProxied

		if account, err := a.Store.FindDNSAccount(ctx, site.DNSAccountID, user.ID); err == nil {
			answer["dns"] = map[string]any{
				"provider":   dns.ProviderLabel(account.Provider),
				"credential": account.Label,
				"record": fmt.Sprintf("%s %s -> %s", site.DNSType, site.Domain,
					site.DNSContent),
				"proxied": proxied && dns.SupportsProxy(account.Provider),
				"note": "The panel publishes this during the deployment and removes it again " +
					"when the site is deleted — nothing to add by hand. Where the provider's " +
					"proxy is on and already terminating TLS, the deployment also skips asking " +
					"Let's Encrypt for a certificate nobody would be shown. get_site's tls_mode " +
					"says which happened, and get_task has the DNS step's own output.",
			}
		}

		return answer, nil
	}

	// No credential to publish with. The site is still created — the same as
	// the form with the DNS box unticked — and the answer carries the sentence
	// to read out, because this is the one case where somebody has to go and do
	// something before the site can answer at all.
	answer["dns"] = map[string]any{
		"managed": false,
		"reason": "no DNS provider is connected to this panel, so its records have to be added " +
			"wherever the domain's DNS lives",
		"tell_the_person": fmt.Sprintf("Add a %s record for %s pointing at %s wherever this "+
			"domain's DNS is managed. Until that resolves here, the site will not answer and its "+
			"certificate cannot be issued.", site.DNSType, site.Domain, site.DNSContent),
	}

	return answer, nil
}

func (a *App) mcpDeleteSite() *mcp.Tool {
	return &mcp.Tool{
		Name:        "delete_site",
		Capability:  capSitesDelete,
		Title:       "Delete a site",
		Destructive: true,
		Description: "Remove a site: its vhost, its service, its DNS records, its database and — " +
			"unless told otherwise — its files. This cannot be undone, so confirm with the person " +
			"asking before calling it.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
			"delete_files": {Type: "boolean", Default: true,
				Description: "Remove the site's directory as well as its configuration."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this removes and has said yes."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if err := a.requireConfirmation(args, "Deleting "+site.Domain); err != nil {
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
		Name:       "deploy_site",
		Capability: capSitesWrite,
		Title:      "Deploy a site again",
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
		Name:       "pull_site",
		Capability: capSitesWrite,
		Title:      "Pull a site's repository",
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
		Name:       "restart_site",
		Capability: capSitesWrite,
		Title:      "Restart a site's service",
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
		Name:       "site_logs",
		Capability: capSitesRead,
		Title:      "Read a site's logs",
		ReadOnly:   true,
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
		Capability:  capFilesRead,
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
		Capability:  capFilesRead,
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
		Name:       "write_file",
		Capability: capFilesWrite,
		Title:      "Write a site's file",
		Description: "Write a text file inside a site, creating it or replacing it whole. The " +
			"file is written as the site's own user, so the application can read it.",
		Properties: map[string]mcp.Property{
			"site":     {Description: "The site's id, or its domain."},
			"path":     {Description: "The file, relative to the site root."},
			"contents": {Description: "The whole of the file's new contents."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told which file is being replaced and has said yes."},
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

			// Replacing a file loses what was in it, so it asks first — but only
			// for a file that is already there. Creating one takes nothing away.
			if _, err := a.Sites.ReadFile(site, path); err == nil {
				if err := a.requireConfirmation(args,
					"Replacing "+path+" in "+site.Domain); err != nil {
					return nil, err
				}
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
		Name:       "get_task",
		Capability: capSitesRead,
		Title:      "Get a task",
		ReadOnly:   true,
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
		Name:       "list_tasks",
		Capability: capSitesRead,
		Title:      "List recent tasks",
		ReadOnly:   true,
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
		Name:       "list_databases",
		Capability: capDatabases,
		Title:      "List databases",
		ReadOnly:   true,
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
		Name:       "create_database",
		Capability: capDatabases,
		Title:      "Create a database",
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

func (a *App) mcpListDNSAccounts() *mcp.Tool {
	return &mcp.Tool{
		Name:       "list_dns_accounts",
		Title:      "List DNS credentials",
		Capability: capSitesRead,
		ReadOnly:   true,
		Description: "The DNS providers connected to this panel. create_site finds the right one " +
			"by itself, so this is for saying which are connected, or for picking between two " +
			"that both hold the domain.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			accounts, err := a.Store.DNSAccounts(ctx, user.ID)
			if err != nil {
				return nil, errors.New("the panel could not read its DNS credentials")
			}

			list := make([]map[string]any, 0, len(accounts))

			for _, account := range accounts {
				list = append(list, map[string]any{
					"id":             account.ID,
					"label":          account.Label,
					"provider":       account.Provider,
					"provider_label": dns.ProviderLabel(account.Provider),
					"supports_proxy": dns.SupportsProxy(account.Provider),
					"verified_at":    nullable(account.VerifiedAt),
					"sites_count":    account.SitesCount,
				})
			}

			return map[string]any{"dns_accounts": list, "count": len(list)}, nil
		},
	}
}

func (a *App) mcpSyncDNS() *mcp.Tool {
	return &mcp.Tool{
		Name:       "sync_dns",
		Title:      "Publish a site's DNS records",
		Capability: capSitesWrite,
		Description: "Write this site's records at its DNS provider again — after changing what " +
			"they point at, or when the deployment's own attempt failed. Only for a site the panel " +
			"manages DNS for; the answer says what it wrote.",
		Properties: map[string]mcp.Property{
			"site": {Description: "The site's id, or its domain."},
		},
		Required: []string{"site"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			site, err := a.mcpSite(ctx, args)
			if err != nil {
				return nil, err
			}

			if !site.ManageDNS || site.DNSAccountID <= 0 {
				return nil, fmt.Errorf("the panel does not manage DNS for %s — no connected "+
					"credential held the domain when it was created. Connect the provider on the "+
					"panel's DNS page, then create the site again, or add the record by hand",
					site.Domain)
			}

			written, err := dns.SyncForSite(ctx, a.Store, site)
			if err != nil {
				return nil, err
			}

			system.ForgetResolution(site.Domain)

			return map[string]any{
				"site_id": site.ID,
				"domain":  site.Domain,
				"written": written,
			}, nil
		},
	}
}

func (a *App) mcpListApps() *mcp.Tool {
	return &mcp.Tool{
		Name:       "list_apps",
		Capability: capSitesRead,
		Title:      "List the server's apps",
		ReadOnly:   true,
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
		Name:       "install_apps",
		Capability: capApps,
		Title:      "Install apps",
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

// readOnlyBinaries is the allowlist behind the confirm-first switch.
//
// The list is what the panel is willing to call harmless by inspection, and it
// is deliberately short: anything not on it needs a yes, including things that
// are obviously safe. Getting that wrong in this direction costs one extra
// question; getting it wrong the other way costs a server.
var readOnlyBinaries = map[string]bool{
	// No pagers and nothing interactive: `less` and `top` wait for a keypress
	// that is never coming, and the only thing that ends them is the timeout.
	"cat": true, "head": true, "tail": true, "grep": true, "egrep": true,
	"fgrep": true, "wc": true, "ls": true, "stat": true, "file": true, "find": true,
	"du": true, "df": true, "free": true, "uptime": true, "uname": true, "hostname": true,
	"whoami": true, "id": true, "date": true, "ps": true, "pgrep": true,
	"journalctl": true, "dmesg": true, "ss": true, "netstat": true, "ip": true,
	"lsblk": true, "printenv": true, "env": true, "dpkg-query": true, "apt-cache": true,
	"systemctl": true, "git": true, "nginx": true, "which": true, "readlink": true,
	"realpath": true, "sha256sum": true, "md5sum": true, "dig": true, "host": true,
	"nslookup": true, "ping": true,
}

// readOnlyCommand reports whether a command only reads.
//
// By inspection, and conservatively: a command that chains, redirects or
// substitutes is not judged at all, because the interesting half of
// `ls; rm -rf /` is the half that is not `ls`.
func readOnlyCommand(command string) bool {
	trimmed := strings.TrimSpace(command)

	if trimmed == "" || strings.ContainsAny(trimmed, ";&|><`\n") {
		return false
	}

	fields := strings.Fields(trimmed)

	// sudo does not make a command dangerous by itself, and refusing to look
	// past it would put `sudo tail -n 50 /var/log/syslog` behind a question.
	if fields[0] == "sudo" {
		fields = fields[1:]

		if len(fields) == 0 {
			return false
		}
	}

	name := fields[0]

	if strings.Contains(name, "/") {
		name = name[strings.LastIndex(name, "/")+1:]
	}

	if !readOnlyBinaries[name] {
		return false
	}

	// A reading binary can still be asked to write.
	for _, argument := range fields[1:] {
		switch argument {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf":
			return false
		}
	}

	// The ones whose subcommand is the whole question.
	switch name {
	case "systemctl":
		return len(fields) > 1 && map[string]bool{
			"status": true, "show": true, "cat": true, "list-units": true,
			"list-unit-files": true, "is-active": true, "is-enabled": true, "is-failed": true,
		}[fields[1]]

	case "git":
		return len(fields) > 1 && map[string]bool{
			"status": true, "log": true, "diff": true, "show": true, "branch": true,
			"remote": true, "config": true, "describe": true, "rev-parse": true,
		}[fields[1]]

	case "nginx":
		return len(fields) > 1 && (fields[1] == "-t" || fields[1] == "-T" || fields[1] == "-v")

	case "ip":
		// `ip addr` and `ip route show` read the machine; `ip link set …`
		// changes it, and the difference is the verb after the object.
		return len(fields) < 3 || fields[2] == "show" || fields[2] == "list"
	}

	return true
}

// panelOwnedWork is the panel's own job, done by hand.
//
// Not a safety check — it is a correctness one. A vhost written into
// sites-available, a unit dropped into systemd, a certificate fetched by a
// hand-run certbot: each of those works for an afternoon and is then overwritten
// by the next deployment, or outlives the site it belonged to because no row
// knows about it. The tool that owns the work is named instead.
func panelOwnedWork(command string) string {
	lower := strings.ToLower(command)

	// Reading any of it is fine and often the point.
	writes := strings.ContainsAny(command, ">|") ||
		containsWord(lower, "tee", "rm", "mv", "cp", "sed", "install", "ln", "truncate", "chown", "chmod")

	switch {
	case writes && (strings.Contains(lower, "/etc/nginx/sites-available") ||
		strings.Contains(lower, "/etc/nginx/sites-enabled")):
		return "writing an nginx vhost by hand — the panel writes those from the site's row, and " +
			"the next deployment overwrites whatever is there. Use create_site, or the site tools " +
			"for a site that already exists"

	case writes && strings.Contains(lower, "/etc/systemd/system/ubuntu-panel-"):
		return "writing a site's systemd unit by hand — the panel writes those, and restart_site " +
			"or deploy_site is what changes them"

	case containsWord(lower, "certbot"):
		return "asking certbot for a certificate directly — the panel requests and renews them per " +
			"site, and records what it got. deploy_site does it, and get_site says whether it worked"

	case containsWord(lower, "mysqladmin") && containsWord(lower, "create"),
		strings.Contains(lower, "create database"):
		return "creating a database by hand — the panel keeps a row beside every database it " +
			"creates, with the credentials it generated. create_database does both"
	}

	return ""
}

// containsWord reports whether any of these appear as a whole word.
func containsWord(text string, words ...string) bool {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '|' || r == '&' ||
			r == '(' || r == ')' || r == '"' || r == '\''
	})

	for _, field := range fields {
		for _, word := range words {
			if field == word {
				return true
			}
		}
	}

	return false
}

func (a *App) mcpRunCommand() *mcp.Tool {
	return &mcp.Tool{
		Name:       "run_command",
		Title:      "Run a command",
		Capability: capShell,
		Description: "Run a shell command on the server. It runs as the panel's own user, which " +
			"has passwordless sudo, so anything the machine can do is reachable from here.\n\n" +
			"This is for what the other tools do not cover: inspecting a service, tailing a log, " +
			"running composer, npm or wp inside a site, restarting a daemon. It is not the way to " +
			"do what the panel does — creating sites, vhosts, systemd units, databases or " +
			"certificates by hand leaves the panel's records disagreeing with the machine, and the " +
			"next deployment overwrites the work.\n\n" +
			"Reading is free. Anything else needs the person's explicit yes first, sent back as " +
			"confirm: true. Every command is recorded in the panel's Logs, with its output.",
		Properties: map[string]mcp.Property{
			"command":   {Description: "The command line, run through bash."},
			"directory": {Description: "Where to run it. Defaults to /."},
			"timeout": {Type: "integer", Default: 120,
				Description: "Seconds to allow before it is killed. Up to 1800."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this command does and has said yes."},
		},
		Required: []string{"command"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			command := strings.TrimSpace(args.String("command"))
			if command == "" {
				return nil, errors.New("`command` is the command line to run")
			}

			if instead := panelOwnedWork(command); instead != "" {
				return nil, errors.New("that is " + instead + ". If you have a reason this is " +
					"genuinely different, say what it is to the person first")
			}

			if !readOnlyCommand(command) {
				if err := a.requireConfirmation(args, "Running `"+command+"`"); err != nil {
					return nil, err
				}
			}

			timeout := time.Duration(args.Int("timeout")) * time.Second

			switch {
			case timeout <= 0:
				timeout = 2 * time.Minute
			case timeout > 30*time.Minute:
				timeout = 30 * time.Minute
			}

			full := command

			if directory := args.String("directory"); directory != "" {
				full = "cd " + proc.Quote(directory) + " && " + command
			}

			// Recorded like everything else the panel does, so the Logs page
			// answers "what has the assistant been doing on my server".
			user := mcpUser(ctx)

			var userID int64
			if user != nil {
				userID = user.ID
			}

			taskID, _ := a.Store.CreateTask(ctx, "mcp", "command", userID, 0, proc.Redact(command))

			result := proc.Run(full, timeout)

			if taskID > 0 {
				a.Store.AppendTaskOutput(taskID, result.Output)

				status, message := "success", "Exit 0"

				if !result.OK() {
					status = "failed"
					message = fmt.Sprintf("Exit %d", result.Code)
				}

				if result.TimedOut {
					message = "Killed after " + timeout.String()
				}

				a.Store.FinishTask(taskID, status, message, 100)
			}

			// A command that failed is not a tool that failed: the exit status
			// and the output are the answer, and the assistant reads both.
			return map[string]any{
				"command":   command,
				"exit_code": result.Code,
				"timed_out": result.TimedOut,
				"output":    tail(result.Output, 20000),
				"task_id":   taskID,
			}, nil
		},
	}
}

// ------------------------------------------------------------ the panel page

// panelURL is where this panel answers from outside the machine, which is what
// a client is pointed at.
//
// The hostname somebody set on the Settings page when there is one, because
// that is the name with a certificate on it. Otherwise the public IP, on port
// 80.
//
// Not the port the panel binds. That is 127.0.0.1:8088, and it is loopback on
// purpose: nginx is the front door and proxies every path — /mcp included — from
// port 80. Printing the bind port here sent people to an address that is
// firewalled from the outside by design, and the symptom was a client that sat
// there until it timed out.
func (a *App) panelURL() string {
	domain := a.Store.Setting("panel_domain", "")

	if domain != "" {
		if a.Store.SettingBool("panel_domain_tls", false) {
			return "https://" + domain
		}

		return "http://" + domain
	}

	return "http://" + orDefault(system.PublicIP(), a.Config.BindHost)
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
			"id":     token.ID,
			"name":   token.Name,
			"prefix": token.Prefix,
			// A key issued before the panel kept this shows the name it answers
			// by now, which is the only one it has ever had.
			"server_name":  orDefault(token.ServerName, a.mcpServerName()),
			"last_used_at": nullable(token.LastUsedAt),
			"created_at":   token.CreatedAt,
		})
	}

	// The checklist, with what is ticked now. `allow_all` is its own answer
	// rather than every box ticked: a panel that has never been here allows
	// everything, and that is a different statement from somebody having chosen
	// each one.
	allowAll := a.Store.SettingBool(mcpAllowAllSetting, true)

	capabilities := make([]map[string]any, 0, len(mcpCapabilities))

	for _, capability := range mcpCapabilities {
		capabilities = append(capabilities, map[string]any{
			"key":         capability.Key,
			"label":       capability.Label,
			"help":        capability.Help,
			"destructive": capability.Destructive,
			"granted":     a.mcpAllows(capability.Key),
		})
	}

	issued := ""
	if sess := session.FromContext(r.Context()); sess != nil {
		issued = sess.Flashed("issued_token")
	}

	name := a.mcpServerName()

	a.Render.Render(w, r, "System/Ai", inertia.Props{
		"tokens": list,
		"settings": map[string]any{
			"enabled":             a.mcpEnabled(),
			"confirm_destructive": a.mcpConfirms(),
			"allow_all":           allowAll,
			"name":                name,
		},
		"capabilities": capabilities,
		// What a client will call this server, and the same thing in the form a
		// configuration file uses as its key. Somebody with three panels
		// connected needs both to differ.
		"serverName":  name,
		"serverSlug":  mcpServerSlug(name),
		"defaultName": config.Name,
		// Where a client has to point. The panel's own hostname when one was
		// set, and the public IP with the port when not — which is what a panel
		// nobody has put a domain in front of actually answers on.
		"endpoint": a.panelURL() + MCPPath,
		// Shown once, on the response that created it. The panel keeps a hash,
		// so this is the only time the key exists anywhere it can be read.
		"issued": nullable(issued),
	})
}

// aiSettings writes the switches and the checklist.
func (a *App) aiSettings(w http.ResponseWriter, r *http.Request) {
	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)

		return
	}

	a.Store.SetSetting(mcpEnabledSetting, boolSetting(boolInput(input, "enabled", true)))
	a.Store.SetSetting(mcpConfirmSetting, boolSetting(boolInput(input, "confirm_destructive", true)))

	allowAll := boolInput(input, "allow_all", false)

	a.Store.SetSetting(mcpAllowAllSetting, boolSetting(allowAll))

	if !allowAll {
		granted := []string{}

		for _, capability := range mcpCapabilities {
			for _, asked := range listInput(input, "permissions") {
				if asked == capability.Key {
					granted = append(granted, capability.Key)

					break
				}
			}
		}

		a.Store.SetSetting(mcpPermissionsSetting, strings.Join(granted, ","))
	}

	// The server's name is not written here. It is set beside the key it goes
	// into, and one writer is what keeps the two forms from disagreeing about
	// which of them last had the truth.

	if sess := session.FromContext(r.Context()); sess != nil {
		sess.Flash("success", "Saved. A client that is already connected picks this up when it "+
			"reconnects; the panel applies it to every call either way.")
	}

	inertia.Redirect(w, r, "/ai")
}

// boolSetting is how a checkbox is stored, so SettingBool reads it back.
func boolSetting(value bool) string {
	if value {
		return "1"
	}

	return "0"
}

func (a *App) aiStoreToken(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)

		return
	}

	name := truncate(stringInput(input, "name", "AI assistant"), 60)

	// The name a client will show this panel under, set here because this is
	// where somebody is already typing — and it is the same field they will want
	// when the second panel is added to the same machine.
	if serverName := stringInput(input, "server_name", ""); serverName != "" {
		a.Store.SetSetting(mcpNameSetting, truncate(serverName, 60))
	}

	secret, hash := store.NewAPIToken()

	// Stamped on the row as well as stored as the setting: this is the name the
	// configuration this key goes into is keyed on, and it stays true after
	// somebody renames the panel.
	if _, err := a.Store.CreateAPIToken(r.Context(), user.ID, name, hash,
		store.TokenLabel(secret), a.mcpServerName()); err != nil {
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
