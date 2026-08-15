package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
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

	a.Render.Render(w, r, "System/Settings", inertia.Props{
		"system": a.systemSummary(r.Context()),
		"update": a.updateStatus(r.Context(), false),
		"panel": map[string]any{
			"domain":    nullable(domain),
			"url":       a.panelURL(),
			"port":      a.panelPort(),
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

	port := a.panelPort()

	if asked := strings.TrimSpace(input["panel_port"]); asked != "" {
		wanted, err := strconv.Atoi(asked)

		switch {
		case err != nil || wanted < 1 || wanted > 65535:
			sess.Error("panel_port", "Enter a port between 1 and 65535.")

		case wanted == 80 || wanted == 443:
			sess.Error("panel_port", "80 and 443 are for this machine's sites. The panel needs a "+
				"port of its own — that is what keeps its login page off this server's bare address.")

		default:
			port = wanted
		}
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

	if port != a.panelPort() {
		if err := a.movePanelPort(port); err != nil {
			a.failBack(w, r, err.Error())

			return
		}

		// With a hostname set the panel's address does not move with the port —
		// the hostname is served on 80 and 443 — so saying it changed would send
		// somebody looking for an address that is already in their browser.
		if a.Store.Setting("panel_domain", "") != "" {
			sess.Flash("success", fmt.Sprintf("Saved. %s is unchanged; the panel's own port is "+
				"now %d, and if your server has a firewall of its own, at your host rather than "+
				"on the machine, let it through there too.", a.panelURL(), port))
		} else {
			sess.Flash("success", fmt.Sprintf("Saved. The panel now answers on %s — open that "+
				"address before closing this tab, and if your server has a firewall of its own, "+
				"at your host rather than on the machine, let port %d through it there too.",
				a.panelURL(), port))
		}

		inertia.Back(w, r, "/settings")

		return
	}

	a.okBack(w, r, "Settings saved.")
}

// movePanelPort puts the panel on a different port, and puts it back if nginx
// will not have it.
//
// The order is the careful part: the firewall first, so the new port is
// reachable before anything starts answering on it, and the old port's rule is
// left alone — a rule for a port nothing listens on costs nothing, and removing
// it before the operator has confirmed the new address works is how somebody
// ends up locked out of their own panel.
func (a *App) movePanelPort(port int) error {
	previous := a.panelPort()

	openPanelPort(port)

	if err := a.Store.SetSetting(panelPortSetting, strconv.Itoa(port)); err != nil {
		return errors.New("the panel could not store the new port")
	}

	if err := a.refreshPanelVhost(nil); err != nil {
		a.Store.SetSetting(panelPortSetting, strconv.Itoa(previous))
		a.refreshPanelVhost(nil)

		return fmt.Errorf("the panel stayed on port %d: %w", previous, err)
	}

	return nil
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

	if mongo := a.Store.SecretSetting(databases.MongoSecret); mongo != "" {
		host := orDefault(system.PublicIP(), "127.0.0.1")

		body["mongodb"] = map[string]any{
			"username": databases.MongoAdmin,
			"password": mongo,
			"auth_db":  "admin",
			"port":     databases.MongoPort,
			"host":     host,
			"uri": mongoURI(host, databases.MongoPort, "admin",
				databases.MongoAdmin, mongo),
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

// ------------------------------------------------------------- panel port

// panelPortSetting is where an operator's choice of port is kept.
//
// A setting rather than only the configuration file, because changing it is a
// thing to do from the Settings page — and because the file belongs to root,
// which the panel is not. The file's value is the default this falls back to,
// so a machine that has never been asked answers on whatever the installer
// wrote.
const panelPortSetting = "panel_port"

// panelPort is the port nginx serves the panel on.
func (a *App) panelPort() int {
	if stored := a.Store.Setting(panelPortSetting, ""); stored != "" {
		return config.PanelPort(stored)
	}

	if a.Config.PanelPort > 0 {
		return a.Config.PanelPort
	}

	return config.DefaultPanelPort
}

// openPanelPort lets the panel's port through ufw, where ufw is running.
//
// Called before the vhost that needs it is written, on every start and on every
// change: the port moving is the one change that can put the panel out of reach
// of the person who asked for it, and a firewall rule is the half of it the
// panel can see to itself.
//
// Nothing here can open a cloud provider's own firewall — a security group, a
// network ACL — and that is worth saying out loud wherever this port is shown.
func openPanelPort(port int) {
	if !proc.Ok("command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q 'Status: active'") {
		return
	}

	proc.Ok(fmt.Sprintf("sudo ufw allow %d/tcp", port))
}

// EnsurePanelAddress puts the panel on its own port, and keeps it there.
//
// This is the migration as much as the check. A panel installed before the move
// has a vhost holding `listen 80 default_server`, which is what made this
// machine's bare address open a login form; rewriting it is what takes the
// panel off port 80 and hands the address back to the catch-all, whose answer
// is "No site is configured for this hostname."
//
// The order matters. The panel's vhost gives up port 80 first, and the shared
// configuration is rewritten after — the catch-all only claims the port-80
// default server when nothing else holds it, so doing it the other way round
// leaves the slot empty for a moment, and an empty slot means the bare IP is
// served by whichever site nginx happens to read first.
func (a *App) EnsurePanelAddress() {
	if !a.Store.ServiceInstalled("nginx") {
		return
	}

	port := a.panelPort()

	openPanelPort(port)

	if err := a.refreshPanelVhost(nil); err != nil {
		a.Log.Warn("could not put the panel's own vhost in shape", "error", err)

		return
	}

	// Now that port 80 is free, the catch-all can have it back.
	if err := a.Sites.WriteSharedConfig(nil); err != nil {
		a.Log.Warn("could not write the shared nginx configuration", "error", err)

		return
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		a.Log.Error("nginx refused the configuration after the panel's address was checked",
			"output", check.Output)

		return
	}

	proc.Ok("sudo systemctl reload nginx")

	a.Log.Info("the panel answers on its own port", "url", a.panelURL(),
		"note", "ports 80 and 443 are for this machine's sites")
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
	panelPort := a.panelPort()

	// Which PHP-FPM socket the phpMyAdmin location talks to. The panel's
	// default version, which is the one the app was installed against.
	php := a.Store.PHPVersion()

	// The self-signed pair the hostname's 443 block presents until certbot has
	// run. Written before the vhost that names it: a vhost naming a certificate
	// that is not there is one nginx refuses — taking every site on the machine
	// down with it.
	sites.EnsureDefaultCert()

	// Plain first. The Let's Encrypt certificate does not exist yet, so the
	// hostname answers on 443 with the self-signed pair and on 80 in the clear —
	// which is enough for certbot's own check to reach the challenge, whichever
	// port a CDN in front of this machine sends it to.
	if err := proc.PutFile("/etc/nginx/sites-available/ubuntu-panel",
		panelVhost(domain, panelPort, port, php, false), true); err != nil {
		a.Store.FinishTask(taskID, "failed", err.Error(), 0)

		return err
	}

	list := &tasks.List{}

	list.Add("Enable the panel vhost",
		"sudo ln -sfn /etc/nginx/sites-available/ubuntu-panel /etc/nginx/sites-enabled/ubuntu-panel",
		"sudo mkdir -p "+sites.ACMEWebroot+"/.well-known/acme-challenge",
		// 443 is where the hostname serves the panel, 80 is where the challenge
		// arrives, and the panel's own port stays open as the address that works
		// when the hostname does not.
		"if command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q 'Status: active'; then "+
			"sudo ufw allow 'Nginx Full'; sudo ufw allow "+strconv.Itoa(panelPort)+"/tcp; fi",
		"sudo nginx -t",
		"sudo systemctl reload nginx")

	list.AddFn("Remember the hostname", func(runner *tasks.Runner) (string, error) {
		a.Store.SetSetting("panel_domain", domain)
		a.Store.ForgetSetting("panel_domain_tls")
		system.ForgetPublicIP()

		return fmt.Sprintf("The panel answers on http://%s — no port. Saved as the panel "+
			"address. https://%s answers too, on a self-signed certificate until a real one "+
			"is issued, and http://%s:%d stays open as the address that always works.",
			domain, domain, domain, panelPort), nil
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
				advice = "Make sure the hostname resolves to this server and that ports 80 " +
					"and 443 reach it"
			}

			// Never fatal: the hostname is already live, and a missing
			// certificate is a thing to retry, not a reason to unwind what
			// worked.
			return fmt.Sprintf("There is no certificate for %s, so the panel answers on "+
				"http://%s and on https://%s with a self-signed one — both work, and a "+
				"browser will warn about the second. %s, then set the hostname again to retry.",
				domain, domain, domain, advice), nil
		}

		if err := proc.PutFile("/etc/nginx/sites-available/ubuntu-panel",
			panelVhost(domain, panelPort, port, php, true), true); err != nil {
			return "", err
		}

		// Tested before the reload: a vhost nginx refuses freezes the running
		// configuration for every site on the box.
		if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
			proc.PutFile("/etc/nginx/sites-available/ubuntu-panel",
				panelVhost(domain, panelPort, port, php, false), true)
			proc.Ok("sudo systemctl reload nginx")

			return "", fmt.Errorf("nginx rejected the HTTPS vhost, so the panel was put back on HTTP:\n%s",
				check.Output)
		}

		if err := runner.Must("sudo systemctl reload nginx", time.Minute); err != nil {
			return "", err
		}

		a.Store.SetSetting("panel_domain_tls", "1")

		return fmt.Sprintf("The panel is now served over HTTPS at https://%s — no port — and "+
			"http://%s redirects there. http://%s:%d is still the address that works when the "+
			"hostname does not.", domain, domain, domain, panelPort), nil
	})

	if !tasks.New(a.Store, taskID).Run(list) {
		return fmt.Errorf("the panel hostname was not fully configured")
	}

	return nil
}

// The panel's own vhost, in the three shapes it comes in.
//
// The bare address is the thing being protected, not the ports. What answers on
// this machine's IP is the catch-all's "No site is configured for this
// hostname" — a stranger who scans the address is not shown a control panel's
// login form, and never learns there is one here. The catch-all owns
// `default_server` on 80 and 443, and nothing below ever claims it.
//
// A hostname is different. It is a name the operator chose, pointed at this
// machine and typed into Settings — nobody arrives at it by scanning — so it
// serves the panel itself, on the ports a browser does not have to be told
// about. That is the address people actually use: https://panel.example.com,
// with a certificate on it and no port to remember.
//
// The panel's own port stays regardless, as the address that always works: it
// answers on the bare IP, before a hostname is set and after one is pointed
// somewhere wrong.

// panelPortVhost is the panel itself, on its port.
//
// server_name _ rather than the hostname: this port serves nothing but the
// panel, so every name that reaches it — the hostname, the bare IP, a local
// name somebody set in /etc/hosts — is meant for the panel. Making it the
// default server on that port is also what keeps the IP working when a
// hostname is set, which is the address that always works.
func panelPortVhost(panelPort, port int, php string, certificate string) string {
	tls := ""
	listen := fmt.Sprintf(
		"    listen %[1]d default_server;\n"+
			"    listen [::]:%[1]d default_server;\n", panelPort)

	if certificate != "" {
		listen = fmt.Sprintf(
			"    listen %[1]d ssl default_server;\n"+
				"    listen [::]:%[1]d ssl default_server;\n", panelPort)

		// error_page 497 is the one for "you spoke HTTP to an HTTPS port",
		// which is what an old bookmark and a mistyped scheme both look like.
		// Without it nginx answers with a wall of text about the plain HTTP
		// request being sent to an SSL port.
		tls = fmt.Sprintf(
			"    ssl_certificate     /etc/letsencrypt/live/%[1]s/fullchain.pem;\n"+
				"    ssl_certificate_key /etc/letsencrypt/live/%[1]s/privkey.pem;\n"+
				"    ssl_protocols TLSv1.2 TLSv1.3;\n"+
				"    error_page 497 https://$host:%[2]d$request_uri;\n"+
				"\n", certificate, panelPort)
	}

	return fmt.Sprintf(
		"server {\n"+
			"%s"+
			"    server_name _;\n"+
			"\n"+
			"%s"+
			"%s\n"+
			"%s"+
			"}\n",
		listen, tls, sites.PhpMyAdminLocation(php, phpMyAdminGatePath, port),
		panelProxyBlock(port))
}

// acmeLocation is the HTTP-01 challenge, on whichever block it is put in.
//
// It belongs in the 443 block as much as the 80 one. Behind a CDN with "always
// use HTTPS" switched on — Cloudflare's, and it is on by default — the redirect
// happens in front of this machine, so certbot's own check arrives on 443. With
// the challenge only on 80 it lands on the catch-all's 404 instead of the token,
// the certificate is never issued, and the panel is stuck on the plain address
// for the reason nothing on this machine can see.
func acmeLocation() string {
	return fmt.Sprintf(
		"    location ^~ /.well-known/acme-challenge/ {\n"+
			"        root %s;\n"+
			"        default_type \"text/plain\";\n"+
			"    }\n",
		sites.ACMEWebroot)
}

// panelHostVhost is the hostname: the panel on 80 and 443, so the address is
// the name and nothing else.
//
// Named explicitly and never `default_server` — the catch-all owns that slot on
// both ports, and a second default server is not a warning: nginx refuses the
// whole configuration, for every site on the machine.
//
// Both shapes answer on 443, which is the difference between this and a
// hostname that only redirects. Before the certificate exists the self-signed
// pair is presented — the one the catch-all already uses — because a hostname
// with nothing at all on 443 is a TLS handshake that fails, which is what a CDN
// in front of it reports as an origin SSL error. A warning a browser lets you
// past beats a connection that never opens, and it lasts only until certbot has
// run.
//
// Plain 80 serves the panel until then rather than redirecting into that
// warning. Once there is a real certificate, 80 keeps the challenge and
// redirects everything else.
func panelHostVhost(domain string, port int, php string, secure bool) string {
	certificate, key := sites.DefaultCert+".crt", sites.DefaultCert+".key"
	plain := fmt.Sprintf("%s\n%s\n%s", acmeLocation(),
		sites.PhpMyAdminLocation(php, phpMyAdminGatePath, port), panelProxyBlock(port))

	if secure {
		certificate = "/etc/letsencrypt/live/" + domain + "/fullchain.pem"
		key = "/etc/letsencrypt/live/" + domain + "/privkey.pem"
		plain = fmt.Sprintf("%s\n    location / { return 301 https://%s$request_uri; }\n",
			acmeLocation(), domain)
	}

	return fmt.Sprintf(
		"\nserver {\n"+
			"    listen 80;\n"+
			"    listen [::]:80;\n"+
			"    server_name %[1]s;\n"+
			"\n"+
			"%[2]s"+
			"}\n"+
			"\nserver {\n"+
			"    listen 443 ssl;\n"+
			"    listen [::]:443 ssl;\n"+
			"    server_name %[1]s;\n"+
			"\n"+
			"    ssl_certificate     %[3]s;\n"+
			"    ssl_certificate_key %[4]s;\n"+
			"    ssl_protocols TLSv1.2 TLSv1.3;\n"+
			"\n"+
			"%[5]s\n"+
			"%[6]s\n"+
			"%[7]s"+
			"}\n",
		domain, plain, certificate, key, acmeLocation(),
		sites.PhpMyAdminLocation(php, phpMyAdminGatePath, port), panelProxyBlock(port))
}

// panelVhost is the whole file: the panel on its own port, plus the panel on the
// hostname when one has been set.
func panelVhost(domain string, panelPort, port int, php string, secure bool) string {
	certificate := ""
	if secure && domain != "" {
		certificate = domain
	}

	file := "# Managed by " + config.Name + " — the panel, on port " +
		strconv.Itoa(panelPort) + " and on its hostname.\n" +
		"# Neither block is a default server: the address nobody configured belongs\n" +
		"# to the catch-all, whose answer is a 404 rather than a login form.\n" +
		panelPortVhost(panelPort, port, php, certificate)

	if domain != "" {
		file += panelHostVhost(domain, port, php, secure)
	}

	return file
}

// refreshPanelVhost rewrites the panel's vhost in the shape it is already in,
// so a location the panel has since learned to emit reaches a machine whose
// vhost was written before it did. phpMyAdmin is why it exists.
//
// Only ever rewrites a vhost the panel or its installer wrote — both start
// with a "# Managed by" line — and puts the old one back if nginx will not
// have the new one. The file being rewritten is the one serving the panel, so
// a bad write is the panel disappearing.
func (a *App) refreshPanelVhost(runner *tasks.Runner) error {
	const path = "/etc/nginx/sites-available/ubuntu-panel"

	current := proc.Run("sudo cat "+path+" 2>/dev/null", time.Minute).Output

	// A file that is not there is one to write, not one to refuse: a machine
	// whose vhost was deleted has no panel on its port, and rewriting it is the
	// repair. Only a file with something else in it is left alone.
	if trimmed := strings.TrimSpace(current); trimmed != "" &&
		!strings.HasPrefix(trimmed, "# Managed by") {
		return errors.New("the panel's nginx vhost was edited by hand, so it was left alone. " +
			"Add the phpMyAdmin location to it yourself, or set the panel hostname again " +
			"from Settings to have the panel rewrite it")
	}

	php := a.Store.PHPVersion()
	port := a.Config.BindPort
	domain := a.Store.Setting("panel_domain", "")
	secure := domain != "" && a.Store.SettingBool("panel_domain_tls", false)

	// The certificate the stored flag promises has to be on disk. A machine
	// whose certificate was removed — or never renewed — would otherwise be
	// rewritten into a vhost naming a file that is not there, which nginx
	// refuses for every site on the box, not just this one.
	if secure && !proc.Ok("sudo test -f /etc/letsencrypt/live/"+domain+"/fullchain.pem") {
		secure = false
	}

	// The self-signed pair, for the same reason: without a certificate of its
	// own the hostname's 443 block names that one, and a certificate nginx
	// cannot open is a configuration it refuses for the whole machine.
	if domain != "" && !secure {
		sites.EnsureDefaultCert()
	}

	rewritten := panelVhost(domain, a.panelPort(), port, php, secure)

	if strings.TrimSpace(rewritten) == strings.TrimSpace(current) {
		return nil
	}

	if err := proc.PutFile(path, rewritten, true); err != nil {
		return err
	}

	if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
		proc.PutFile(path, current, true)
		proc.Ok("sudo systemctl reload nginx")

		return fmt.Errorf("nginx refused the rewritten panel vhost, so the old one was put back:\n%s",
			check.Output)
	}

	if runner != nil {
		return runner.Must("sudo systemctl reload nginx", time.Minute)
	}

	if !proc.Ok("sudo systemctl reload nginx") {
		return errors.New("nginx accepted the vhost but would not reload")
	}

	return nil
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
