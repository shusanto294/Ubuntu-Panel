package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/catalog"
	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
	"github.com/shusanto294/ubuntu-panel-go/internal/mcp"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/queue"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
)

// The Apps, Settings and Logs pages, as tools: uninstalling and retrying an
// app, the server's own defaults, the panel's hostname and its updates, the
// passwords the panel generated for the services it installed, and the metric
// history the dashboard graphs.

func (a *App) mcpUninstallApp() *mcp.Tool {
	return &mcp.Tool{
		Name:        "uninstall_app",
		Capability:  capApps,
		Title:       "Uninstall an app",
		Destructive: true,
		Description: "Remove one of the apps from list_apps. The panel refuses where something " +
			"it hosts still needs it — a PHP site and PHP, a database and its engine — and says " +
			"which. Asynchronous: get_task has the console.",
		Properties: map[string]mcp.Property{
			"key": {Description: "The app key, from list_apps."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what this removes and has said yes."},
		},
		Required: []string{"key"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			key := args.String("key")

			if !catalog.Exists(key) {
				return nil, fmt.Errorf("this panel has no app called %s — list_apps has the keys", key)
			}

			if blocker := a.Catalog.UninstallBlocker(ctx, key); blocker != "" {
				return nil, errors.New(blocker)
			}

			if err := a.requireConfirmation(args,
				"Uninstalling "+catalog.Label(key)+" from this server"); err != nil {
				return nil, err
			}

			a.Queue.Push(queue.ServicesUninstall, map[string]any{"key": key, "user": user.ID})

			return map[string]any{
				"app":    key,
				"label":  catalog.Label(key),
				"queued": true,
				"next":   "The removal is running on the queue; list_apps says when it is gone.",
			}, nil
		},
	}
}

func (a *App) mcpDetectApps() *mcp.Tool {
	return &mcp.Tool{
		Name:       "detect_apps",
		Capability: capSitesRead,
		Title:      "Re-check what is installed",
		Description: "Ask the machine again what is actually installed, rather than trusting the " +
			"panel's own record. This is what clears a `failed` left behind by a half-finished " +
			"install on a server that is plainly running the thing.",
		Properties: map[string]mcp.Property{
			"keys": {Type: "array", Description: "Only these apps. All of them when left out."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			a.Catalog.Detect(args.List("keys"))

			return map[string]any{"apps": a.Catalog.Summary(ctx)}, nil
		},
	}
}

func (a *App) mcpServerMetrics() *mcp.Tool {
	return &mcp.Tool{
		Name:       "server_metrics",
		Capability: capSitesRead,
		Title:      "Server metrics",
		ReadOnly:   true,
		Description: "CPU, memory, swap, disk and load — what they are now, and their history " +
			"over one of the windows the dashboard graphs.",
		Properties: map[string]mcp.Property{
			"range": {Description: "The history window.", Default: store.DefaultMetricRange},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			window := args.StringOr("range", store.DefaultMetricRange)

			if !store.ValidMetricRange(window) {
				window = store.DefaultMetricRange
			}

			return map[string]any{
				"now":     system.Read().Summary(),
				"range":   window,
				"ranges":  store.MetricRanges,
				"history": a.Store.MetricHistory(ctx, window),
			}, nil
		},
	}
}

func (a *App) mcpServiceCredentials() *mcp.Tool {
	return &mcp.Tool{
		Name:       "service_credentials",
		Capability: capDatabases,
		Title:      "Service credentials",
		ReadOnly:   true,
		Description: "The passwords the panel generated for the services it installed — the " +
			"MongoDB administrator, the Redis password — and how MariaDB's root access works on " +
			"this machine.",
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
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
					"note": "Root access uses unix socket auth: `sudo mysql` on this machine. " +
						"Per-database credentials are in list_databases.",
				}
			}

			return body, nil
		},
	}
}

func (a *App) mcpPanelSettings() *mcp.Tool {
	return &mcp.Tool{
		Name:       "panel_settings",
		Capability: capSystem,
		Title:      "Read or change the server's defaults",
		Description: "The defaults a new site is given — the PHP version, the Node.js version — " +
			"and the hostname the mail server calls itself in SMTP conversation. Called with " +
			"nothing, it reads them back with the versions this panel installs.\n\n" +
			"Changing these does not touch a site that already exists: they are what the next one " +
			"gets.",
		Properties: map[string]mcp.Property{
			"php_version":   {Description: "The default PHP version for new sites."},
			"node_version":  {Description: "The default Node.js version for new sites."},
			"mail_hostname": {Description: "What the mail server calls itself. Empty clears it."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			if php := args.String("php_version"); php != "" {
				if !config.IsPHPVersion(php) {
					return nil, fmt.Errorf("%s is not a PHP version this panel installs — %s are",
						php, strings.Join(config.PHPVersions, ", "))
				}

				a.Store.SetSetting("php_version", php)
			}

			if node := args.String("node_version"); node != "" {
				if !config.IsNodeVersion(node) {
					return nil, fmt.Errorf("%s is not a Node.js version this panel installs — %s are",
						node, strings.Join(config.NodeVersions, ", "))
				}

				a.Store.SetSetting("node_version", node)
			}

			if args.Has("mail_hostname") {
				if hostname := args.String("mail_hostname"); hostname != "" {
					a.Store.SetSetting("mail_hostname", hostname)
				} else {
					a.Store.ForgetSetting("mail_hostname")
				}
			}

			return map[string]any{
				"php_version":   a.Store.PHPVersion(),
				"node_version":  a.Store.NodeVersion(),
				"mail_hostname": a.Mail.Hostname(),
				"php_versions":  config.PHPVersions,
				"node_versions": config.NodeVersions,
				"panel_domain":  nullable(a.Store.Setting("panel_domain", "")),
				"panel_url":     a.panelURL(),
			}, nil
		},
	}
}

func (a *App) mcpSetPanelDomain() *mcp.Tool {
	return &mcp.Tool{
		Name:       "set_panel_domain",
		Capability: capSystem,
		Title:      "Serve the panel on a hostname",
		Description: "Point the panel itself at a hostname: write its own nginx vhost, ask " +
			"Let's Encrypt for a certificate, and reload. The hostname has to resolve to this " +
			"server first, or the certificate cannot be issued and the panel stays on HTTP.\n\n" +
			"The address a client connects an assistant to changes with it, so a key already " +
			"configured somewhere needs its endpoint updating too. Asynchronous — get_task has the run.",
		Properties: map[string]mcp.Property{
			"domain": {Description: "The hostname, like panel.example.com."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told the panel's own address changes and has said yes."},
		},
		Required: []string{"domain"},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			domain := strings.ToLower(args.String("domain"))

			if !validDomain(domain) {
				return nil, errors.New("that is not a hostname — send it like panel.example.com")
			}

			if err := a.requireConfirmation(args, "Moving the panel itself to "+domain+
				", which changes the address it — and this connection — answers on"); err != nil {
				return nil, err
			}

			a.Queue.Push(queue.PanelDomain, map[string]any{"domain": domain, "email": user.Email})

			return map[string]any{
				"domain": domain,
				"queued": true,
				"next": "The vhost and the certificate are being written on the queue; get_task " +
					"has the console. The panel answers on https://" + domain + " when it finishes.",
			}, nil
		},
	}
}

func (a *App) mcpPanelUpdate() *mcp.Tool {
	return &mcp.Tool{
		Name:       "panel_update",
		Capability: capSystem,
		Title:      "Check for and install a panel update",
		Description: "What version this panel is, what the latest release is, and whether it is " +
			"behind. With `install` set, it runs the installer in a unit of its own and the panel " +
			"restarts itself when it finishes — which drops this connection for a few seconds.",
		Properties: map[string]mcp.Property{
			"install": {Type: "boolean", Default: false,
				Description: "Install the update rather than only reporting it."},
			"confirm": {Type: "boolean", Default: false,
				Description: "For `install`: the person has been told the panel restarts and has said yes."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			status := a.updateStatus(ctx, true)

			if !args.Bool("install", false) {
				return status, nil
			}

			if err := a.requireConfirmation(args,
				"Updating the panel, which replaces its binary and restarts it"); err != nil {
				return nil, err
			}

			command := fmt.Sprintf(
				"sudo systemd-run --unit=ubuntu-panel-update --description='Ubuntu Panel update' "+
					"/bin/bash -c %s", proc.Quote(a.updateCommand()))

			if result := proc.Run(command, time.Minute); !result.OK() {
				return nil, errors.New("the update could not be started: " + result.Output)
			}

			status["installing"] = true
			status["next"] = "The update is running in a unit of its own. The panel restarts when " +
				"it finishes — expect this connection to drop briefly, then report the version " +
				"server_status gives afterwards."

			return status, nil
		},
	}
}

func (a *App) mcpPanelAccount() *mcp.Tool {
	return &mcp.Tool{
		Name:       "panel_account",
		Capability: capSystem,
		Title:      "Read or change the panel account",
		Description: "The account this access key belongs to: its name and the address it signs " +
			"in with, and the way to change either or its password. Changing the password signs " +
			"the browser sessions out; this key keeps working.",
		Properties: map[string]mcp.Property{
			"name":     {Description: "A new display name."},
			"email":    {Description: "A new sign-in address."},
			"password": {Description: "A new password, at least twelve characters."},
			"current_password": {Description: "The account's current password, required before a " +
				"new one can be set."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			changed := []string{}

			name := args.StringOr("name", user.Name)
			email := strings.ToLower(args.StringOr("email", user.Email))

			if name != user.Name || email != user.Email {
				if email == "" || !strings.Contains(email, "@") {
					return nil, errors.New("that is not an email address")
				}

				if err := a.Store.UpdateProfile(ctx, user.ID, name, email); err != nil {
					return nil, errors.New("the panel could not update the account")
				}

				changed = append(changed, "details")
			}

			if password := args.String("password"); password != "" {
				if len(password) < minPasswordLength {
					return nil, fmt.Errorf("a password is at least %d characters", minPasswordLength)
				}

				// The current one, the same as the profile form asks for: this key
				// is a credential for the panel, not for the account behind it.
				if !a.Store.CheckPassword(ctx, user.ID, args.String("current_password")) {
					return nil, errors.New("`current_password` has to be the account's current " +
						"password before a new one can be set")
				}

				if err := a.Store.UpdatePassword(ctx, user.ID, password); err != nil {
					return nil, errors.New("the panel could not store the new password")
				}

				changed = append(changed, "password")
			}

			return map[string]any{
				"user_id": user.ID,
				"name":    name,
				"email":   email,
				"changed": changed,
			}, nil
		},
	}
}
