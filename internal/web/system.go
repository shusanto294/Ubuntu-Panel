package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/inertia"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/session"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// systemSummary is the `system` prop every system page carries in its header.
func (a *App) systemSummary(ctx context.Context) map[string]any {
	services, _ := a.Store.Services(ctx)

	installed, pending, failed := 0, 0, 0

	for _, service := range services {
		switch service.Status {
		case store.ServiceInstalled:
			installed++
		case store.ServiceQueued, store.ServiceInstalling:
			pending++
		case store.ServiceFailed:
			failed++
		}
	}

	return map[string]any{
		"hostname":                 system.Hostname(),
		"public_ip":                system.PublicIP(),
		"os":                       system.OS(),
		"php_version":              a.Store.PHPVersion(),
		"node_version":             a.Store.NodeVersion(),
		"mail_hostname":            nullable(a.Store.Setting("mail_hostname", "")),
		"mail_configured":          a.Mail.IsConfigured(),
		"installed_services":       a.Store.InstalledServiceKeys(ctx),
		"available_engines":        a.availableEngines(),
		"services_installed_count": installed,
		"services_pending_count":   pending,
		"services_failed_count":    failed,
		"preparing":                pending > 0,
	}
}

// overview is the dashboard.
func (a *App) overview(w http.ResponseWriter, r *http.Request) {
	metrics := system.Read()

	sitesList, _ := a.Store.Sites(r.Context())

	recent := make([]map[string]any, 0, 5)
	for i, site := range sitesList {
		if i >= 5 {
			break
		}

		recent = append(recent, map[string]any{
			"id":         site.ID,
			"domain":     site.Domain,
			"type_label": site.TypeLabel(),
			"status":     site.Status,
		})
	}

	activity, _ := a.Store.RecentTasks(r.Context(), 5)

	recentActivity := make([]map[string]any, 0, len(activity))
	for _, task := range activity {
		recentActivity = append(recentActivity, map[string]any{
			"id":         task.ID,
			"type":       task.Type,
			"action":     task.Action,
			"status":     task.Status,
			"message":    task.Message,
			"created_at": task.CreatedAt,
		})
	}

	services, _ := a.Store.Services(r.Context())

	installed := 0
	for _, service := range services {
		if service.Status == store.ServiceInstalled {
			installed++
		}
	}

	a.Render.Render(w, r, "System/Overview", inertia.Props{
		"system":  a.systemSummary(r.Context()),
		"update":  a.updateStatus(r.Context(), false),
		"metrics": metrics.Summary(),
		// The graphs open on the shortest range, so ship that one with the page
		// and let the browser fetch the others only if it asks.
		"history":       a.Store.MetricHistory(r.Context(), store.DefaultMetricRange),
		"historyRanges": store.MetricRanges,
		"counts": map[string]any{
			"sites":              a.Store.SiteCount(r.Context()),
			"databases":          a.Store.DatabaseCount(r.Context(), ""),
			"services_installed": installed,
			"services_total":     len(catalog.Entries),
		},
		"sites":          recent,
		"activeTask":     a.firstActiveTask(r, "provision", "site", "database", "mail"),
		"recentActivity": recentActivity,
	})
}

// firstActiveTask is the running task of any of the given types, or null.
func (a *App) firstActiveTask(r *http.Request, kinds ...string) map[string]any {
	for _, kind := range kinds {
		if id := a.Store.LatestTaskID(r.Context(), kind, true); id > 0 {
			return a.Store.TaskConsole(r.Context(), id, 0)
		}
	}

	return nil
}

// appTaskActions are the provision tasks this page drives. The type also
// carries the panel's own domain change, which belongs to Settings.
var appTaskActions = []string{"services.install", "services.uninstall"}

// apps is the Apps page.
func (a *App) apps(w http.ResponseWriter, r *http.Request) {
	a.Catalog.SyncRows()

	a.Render.Render(w, r, "System/Apps", inertia.Props{
		"system":   a.systemSummary(r.Context()),
		"services": a.Catalog.Summary(r.Context()),
		"activeTask": a.Store.TaskConsole(r.Context(),
			a.Store.LatestTaskByActions(r.Context(), "provision", appTaskActions, true), 0),
		// The console of an install that has just finished, and only just: a
		// finished task stops being news, and the page should look like a
		// catalogue again rather than like something is happening.
		"latestTask": a.Store.TaskConsole(r.Context(),
			a.Store.RecentTaskID(r.Context(), "provision", appTaskActions, store.RecentTaskWindow), 0),
	})
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	a.Catalog.SyncRows()

	// Whether setting a hostname can also get a certificate, said before the
	// form is submitted rather than in the console afterwards. Detected rather
	// than read from the row: the table only learns an app is there when the
	// panel installs it, and a certificate is not something to promise on that.
	a.Catalog.DetectFresh([]string{"certbot"}, 30*time.Second)

	domain := a.Store.Setting("panel_domain", "")

	url := "http://" + orDefault(system.PublicIP(), "127.0.0.1")
	if domain != "" {
		if a.Store.SettingBool("panel_domain_tls", false) {
			url = "https://" + domain
		} else {
			url = "http://" + domain
		}
	}

	a.Render.Render(w, r, "System/Settings", inertia.Props{
		"system": a.systemSummary(r.Context()),
		"update": a.updateStatus(r.Context(), false),
		"panel": map[string]any{
			"domain":    nullable(domain),
			"url":       url,
			"public_ip": system.PublicIP(),
			"certbot":   a.Store.ServiceInstalled("certbot"),
		},
		"defaults": map[string]any{
			"php_version":   a.Store.PHPVersion(),
			"node_version":  a.Store.NodeVersion(),
			"mail_hostname": nullable(a.Store.Setting("mail_hostname", "")),
		},
		"phpVersions":  config.PHPVersions,
		"nodeVersions": config.NodeVersions,
		// Narrowed to this action so an app install running on the Apps page
		// does not appear here.
		"activeTask": a.Store.TaskConsole(r.Context(),
			a.Store.LatestTaskByAction(r.Context(), "provision", "panel.domain", true), 0),
		"latestTask": a.Store.TaskConsole(r.Context(),
			a.Store.RecentTaskID(r.Context(), "provision", []string{"panel.domain"},
				store.RecentTaskWindow), 0),
	})
}

func (a *App) updateSettings(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	php := input["php_version"]
	node := input["node_version"]

	if !config.IsPHPVersion(php) {
		sess.Error("php_version", "That is not a PHP version the panel installs.")
	}
	if !config.IsNodeVersion(node) {
		sess.Error("node_version", "That is not a Node.js version the panel installs.")
	}

	if sess.HasErrors() {
		inertia.Back(w, r, "/settings")
		return
	}

	a.Store.SetSetting("php_version", php)
	a.Store.SetSetting("node_version", node)

	if hostname := strings.TrimSpace(input["mail_hostname"]); hostname != "" {
		a.Store.SetSetting("mail_hostname", hostname)
	} else {
		a.Store.ForgetSetting("mail_hostname")
	}

	a.okBack(w, r, "Settings saved.")
}

// metricsEndpoint is what the dashboard polls for live figures.
func (a *App) metricsEndpoint(w http.ResponseWriter, r *http.Request) {
	metrics := system.Read()

	a.writeJSON(w, http.StatusOK, map[string]any{"metrics": metrics.Summary()})
}

func (a *App) metricHistory(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("range")

	if !store.ValidMetricRange(window) {
		window = store.DefaultMetricRange
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"history": a.Store.MetricHistory(r.Context(), window),
	})
}

// credentials answers the "show credentials" panel for the services the panel
// generated a password for.
func (a *App) credentials(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}

	if mongo := a.Store.SecretSetting("mongo_root_password"); mongo != "" {
		body["mongodb"] = map[string]any{
			"username": "panelAdmin",
			"password": mongo,
			"auth_db":  "admin",
			"port":     27017,
		}
	}

	if redis := a.Store.SecretSetting("redis_password"); redis != "" {
		body["redis"] = map[string]any{"password": redis, "port": 6379}
	}

	if a.Store.ServiceInstalled("mysql") {
		body["mysql"] = map[string]any{
			"note": "Root access uses unix socket auth: run `sudo mysql` on this machine.",
		}
	}

	a.writeJSON(w, http.StatusOK, body)
}

// ------------------------------------------------------------------- apps

func (a *App) servicesDetect(w http.ResponseWriter, r *http.Request) {
	a.Catalog.Detect(nil)

	a.okBack(w, r, "Re-checked what is installed on this machine.")
}

func (a *App) servicesInstallOne(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	key := r.PathValue("service")

	if !catalog.Exists(key) {
		a.notFoundError(w, r, "No such app.")
		return
	}

	input, _ := a.input(r)

	if a.Catalog.Queue([]string{key}, boolInput(input, "force", false)) == 0 {
		a.failBack(w, r, catalog.Label(key)+" is already installed or already queued.")
		return
	}

	a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

	inertia.Back(w, r, "/apps")
}

func (a *App) servicesInstallMany(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	keys := []string{}

	for _, key := range listInput(input, "keys") {
		if catalog.Exists(key) {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		// No selection means the defaults, which is what "install all required"
		// asks for.
		for _, entry := range catalog.Entries {
			if entry.Core {
				keys = append(keys, entry.Key)
			}
		}
	}

	if a.Catalog.Queue(keys, boolInput(input, "force", false)) == 0 {
		a.failBack(w, r, "Everything asked for is already installed or already queued.")
		return
	}

	a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

	inertia.Back(w, r, "/apps")
}

func (a *App) servicesRetry(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	key := r.PathValue("service")

	if !catalog.Exists(key) {
		a.notFoundError(w, r, "No such app.")
		return
	}

	a.Catalog.Queue([]string{key}, true)
	a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

	inertia.Back(w, r, "/apps")
}

func (a *App) servicesUninstall(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	key := r.PathValue("service")

	if !catalog.Exists(key) {
		a.notFoundError(w, r, "No such app.")
		return
	}

	if blocker := a.Catalog.UninstallBlocker(r.Context(), key); blocker != "" {
		a.failBack(w, r, blocker)
		return
	}

	a.Queue.Push(queue.ServicesUninstall, map[string]any{"key": key, "user": user.ID})

	inertia.Back(w, r, "/apps")
}

// ------------------------------------------------------------ panel domain

// updateDomain serves the panel on a hostname the operator owns.
func (a *App) updateDomain(w http.ResponseWriter, r *http.Request) {
	sess := session.FromContext(r.Context())
	user := userFrom(r.Context())

	input, err := a.input(r)
	if err != nil {
		a.badRequest(w, r, err)
		return
	}

	domain := strings.ToLower(strings.TrimSpace(input["domain"]))

	if !validDomain(domain) {
		sess.Error("domain", "Enter a hostname, like panel.example.com.")
		inertia.Back(w, r, "/settings")

		return
	}

	// The Let's Encrypt contact only ever receives expiry warnings, so it is
	// taken from the account rather than typed again.
	a.Queue.Push(queue.PanelDomain, map[string]any{"domain": domain, "email": user.Email})

	inertia.Back(w, r, "/settings")
}

// configurePanelDomain writes the panel's own vhost, gets a certificate and
// reloads nginx. It runs on a worker, because issuing the certificate reloads
// nginx underneath the request that asked for it.
func (a *App) configurePanelDomain(ctx context.Context, domain, email string) error {
	if domain == "" {
		return fmt.Errorf("no hostname was given")
	}

	taskID, err := a.Store.CreateTask(ctx, "provision", "panel.domain", 0, 0, domain)
	if err != nil {
		return err
	}

	if email == "" {
		email = "admin@" + domain
	}

	port := a.Config.BindPort

	// A self-signed pair, so the vhost below can listen on 443 from the first
	// second. nginx refuses to start with an ssl_certificate that is not there,
	// which is why the real certificate cannot be named yet — but leaving 443
	// closed until certbot succeeds is what puts a CDN in front of this
	// hostname into "web server is down".
	proc.Ok(fmt.Sprintf(
		"sudo test -f %[1]s.crt || sudo openssl req -x509 -nodes -newkey rsa:2048 -days 3650 "+
			"-keyout %[1]s.key -out %[1]s.crt -subj \"/CN=%[2]s\" 2>/dev/null",
		sites.DefaultCert, domain))

	// The hostname, and the address the machine answers on. Once this vhost is
	// name-based, a request to the bare IP matches nothing and lands on the
	// catch-all — which exists to refuse unknown hostnames, so the panel would
	// disappear from the one address that always works the moment a site is
	// created. Naming the IP here keeps it reachable.
	names := domain

	if ip := system.PublicIP(); ip != "" {
		names = domain + " " + ip
	}

	if err := proc.PutFile("/etc/nginx/sites-available/ubuntu-panel",
		panelVhost(names, port), true); err != nil {
		a.Store.FinishTask(taskID, "failed", err.Error(), 0)

		return err
	}

	list := &tasks.List{}

	list.Add("Enable the panel vhost",
		"sudo ln -sfn /etc/nginx/sites-available/ubuntu-panel /etc/nginx/sites-enabled/ubuntu-panel",
		"sudo mkdir -p "+sites.ACMEWebroot+"/.well-known/acme-challenge",
		// 443 is new to this vhost, and a firewall that only ever had 80 open
		// would answer the certificate check and nothing else.
		"if command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q 'Status: active'; then "+
			"sudo ufw allow 'Nginx Full'; fi",
		"sudo nginx -t",
		"sudo systemctl reload nginx")

	list.AddFn("Remember the hostname", func(runner *tasks.Runner) (string, error) {
		a.Store.SetSetting("panel_domain", domain)
		a.Store.ForgetSetting("panel_domain_tls")
		system.ForgetPublicIP()

		return "The panel answers on http://" + domain + ". Saved as the panel address.", nil
	})

	// Certbot is an optional app in the catalogue, so on most machines it is
	// simply not there — and `certbot: command not found` used to take the
	// whole task down with it, hostname included. Nothing here installs it: the
	// hostname is the job that was asked for, TLS is what Apps is for.
	certbot := a.Store.ServiceInstalled("certbot") || proc.Ok("command -v certbot >/dev/null 2>&1")

	if certbot {
		list.Add("Request the certificate", fmt.Sprintf(
			"sudo certbot certonly --webroot -w %s -d %s --non-interactive --agree-tos "+
				"--email %s --keep-until-expiring --no-eff-email 2>&1",
			sites.ACMEWebroot, proc.Quote(domain), proc.Quote(email))).Optional()
	}

	list.AddFn("Serve the panel over HTTPS", func(runner *tasks.Runner) (string, error) {
		// sudo, because /etc/letsencrypt/live is root-only: as the panel user
		// the test is false for a certificate that is sitting right there.
		secure := proc.Ok("sudo test -f /etc/letsencrypt/live/" + domain + "/fullchain.pem")

		if !secure {
			a.Store.ForgetSetting("panel_domain_tls")

			advice := "Install Certbot from Apps"
			if certbot {
				advice = "Point the hostname straight at this server, unproxied"
			}

			// Never fatal: the hostname is already live, and a missing
			// certificate is a thing to retry, not a reason to unwind what
			// worked.
			return fmt.Sprintf("There is no certificate for %s, so the panel stays on http://%s — "+
				"which works. %s, then set the hostname again to retry.", domain, domain, advice), nil
		}

		if err := proc.PutFile("/etc/nginx/sites-available/ubuntu-panel",
			panelTLSVhost(domain, system.PublicIP(), port), true); err != nil {
			return "", err
		}

		// Tested before the reload: a vhost nginx refuses freezes the running
		// configuration for every site on the box.
		if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
			proc.PutFile("/etc/nginx/sites-available/ubuntu-panel", panelVhost(names, port), true)
			proc.Ok("sudo systemctl reload nginx")

			return "", fmt.Errorf("nginx rejected the HTTPS vhost, so the panel was put back on HTTP:\n%s",
				check.Output)
		}

		if err := runner.Must("sudo systemctl reload nginx", time.Minute); err != nil {
			return "", err
		}

		a.Store.SetSetting("panel_domain_tls", "1")

		return "The panel is now served over HTTPS at https://" + domain + ".", nil
	})

	if !tasks.New(a.Store, taskID).Run(list) {
		return fmt.Errorf("the panel hostname was not fully configured")
	}

	return nil
}

// panelVhost is plain HTTP and self-signed HTTPS, with no redirect between
// them: a browser on http:// gets the panel rather than a certificate warning,
// and a proxy on https:// gets an answer. The redirect arrives with the real
// certificate.
func panelVhost(names string, port int) string {
	return fmt.Sprintf(
		"# Managed by %s — the panel itself\n"+
			"server {\n"+
			"    listen 80;\n"+
			"    listen [::]:80;\n"+
			"    server_name %s;\n"+
			"\n"+
			"    location ^~ /.well-known/acme-challenge/ {\n"+
			"        root %s;\n"+
			"        default_type \"text/plain\";\n"+
			"    }\n"+
			"\n"+
			"%s"+
			"}\n"+
			"\n"+
			"server {\n"+
			"    listen 443 ssl;\n"+
			"    listen [::]:443 ssl;\n"+
			"    server_name %s;\n"+
			"\n"+
			"    ssl_certificate     %s.crt;\n"+
			"    ssl_certificate_key %s.key;\n"+
			"    ssl_protocols TLSv1.2 TLSv1.3;\n"+
			"\n"+
			"%s"+
			"}\n",
		config.Name, names, sites.ACMEWebroot, panelProxyBlock(port),
		names, sites.DefaultCert, sites.DefaultCert, panelProxyBlock(port))
}

// panelTLSVhost redirects the hostname to HTTPS and keeps a plain block for the
// IP address.
//
// The IP keeps its own plain-HTTP block rather than joining the redirect:
// sending it to https:// would present the hostname's certificate to a request
// that did not use the hostname, so the address that always works would always
// warn.
func panelTLSVhost(domain, ip string, port int) string {
	ipBlock := ""

	if ip != "" {
		ipBlock = fmt.Sprintf(
			"\nserver {\n"+
				"    listen 80;\n"+
				"    listen [::]:80;\n"+
				"    server_name %s;\n"+
				"\n"+
				"%s"+
				"}\n", ip, panelProxyBlock(port))
	}

	return fmt.Sprintf(
		"# Managed by %s — the panel itself\n"+
			"server {\n"+
			"    listen 80;\n"+
			"    listen [::]:80;\n"+
			"    server_name %s;\n"+
			"\n"+
			"    location ^~ /.well-known/acme-challenge/ {\n"+
			"        root %s;\n"+
			"        default_type \"text/plain\";\n"+
			"    }\n"+
			"\n"+
			"    location / { return 301 https://$host$request_uri; }\n"+
			"}\n"+
			"\n"+
			"server {\n"+
			"    listen 443 ssl;\n"+
			"    listen [::]:443 ssl;\n"+
			"    server_name %s;\n"+
			"\n"+
			"    ssl_certificate     /etc/letsencrypt/live/%s/fullchain.pem;\n"+
			"    ssl_certificate_key /etc/letsencrypt/live/%s/privkey.pem;\n"+
			"    ssl_protocols TLSv1.2 TLSv1.3;\n"+
			"    add_header Strict-Transport-Security \"max-age=31536000\" always;\n"+
			"\n"+
			"%s"+
			"}\n%s",
		config.Name, domain, sites.ACMEWebroot, domain, domain, domain,
		panelProxyBlock(port), ipBlock)
}

func panelProxyBlock(port int) string {
	return fmt.Sprintf(
		"    location / {\n"+
			"        proxy_pass http://127.0.0.1:%d;\n"+
			"        proxy_http_version 1.1;\n"+
			"        proxy_set_header Upgrade $http_upgrade;\n"+
			"        proxy_set_header Connection \"upgrade\";\n"+
			"        proxy_set_header Host $host;\n"+
			"        proxy_set_header X-Real-IP $remote_addr;\n"+
			"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"+
			"        proxy_set_header X-Forwarded-Proto $scheme;\n"+
			"        proxy_read_timeout 3600;\n"+
			"    }\n", port)
}

// ----------------------------------------------------------------- updates

// updateCache keeps the answer from GitHub so the overview never waits on it.
var updateCache struct {
	sync.Mutex
	value   map[string]any
	fetched time.Time
}

// updateStatus is everything the version card needs to say "you are up to
// date" or otherwise: what is installed, what is published, and the one command
// that closes the gap.
//
// The shape is the Laravel panel's, because the card that reads it came from
// there — `current`, `latest`, `available`, `command` — and a flat object would
// blank the whole Settings page on the first property it could not find.
func (a *App) updateStatus(ctx context.Context, fresh bool) map[string]any {
	current := map[string]any{
		"version":      config.Version,
		"commit":       nullable(a.Store.Setting("installed_commit", "")),
		"committed_at": nullable(a.Store.Setting("installed_at", "")),
		"repository":   a.Config.Repository,
	}

	updateCache.Lock()
	cached, fetchedAt := updateCache.value, updateCache.fetched
	updateCache.Unlock()

	if !fresh && cached != nil && time.Since(fetchedAt) < 6*time.Hour {
		// The installed half is read fresh every time: it changes when the
		// panel is updated, and the cache is about GitHub, not about this
		// machine.
		refreshed := map[string]any{}
		for key, value := range cached {
			refreshed[key] = value
		}

		refreshed["current"] = current
		refreshed["available"] = behind(current, refreshed["latest"])

		return refreshed
	}

	latest := a.latestRelease()

	status := map[string]any{
		"current":    current,
		"latest":     latest,
		"available":  behind(current, latest),
		"command":    a.updateCommand(),
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	}

	// A failed check is not cached: the next page view should try again rather
	// than repeat an error for six hours.
	if latest["error"] == nil {
		updateCache.Lock()
		updateCache.value, updateCache.fetched = status, time.Now()
		updateCache.Unlock()
	}

	return status
}

// updateCommand is the exact line to paste. Built here rather than written into
// the page because the panel knows which repository it came from.
func (a *App) updateCommand() string {
	repository := strings.TrimSuffix(strings.TrimSuffix(a.Config.Repository, "/"), ".git")
	repository = strings.TrimPrefix(repository, "https://github.com/")

	return fmt.Sprintf(
		"curl -fsSL https://raw.githubusercontent.com/%s/%s/get.sh | sudo bash",
		repository, a.Config.UpdateBranch)
}

// latestRelease is the newest published release, falling back to the tip of the
// update branch on a repository that has never tagged one.
func (a *App) latestRelease() map[string]any {
	repository := strings.TrimSuffix(strings.TrimPrefix(a.Config.Repository, "https://github.com/"), ".git")

	empty := func(reason string) map[string]any {
		return map[string]any{
			"version": nil, "commit": nil, "committed_at": nil, "url": nil,
			"error": reason,
		}
	}

	if !strings.Contains(repository, "/") {
		return empty("The configured repository is not a GitHub one.")
	}

	var release struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		TargetSHA   string `json:"target_commitish"`
	}

	status, err := fetchJSON("https://api.github.com/repos/"+repository+"/releases/latest", &release)

	switch {
	case err != nil:
		return empty(err.Error())

	case status == http.StatusOK && release.TagName != "":
		return map[string]any{
			"version":      strings.TrimPrefix(release.TagName, "v"),
			"commit":       nullable(release.TargetSHA),
			"committed_at": nullable(release.PublishedAt),
			"url":          nullable(release.HTMLURL),
			"error":        nil,
		}

	case status != http.StatusNotFound:
		return empty(fmt.Sprintf("GitHub answered HTTP %d.", status))
	}

	// No release yet: the tip of the update branch is the only thing to
	// compare against, and it is what an install from source recorded.
	var commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}

	status, err = fetchJSON(
		"https://api.github.com/repos/"+repository+"/commits/"+a.Config.UpdateBranch, &commit)

	if err != nil {
		return empty(err.Error())
	}

	if status != http.StatusOK || commit.SHA == "" {
		return empty(fmt.Sprintf("GitHub answered HTTP %d.", status))
	}

	return map[string]any{
		"version":      nil,
		"commit":       commit.SHA,
		"committed_at": nullable(commit.Commit.Committer.Date),
		"url":          nullable(commit.HTMLURL),
		"error":        nil,
	}
}

// behind compares what is installed against what is published. Versions decide
// it when both have one; otherwise the commit does, and a machine that recorded
// neither is left alone rather than told forever that it is out of date.
func behind(current map[string]any, latest any) bool {
	published, ok := latest.(map[string]any)
	if !ok || published["error"] != nil {
		return false
	}

	if version, ok := published["version"].(string); ok && version != "" {
		installed, _ := current["version"].(string)

		return installed != "" && strings.TrimPrefix(installed, "v") != strings.TrimPrefix(version, "v")
	}

	commit, ok := published["commit"].(string)
	if !ok || commit == "" {
		return false
	}

	installed, _ := current["commit"].(string)

	return installed != "" && installed != commit
}

// fetchJSON is one GitHub call. The status code comes back with the body
// because "nothing published yet" and "rate limited" are different answers to
// the same question.
func fetchJSON(url string, into any) (int, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Ubuntu-Panel")

	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return 0, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, err
	}

	json.Unmarshal(raw, into)

	return response.StatusCode, nil
}

func (a *App) checkUpdates(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.updateStatus(r.Context(), true))
}

// updatePanel runs the installer again, in a transient unit of its own so that
// replacing this binary and restarting this service does not kill the process
// doing it.
func (a *App) updatePanel(w http.ResponseWriter, r *http.Request) {
	// The same line the card offers to copy, in a transient unit of its own —
	// replacing this binary and restarting this service would otherwise kill
	// the process doing it.
	command := fmt.Sprintf(
		"sudo systemd-run --unit=ubuntu-panel-update --description='Ubuntu Panel update' "+
			"/bin/bash -c %s", proc.Quote(a.updateCommand()))

	if result := proc.Run(command, time.Minute); !result.OK() {
		a.failBack(w, r, "Could not start the update: "+result.Output)
		return
	}

	a.okBack(w, r, "The update is running. The panel will restart on its own when it finishes.")
}
