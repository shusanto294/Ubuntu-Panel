package web

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/mcp"
)

// panel_guide is the map of this panel, written for whoever is holding the
// tools.
//
// A tool list is an alphabet: sixty names with a sentence each, and no order to
// them. What an assistant is missing when it says "the panel can do this but I
// cannot" is not a tool — it is the knowledge that the tool exists and which
// page of the panel it belongs to. This is that, in the panel's own order, with
// the step-by-step for the jobs that take more than one call.
//
// Kept beside the tools it describes, and checked by a test that every tool it
// names is registered: a guide that points at something that is not there is
// worse than no guide.

// guideArea is one page of the panel and the tools behind it.
type guideArea struct {
	Key      string
	Page     string
	What     string
	Tools    []string
	Recipes  []guideRecipe
	Warnings []string
}

// guideRecipe is a job somebody actually asks for, and the calls it takes.
type guideRecipe struct {
	Task  string
	Steps []string
}

// panelGuide is the whole of it. Every tool name here is registered in
// buildMCP, and TestTheGuideOnlyNamesRealTools keeps it that way.
var panelGuide = []guideArea{
	{
		Key:  "sites",
		Page: "Sites",
		What: "Websites this server hosts: WordPress, Laravel, Next.js, Node.js, plain PHP, " +
			"static HTML, and the applications the panel installs itself (Nextcloud, n8n).",
		Tools: []string{"list_site_types", "list_sites", "get_site", "create_site", "deploy_site",
			"pull_site", "restart_site", "delete_site", "site_logs", "set_web_directory",
			"set_site_aliases",
			"site_php_settings", "request_certificate", "site_cache", "site_cron", "site_access",
			"wordpress_login_link", "sync_dns"},
		Recipes: []guideRecipe{
			{
				Task: "Put a new site on this server",
				Steps: []string{
					"list_site_types — check the type's `ready`; if false, install_apps what it names and wait.",
					"create_site with the type and the domain, and nothing else unless the person asked for it.",
					"Read the answer's `dns`. `managed: false` means no connected credential holds that " +
						"zone, so the panel published no record — the site will build and still show " +
						"DNS_PROBE_FINISHED_NXDOMAIN. Say `tell_the_person` to them; it is the record to add.",
					"get_site every few seconds until `status` is active or failed.",
					"If it failed: get_task with the id in `latest_task` for the console, fix, then deploy_site.",
					"Report the URL, and — where the type has one — the admin login in `application`. " +
						"get_site's `dns_problem` is there whenever the name still resolves nowhere.",
				},
			},
			{
				Task: "Ship a change to a git-backed site",
				Steps: []string{
					"pull_site — fetches the repository again and rebuilds, leaving nginx and the certificate alone.",
					"get_site until the task finishes; site_logs if the site then answers with a 500.",
				},
			},
			{
				Task: "A site answers on its domain but not on www",
				Steps: []string{
					"get_site — `aliases` is every hostname besides the domain. A site created " +
						"for a root domain has its www there already; one made before the panel " +
						"did that does not, and the www reaches the catch-all vhost instead, " +
						"which answers \"No site is configured for this hostname\".",
					"set_site_aliases with the whole list: what `aliases` had, plus the www host. " +
						"That writes the vhost, publishes the record where the panel manages the " +
						"site's DNS, and asks for a certificate covering it.",
					"get_task or site_logs for certbot's run. A hostname that still resolves " +
						"nowhere is left off the certificate rather than failing the request, so " +
						"publish its record and deploy_site to have it covered.",
				},
			},
			{
				Task: "A site is up but does not answer, or has no certificate",
				Steps: []string{
					"get_site — `resolution` says whether the domain points at this server, `resolution.record` is the record it needs, and `certificate` says what is on disk.",
					"Not resolving and `resolution.managed` is true: sync_dns. False: give the person `resolution.record` to add wherever the domain's DNS is run — or connect_dns_account for that provider and then sync_dns, which has the panel write it.",
					"Resolving with no certificate: request_certificate, then get_task for certbot's own words.",
					"Answering with an error: site_logs, which is the tail of nginx's and the application's own logs.",
				},
			},
		},
		Warnings: []string{
			"The panel owns a site's nginx vhost, its systemd unit and its directory. Writing any " +
				"of those by hand is overwritten by the next deployment.",
		},
	},
	{
		Key:  "backups",
		Page: "Sites → Backups",
		What: "A copy of one site's directory and the database the panel made for it, taken on " +
			"demand or on a schedule, and put back over the top of what is there now. The same " +
			"for every kind of site: what is copied is the directory and the database, not " +
			"anything WordPress or Laravel specific.",
		Tools: []string{"list_site_backups", "create_site_backup", "restore_site_backup",
			"delete_site_backup", "site_backup_schedule"},
		Recipes: []guideRecipe{
			{
				Task: "Before doing something to a site that cannot be undone",
				Steps: []string{
					"create_site_backup with a `note` saying what is about to happen — \"before the WooCommerce 9 upgrade\".",
					"list_site_backups until that backup is `complete`. Do not start the risky work until it is: a backup that is still running is not a backup.",
					"Then do the work. If it goes wrong, restore_site_backup with that backup's id.",
				},
			},
			{
				Task: "Put a site back the way it was",
				Steps: []string{
					"list_site_backups — pick by `created_at` and `note`, and check `status` is `complete`.",
					"Tell the person the date of the backup and that everything since it is lost, including anything added today.",
					"Offer create_site_backup first, so today's version is recoverable too.",
					"restore_site_backup with `confirm: true` once they have said yes. get_site until its status is `active` again.",
				},
			},
			{
				Task: "Set a site up to back itself up",
				Steps: []string{
					"site_backup_schedule with `daily`, `weekly` or `monthly` and a `keep`.",
					"Check the disk first with server_status: a large site kept thirty times over is a full disk, and the panel does not stop you.",
				},
			},
		},
		Warnings: []string{
			"restore_site_backup replaces the whole site — every file under its root and " +
				"everything in its database. There is no undo, the site is offline while it " +
				"runs, and anything done since that backup was taken is gone. Say all of that " +
				"to the person before calling it.",
			"A database somebody attached by hand is never backed up and never overwritten. " +
				"Only the one the panel created for the site is, which is what `database` in " +
				"list_site_backups names.",
			"Backups live on this same server. They are the answer to a bad upgrade, not to a " +
				"dead disk — say so if somebody is relying on them as off-site copies.",
		},
	},
	{
		Key:  "files",
		Page: "Sites → Files",
		What: "The file manager inside one site, rooted at the site's own directory. Nothing " +
			"outside it is reachable through these.",
		Tools: []string{"list_files", "read_file", "write_file", "manage_files", "upload_file",
			"download_file"},
		Recipes: []guideRecipe{
			{
				Task: "Change a site's configuration file",
				Steps: []string{
					"read_file to see what is there now — never write one blind, write_file replaces it whole.",
					"write_file with the complete new contents.",
					"restart_site for a Node.js or Next.js site; PHP picks it up by itself.",
				},
			},
		},
		Warnings: []string{
			"write_file takes the whole file, not a patch. Read it, change what is needed, send it back.",
		},
	},
	{
		Key:  "email",
		Page: "Email",
		What: "Mail domains this server sends and receives for, and the mailboxes on them. " +
			"Postfix, Dovecot, OpenDKIM and Roundcube webmail.",
		Tools: []string{"list_email_domains", "get_email_domain", "create_email_domain",
			"delete_email_domain", "retry_email_domain", "sync_email_dns", "create_mailbox",
			"list_mailboxes", "get_mailbox", "update_mailbox", "set_mailbox_password",
			"repair_mailbox", "delete_mailbox", "configure_mail_server", "test_mail_delivery"},
		Recipes: []guideRecipe{
			{
				Task: "Set up an email address, like name@example.com",
				Steps: []string{
					"list_email_domains — is example.com already there, and is `mail_configured` true?",
					"If the mail server is missing: install_apps with mail (and roundcube for webmail), then configure_mail_server.",
					"If the domain is not there: create_email_domain with example.com, then get_email_domain until its status is active.",
					"create_mailbox with the whole address. The password comes back in the answer, once.",
					"Give the person the address and the password. In Thunderbird that is the whole setup — the domain's autoconfig record hands it the hostnames and ports. Pass on the webmail URL and the IMAP/SMTP settings for anything that cannot look them up.",
				},
			},
			{
				Task: "A mail client will not configure itself for an address on this server",
				Steps: []string{
					"get_email_domain — `autoconfig_host` is the hostname the client asks, `autoconfig_url` the file it fetches.",
					"sync_email_dns if that hostname has no A record. Without it the client falls back to guessing hostnames and ports, which is the setup that ends in 'could not determine the settings'.",
					"retry_email_domain if the record is there and the file is not served: the vhost for it is written during the domain's setup.",
					"Anything that does not speak Mozilla autoconfiguration at all gets `client_settings` typed in by hand.",
				},
			},
			{
				Task: "Who has a mailbox on this server?",
				Steps: []string{
					"list_mailboxes — every address across every domain, flat. Pass `domain` for one domain's.",
					"list_email_domains instead if the question is about the domains rather than the addresses.",
				},
			},
			{
				Task: "Somebody has forgotten their email password",
				Steps: []string{
					"get_mailbox with the address — the panel kept the password, so read it back rather than inventing a new one.",
					"Only if they want it changed: update_mailbox with a new `password`, which is returned once and takes a few seconds to apply.",
					"repair_mailbox if the password is right and Dovecot still refuses the login.",
				},
			},
			{
				Task: "Change who a mailbox belongs to, or how much it may hold",
				Steps: []string{
					"update_mailbox with `name`, `quota_mb`, or both. 0 is unlimited.",
					"The mailbox is set up again with them; delivery is not interrupted.",
				},
			},
			{
				Task: "Mail is not arriving, or a login is refused",
				Steps: []string{
					"get_email_domain — its status, and whether its records were published.",
					"sync_email_dns if the records are missing or the domain moved.",
					"repair_mailbox for a login refused with a password that is definitely right.",
					"test_mail_delivery, then get_task: the whole SMTP conversation is in the log.",
				},
			},
		},
		Warnings: []string{
			"Creating a mail domain takes over its mail records: the MX is pointed at this server " +
				"and any others at the apex are removed, and an SPF already there is replaced. " +
				"That is what makes mail arrive here, and it stops mail arriving wherever it used " +
				"to — so say so first if the domain is on Google Workspace or Microsoft 365. " +
				"Records the panel does not publish, verification strings included, are left " +
				"alone, and get_task has the old values.",
		},
	},
	{
		Key:  "databases",
		Page: "Databases",
		What: "MariaDB and MongoDB databases the panel created, with the credentials it " +
			"generated for each, and the accounts that can reach them.",
		Tools: []string{"list_databases", "get_database", "create_database", "delete_database",
			"set_database_password", "repair_database", "manage_database_users",
			"mongo_connection", "service_credentials"},
		Recipes: []guideRecipe{
			{
				Task: "Give an application a database",
				Steps: []string{
					"A site the panel deploys gets one by itself — check get_site's `database` first.",
					"Otherwise create_database with a name; the user and password are generated and returned.",
					"get_database has everything needed to connect, including the host to use from off the machine.",
				},
			},
			{
				Task: "Credentials are correct and the connection is refused",
				Steps: []string{
					"repair_database — puts the account back at every host the panel creates accounts for.",
				},
			},
		},
	},
	{
		Key:  "dns",
		Page: "DNS",
		What: "Credentials for DNS providers, and the records in the zones they hold. The panel " +
			"publishes a site's and a mail domain's records itself; this is for everything else.",
		Tools: []string{"list_dns_accounts", "list_dns_providers", "connect_dns_account",
			"update_dns_account", "verify_dns_account", "delete_dns_account", "list_dns_zones",
			"list_dns_records", "set_dns_record", "delete_dns_record"},
		Recipes: []guideRecipe{
			{
				Task: "Add a record somebody asked for — a TXT verification, a subdomain elsewhere",
				Steps: []string{
					"set_dns_record with the hostname, the type and the content. The panel finds the credential holding the zone itself.",
					"list_dns_records to read it back.",
				},
			},
		},
		Warnings: []string{
			"A site's own records belong to create_site and delete_site. Writing them here leaves " +
				"the panel's record and the zone disagreeing.",
			"set_dns_record replaces rather than adds: the record already at that name and type, " +
				"and any duplicates of it, are overwritten and removed. The answer's `replaced` " +
				"says what was there. A TXT only replaces one of its own kind — SPF over SPF, " +
				"DMARC over DMARC — so a verification string is never the thing overwritten.",
		},
	},
	{
		Key:  "apps",
		Page: "Apps",
		What: "What the panel installs on the machine: nginx, PHP, MariaDB, MongoDB, Node.js, " +
			"Redis, Certbot, WP-CLI, Composer, phpMyAdmin, the mail server, Roundcube.",
		Tools: []string{"list_apps", "install_apps", "uninstall_app", "retry_app", "detect_apps"},
		Recipes: []guideRecipe{
			{
				Task: "A site type or a feature says something is missing",
				Steps: []string{
					"install_apps with the keys it named — asynchronous, so list_apps says when they are done.",
					"An app stuck at failed: retry_app. An app the panel wrongly believes is missing: detect_apps.",
				},
			},
		},
	},
	{
		Key:  "system",
		Page: "This machine, Settings and Logs",
		What: "The server itself: what it is and how it is doing, the defaults new sites get, " +
			"the panel's own hostname, its updates, its account, and every task it has run.",
		Tools: []string{"server_status", "server_metrics", "panel_settings", "set_panel_domain",
			"panel_update", "panel_account", "ai_access", "list_tasks", "get_task"},
		Recipes: []guideRecipe{
			{
				Task: "Find out what went wrong with something the panel did",
				Steps: []string{
					"list_tasks — every deployment, install, certificate and teardown, newest first.",
					"get_task with the id for the steps and the tail of the console.",
				},
			},
		},
		Warnings: []string{
			"set_panel_domain closes this server's IP address as a way into the panel. The " +
				"hostname becomes the only address there is — the panel's own port answers " +
				"under that name and 404s everything else — so say so before calling it, and " +
				"make sure the name resolves here first.",
		},
	},
	{
		Key:  "shell",
		Page: "Terminal",
		What: "A root shell on the machine. Everything the panel does not cover: inspecting a " +
			"service, tailing a log, running composer, npm, wp or artisan inside a site, apt, " +
			"systemctl, editing anything under /etc.",
		Tools: []string{"run_command"},
		Recipes: []guideRecipe{
			{
				Task: "Run something inside a site as its own user",
				Steps: []string{
					"run_command with `directory` set to the site's root_path from get_site.",
					"For WordPress: `sudo -u www-data wp <command>`; wp refuses to run as root.",
				},
			},
		},
		Warnings: []string{
			"Reading is free; anything else asks the person first where this panel is set to " +
				"confirm. Every command is recorded in the panel's Logs with its output.",
			"Do not do the panel's job here — sites, vhosts, systemd units, databases and " +
				"certificates have tools that also keep the panel's records true.",
		},
	},
}

func (a *App) mcpPanelGuide() *mcp.Tool {
	return &mcp.Tool{
		Name:       "panel_guide",
		Capability: capSitesRead,
		Title:      "How to do things with this panel",
		ReadOnly:   true,
		Description: "The map of this panel: every one of its pages, what that page is for, which " +
			"tools do its work, and the step-by-step for the jobs that take more than one call — " +
			"setting up an email address, putting a site up, chasing down why one does not answer.\n\n" +
			"Read this when a request does not obviously match a tool name, before telling anybody " +
			"that something has to be done in the browser. Nothing here has to be done in the " +
			"browser.",
		Properties: map[string]mcp.Property{
			"topic": {Description: "One area only.",
				Enum: []string{"sites", "files", "email", "databases", "dns", "apps", "system", "shell"}},
		},
		Run: func(ctx context.Context, args mcp.Args) (any, error) {
			topic := strings.ToLower(args.String("topic"))

			areas := make([]map[string]any, 0, len(panelGuide))

			for _, area := range panelGuide {
				if topic != "" && area.Key != topic {
					continue
				}

				recipes := make([]map[string]any, 0, len(area.Recipes))

				for _, recipe := range area.Recipes {
					recipes = append(recipes, map[string]any{
						"task":  recipe.Task,
						"steps": recipe.Steps,
					})
				}

				entry := map[string]any{
					"key":   area.Key,
					"page":  area.Page,
					"what":  area.What,
					"tools": area.Tools,
				}

				if len(recipes) > 0 {
					entry["how_to"] = recipes
				}

				if len(area.Warnings) > 0 {
					entry["watch_out_for"] = area.Warnings
				}

				areas = append(areas, entry)
			}

			if len(areas) == 0 {
				return nil, fmt.Errorf("this panel has no area called %q — call panel_guide with "+
					"no topic for all of them", topic)
			}

			return map[string]any{
				"panel": a.mcpServerName(),
				"areas": areas,
				"rule": "Anything this panel's own pages can do is in this list. If a request " +
					"looks like it has no tool, it is in an area you have not read yet, or it is " +
					"run_command — never a reason to send somebody to the browser.",
			}, nil
		},
	}
}

// guideTools is every tool the guide names, for the test that keeps it honest.
func guideTools() []string {
	seen := map[string]bool{}
	names := []string{}

	for _, area := range panelGuide {
		for _, name := range area.Tools {
			if !seen[name] {
				seen[name] = true

				names = append(names, name)
			}
		}
	}

	sort.Strings(names)

	return names
}
