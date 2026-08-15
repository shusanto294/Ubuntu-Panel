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

func (a *App) mcpRetryApp() *mcp.Tool {
	return &mcp.Tool{
		Name:       "retry_app",
		Capability: capApps,
		Title:      "Install an app again",
		Description: "Run one app's install again from the start, whether or not the panel " +
			"believes it is already there. This is what an app sitting at `failed` needs once the " +
			"reason it failed has been dealt with — no disk, no network, a mirror that was down.",
		Properties: map[string]mcp.Property{
			"key": {Description: "The app key, from list_apps."},
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

			a.Catalog.Queue([]string{key}, true)
			a.Queue.PushID(queue.ServicesInstall, "user", user.ID)

			return map[string]any{
				"app":    key,
				"label":  catalog.Label(key),
				"queued": true,
				"next":   "The install is running on the queue; list_apps says when it is done, and get_task has the console.",
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

// mcpAIAccess is the AI access page itself — the keys, and the switches that
// decide what a key may do.
//
// Under capSystem, which is the tick box that already carries panel updates and
// the account's own password: somebody who has granted that has granted this,
// and somebody who has not gets no tool here at all. Loosening anything asks
// first, because widening your own permissions is the one change nobody else is
// watching.
func (a *App) mcpAIAccess() *mcp.Tool {
	return &mcp.Tool{
		Name:       "ai_access",
		Capability: capSystem,
		Title:      "Manage this panel's AI access",
		Description: "The panel's AI access page: the keys that reach this server, what they are " +
			"allowed to do, and where a client connects. `action` is `list` to read it, `issue` " +
			"to create a key, `revoke` to take one away, or `settings` to change the switches.\n\n" +
			"An issued key is shown once, in the answer. Give it to the person and tell them the " +
			"endpoint — the panel keeps only a hash of it.",
		Properties: map[string]mcp.Property{
			"action": {Description: "What to do.", Default: "list",
				Enum: []string{"list", "issue", "revoke", "settings"}},
			"name":        {Description: "For `issue`: what to call the key, so its row is recognisable."},
			"server_name": {Description: "For `issue`: the name a client introduces this panel by."},
			"token_id":    {Type: "integer", Description: "For `revoke`: the key's id, from `list`."},
			"enabled":     {Type: "boolean", Description: "For `settings`: whether this server answers at all."},
			"confirm_destructive": {Type: "boolean",
				Description: "For `settings`: whether destructive work has to be confirmed first."},
			"allow_all": {Type: "boolean", Description: "For `settings`: grant every capability."},
			"permissions": {Type: "array",
				Description: "For `settings` with allow_all off: the capability keys to grant."},
			"confirm": {Type: "boolean", Default: false,
				Description: "The person has been told what is changing and has said yes."},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			user := mcpUser(ctx)
			if user == nil {
				return nil, errors.New("this key is not attached to an account")
			}

			switch args.StringOr("action", "list") {
			case "list":
				return a.mcpAIAccessReport(ctx, user.ID), nil

			case "issue":
				if serverName := args.String("server_name"); serverName != "" {
					a.Store.SetSetting(mcpNameSetting, truncate(serverName, 60))
				}

				secret, hash := store.NewAPIToken()

				id, err := a.Store.CreateAPIToken(ctx, user.ID,
					truncate(args.StringOr("name", "AI assistant"), 60), hash,
					store.TokenLabel(secret), a.mcpServerName())
				if err != nil {
					a.Log.Error("could not store an access key for an MCP call", "error", err)

					return nil, errors.New("the panel could not store the key")
				}

				return map[string]any{
					"token_id":    id,
					"key":         secret,
					"server_name": a.mcpServerName(),
					"endpoint":    a.panelURL() + MCPPath,
					"next": "This is the only time the key is readable. Give it to the person with " +
						"the endpoint, and tell them it goes in an Authorization: Bearer header.",
				}, nil

			case "revoke":
				id := args.Int("token_id")
				if id <= 0 {
					return nil, errors.New("`token_id` is the key to revoke, from the list")
				}

				if err := a.requireConfirmation(args, fmt.Sprintf("Revoking access key %d, which "+
					"signs out whatever is using it — possibly this connection", id)); err != nil {
					return nil, err
				}

				a.Store.DeleteAPIToken(id, user.ID)

				return map[string]any{"token_id": id, "revoked": true}, nil

			case "settings":
				// Loosening is the change that needs a person behind it. Tightening
				// does not: an assistant taking its own permissions away cannot be
				// how something goes wrong.
				loosening := (args.Has("allow_all") && args.Bool("allow_all", false) &&
					!a.Store.SettingBool(mcpAllowAllSetting, true)) ||
					(args.Has("confirm_destructive") && !args.Bool("confirm_destructive", true) &&
						a.mcpConfirms())

				if loosening {
					if err := a.requireConfirmation(args, "Widening what an assistant is allowed "+
						"to do on this panel without being asked"); err != nil {
						return nil, err
					}
				}

				if args.Has("enabled") && !args.Bool("enabled", true) {
					if err := a.requireConfirmation(args, "Switching this panel's MCP server off, "+
						"which ends every assistant connection to it including this one"); err != nil {
						return nil, err
					}
				}

				if args.Has("enabled") {
					a.Store.SetSetting(mcpEnabledSetting, boolSetting(args.Bool("enabled", true)))
				}

				if args.Has("confirm_destructive") {
					a.Store.SetSetting(mcpConfirmSetting,
						boolSetting(args.Bool("confirm_destructive", true)))
				}

				if args.Has("allow_all") {
					a.Store.SetSetting(mcpAllowAllSetting, boolSetting(args.Bool("allow_all", true)))
				}

				if granted := args.List("permissions"); len(granted) > 0 {
					keep := []string{}

					for _, capability := range mcpCapabilities {
						for _, asked := range granted {
							if asked == capability.Key {
								keep = append(keep, capability.Key)

								break
							}
						}
					}

					a.Store.SetSetting(mcpPermissionsSetting, strings.Join(keep, ","))
				}

				return a.mcpAIAccessReport(ctx, user.ID), nil
			}

			return nil, errors.New("`action` is list, issue, revoke or settings")
		},
	}
}

// mcpAIAccessReport is the AI access page's own contents, minus the keys
// themselves — the panel only ever holds their hashes.
func (a *App) mcpAIAccessReport(ctx context.Context, userID int64) map[string]any {
	tokens, _ := a.Store.APITokens(ctx, userID)

	keys := make([]map[string]any, 0, len(tokens))

	for _, token := range tokens {
		keys = append(keys, map[string]any{
			"id":           token.ID,
			"name":         token.Name,
			"prefix":       token.Prefix,
			"server_name":  orDefault(token.ServerName, a.mcpServerName()),
			"last_used_at": nullable(token.LastUsedAt),
			"created_at":   token.CreatedAt,
		})
	}

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

	return map[string]any{
		"keys":     keys,
		"endpoint": a.panelURL() + MCPPath,
		"settings": map[string]any{
			"enabled":             a.mcpEnabled(),
			"confirm_destructive": a.mcpConfirms(),
			"allow_all":           a.Store.SettingBool(mcpAllowAllSetting, true),
			"server_name":         a.mcpServerName(),
		},
		"capabilities": capabilities,
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
