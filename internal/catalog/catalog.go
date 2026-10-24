// Package catalog is everything the panel can install on this machine.
//
// The table below is the Go form of the C panel's catalog.c, which was the C
// form of config('panel.services'). The install recipe is split the same four
// ways: pre-steps (repositories, keys, debconf answers), apt packages, non-apt
// installs, and configuration — which is what lets a bulk install put every
// service's packages into a single `apt-get install`. dpkg takes an exclusive
// lock, so that single transaction is the only real way to make "install
// everything" fast.
package catalog

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// Entry is one installable app.
type Entry struct {
	Key         string
	Label       string
	Group       string // core | database | runtime | mail
	Description string
	Core        bool
	Default     bool // pre-ticked
	Requires    []string
	Detect      string // shell test deciding whether it is present
	Version     string // command whose first line is shown

	// Packages is the apt line for an entry whose install is nothing more than
	// that. The ones with a repository to add, a version to interpolate or a
	// tarball to fetch compute it in packagesFor instead.
	Packages string

	// RemovePackages is what `apt-get remove` is given. Empty means "whatever
	// was installed", which is right nearly always — mail is the exception,
	// where the packages installed include ones shared with the rest of the
	// system.
	RemovePackages string

	// Removable is off for the base packages: curl, git and ca-certificates
	// are what the panel itself runs on, and removing them from inside the
	// panel is a way to lose the panel.
	Removable bool

	// Keywords are searched alongside the label and description, so "docker"
	// finds Containers and "cache" finds Redis.
	Keywords string

	// SizeHint is roughly what lands on disk, shown before installing.
	SizeHint string

	// OnlyWhenAsked keeps an app out of the first-run install, even the `all`
	// one.
	//
	// "Everything in the catalogue" is a reasonable thing to want from a web
	// server, a database and a runtime: having them and not using them costs
	// disk. It is not reasonable for an app that changes what the machine is.
	// A nameserver listening on port 53 of a public address is a service
	// somebody else can now send traffic to, and putting one on every panel
	// because the catalogue grew is not a default anybody chose.
	OnlyWhenAsked bool
}

// Entries is the catalogue. Array order is install order.
//
// Detect runs as a shell test rather than a package query because "is nginx
// installed" and "is the nginx package installed" are different questions on a
// machine somebody has been at by hand.
var Entries = []Entry{
	{
		Key: "base", Label: "Base packages", Group: "core",
		Description: "curl, git, unzip, gnupg, ufw and the apt repositories the rest depends on.",
		Core:        true, Default: true,
		Detect:  "command -v curl && command -v git && command -v unzip && command -v gpg",
		Version: "lsb_release -ds 2>/dev/null || . /etc/os-release && echo \"$PRETTY_NAME\"",
		// Never removable: the panel itself runs on these.
		Removable: false,
		Keywords:  "curl git unzip gnupg ufw essentials", SizeHint: "~40 MB",
	},
	{
		Key: "nginx", Label: "nginx", Group: "core",
		Description: "Web server. Every site gets a vhost here.",
		Core:        true, Default: true, Requires: []string{"base"},
		Detect: "command -v nginx", Version: "nginx -v 2>&1",
		Packages: "nginx", RemovePackages: "nginx nginx-common nginx-core", Removable: true,
		Keywords: "web server http proxy vhost", SizeHint: "~5 MB",
	},
	{
		Key: "php", Label: "PHP-FPM", Group: "core",
		Description: "PHP-FPM with the extensions WordPress and Laravel need.",
		Core:        true, Default: true, Requires: []string{"base"},
		Detect: "command -v php", Version: "php -v 2>/dev/null | head -1",
		Removable: true,
		Keywords:  "php fpm wordpress laravel runtime", SizeHint: "~90 MB",
	},
	{
		Key: "composer", Label: "Composer", Group: "core",
		Description: "PHP dependency manager, needed for Laravel deployments.",
		Core:        true, Default: true, Requires: []string{"php"},
		Detect: "command -v composer", Version: "composer --version 2>/dev/null | head -1",
		Removable: true,
		Keywords:  "php dependency package manager laravel", SizeHint: "~3 MB",
	},
	{
		Key: "certbot", Label: "Certbot", Group: "core",
		Description: "Let's Encrypt client for free TLS certificates.",
		Core:        true, Default: true, Requires: []string{"nginx"},
		Detect: "command -v certbot", Version: "certbot --version 2>&1 | head -1",
		Packages: "certbot python3-certbot-nginx", Removable: true,
		Keywords: "ssl tls https certificate letsencrypt acme", SizeHint: "~15 MB",
	},
	{
		Key: "mysql", Label: "MariaDB", Group: "database",
		Description: "Required for WordPress and most Laravel sites.",
		Core:        true, Default: true, Requires: []string{"base"},
		Detect: "command -v mariadb || command -v mysql", Version: "mysql --version 2>/dev/null | head -1",
		Packages: "mariadb-server mariadb-client", RemovePackages: "mariadb-server mariadb-client",
		Removable: true,
		Keywords:  "mysql sql database relational wordpress", SizeHint: "~250 MB",
	},
	{
		Key: "mongodb", Label: "MongoDB", Group: "database",
		Description: "Document database, installed from the official repo.",
		Requires:    []string{"base"},
		Detect:      "command -v mongod", Version: "mongod --version 2>/dev/null | head -1",
		RemovePackages: "mongodb-org mongodb-org-server mongodb-org-mongos mongodb-org-tools " +
			"mongodb-org-database mongodb-org-shell mongodb-mongosh",
		Removable: true,
		Keywords:  "nosql document database json", SizeHint: "~450 MB",
	},
	{
		Key: "phpmyadmin", Label: "phpMyAdmin", Group: "database",
		Description: "Web interface for MariaDB, served from the panel's own origin.",
		Default:     true, Requires: []string{"mysql", "php", "nginx"},
		Detect: "test -f /usr/share/ubuntu-panel/phpmyadmin/index.php",
		Version: "grep -oP \"VERSION', '\\K[^']+\" " +
			"/usr/share/ubuntu-panel/phpmyadmin/libraries/classes/Version.php 2>/dev/null | head -1",
		Removable: true,
		Keywords:  "mysql mariadb gui web admin sql browser", SizeHint: "~60 MB",
	},
	{
		Key: "redis", Label: "Redis", Group: "database",
		Description: "Cache, queue and session store. Password protected.",
		Default:     true, Requires: []string{"base"},
		Detect: "command -v redis-server", Version: "redis-server --version 2>/dev/null | head -1",
		Packages: "redis-server", RemovePackages: "redis-server redis-tools", Removable: true,
		Keywords: "cache key value queue session store", SizeHint: "~15 MB",
	},
	{
		Key: "node", Label: "Node.js", Group: "runtime",
		Description: "Needed for Node.js and Next.js sites.",
		Default:     true, Requires: []string{"base"},
		Detect: "command -v node", Version: "node -v 2>/dev/null",
		RemovePackages: "nodejs", Removable: true,
		Keywords: "javascript npm next react runtime", SizeHint: "~90 MB",
	},
	{
		Key: "pm2", Label: "PM2", Group: "runtime",
		Description: "Node.js process manager. The panel runs Node and Next.js sites under " +
			"systemd, so this is for managing processes yourself.",
		Default: true, Requires: []string{"node"},
		Detect: "command -v pm2", Version: "pm2 --version 2>/dev/null | tail -1",
		Removable: true,
		Keywords:  "node process manager daemon cluster", SizeHint: "~30 MB",
	},
	{
		Key: "wpcli", Label: "WP-CLI", Group: "runtime",
		Description: "Needed to install and manage WordPress sites.",
		Core:        true, Default: true, Requires: []string{"php"},
		Detect: "command -v wp", Version: "wp --version --allow-root 2>/dev/null | head -1",
		Removable: true,
		Keywords:  "wordpress cli command line php", SizeHint: "~7 MB",
	},
	{
		Key: "dns", Label: "DNS server", Group: "dns",
		OnlyWhenAsked: true,
		Description: "BIND, so this machine can host zones itself. Point a domain's " +
			"nameservers here and manage its records in the panel instead of at a " +
			"registrar or Cloudflare.",
		Requires: []string{"base"},
		Detect:   "command -v named", Version: "named -v 2>/dev/null | head -1",
		Packages:       "bind9 bind9-utils bind9-dnsutils",
		RemovePackages: "bind9 bind9-utils bind9-dnsutils bind9-libs",
		Removable:      true,
		Keywords: "dns bind named nameserver zone authoritative glue registrar " +
			"ns1 ns2 soa",
		SizeHint: "~15 MB",
	},
	{
		Key: "mail", Label: "Mail server", Group: "mail",
		Description: "Postfix, Dovecot and OpenDKIM with mailboxes stored in MariaDB.",
		Default:     true, Requires: []string{"mysql"},
		// `sudo` on the second half is not decoration: the install ends by
		// taking every other-user permission off /etc/dovecot, so the panel
		// user cannot traverse it afterwards. Without sudo this probe says
		// "not installed" about a mail server it just finished installing.
		Detect:  "command -v postfix && sudo test -f /etc/dovecot/dovecot-sql.conf.ext",
		Version: "postconf -h mail_version 2>/dev/null",
		// Not ssl-cert: the snakeoil pair it ships is used by more than mail.
		RemovePackages: "postfix postfix-mysql dovecot-core dovecot-imapd dovecot-pop3d " +
			"dovecot-lmtpd dovecot-mysql opendkim opendkim-tools",
		Removable: true,
		Keywords:  "email smtp imap postfix dovecot dkim mailbox", SizeHint: "~60 MB",
	},
	{
		Key: "roundcube", Label: "Roundcube webmail", Group: "mail",
		Description: "Webmail for every mail domain, served at mail.<domain>.",
		Default:     true, Requires: []string{"mail", "php", "nginx", "mysql"},
		Detect: "test -f /usr/share/ubuntu-panel/roundcube/index.php",
		Version: "grep -oP \"RCMAIL_VERSION', '\\K[^']+\" " +
			"/usr/share/ubuntu-panel/roundcube/program/include/iniset.php 2>/dev/null | head -1",
		Removable: true,
		Keywords:  "webmail email browser imap client", SizeHint: "~80 MB",
	},
}

// Find returns the entry for a key, or nil.
func Find(key string) *Entry {
	for i := range Entries {
		if Entries[i].Key == key {
			return &Entries[i]
		}
	}

	return nil
}

// Label is an app's name, falling back to the bare key.
func Label(key string) string {
	if entry := Find(key); entry != nil {
		return entry.Label
	}

	return key
}

// Exists reports whether the catalogue knows this key.
func Exists(key string) bool { return Find(key) != nil }

// SortOrder is the entry's index, which is also its install order.
func SortOrder(key string) int {
	for i := range Entries {
		if Entries[i].Key == key {
			return i
		}
	}

	return len(Entries)
}

// WithDependencies expands a selection to include everything it depends on, in
// install order.
func WithDependencies(keys []string) []string {
	seen := map[string]bool{}
	var resolved []string

	var resolve func(key string)

	resolve = func(key string) {
		entry := Find(key)
		if entry == nil || seen[key] {
			return
		}

		seen[key] = true

		for _, requirement := range entry.Requires {
			resolve(requirement)
		}

		resolved = append(resolved, key)
	}

	for _, key := range keys {
		resolve(key)
	}

	// Back into catalogue order: a depth-first walk can put a dependency after
	// something that was named earlier and needs it.
	sorted := make([]string, 0, len(resolved))

	for _, entry := range Entries {
		if seen[entry.Key] {
			sorted = append(sorted, entry.Key)
		}
	}

	return sorted
}

// Service ties the catalogue to the panel's own rows.
type Service struct {
	Store *store.Store

	// detected is when each app was last probed.
	//
	// Detection forks a shell per app and another for its version. One page
	// doing that for seven apps every three seconds — which is what the site
	// form does while it waits for an install — is a hundred processes a
	// minute on a machine already spending everything it has on apt, and the
	// request that arrives next waits behind them.
	detected sync.Map // key -> time.Time
}

// New wires it up and makes sure every catalogue entry has a row.
func New(st *store.Store) *Service {
	service := &Service{Store: st}
	service.SyncRows()

	return service
}

// SyncRows makes sure every catalogue entry has a row, in catalogue order.
// Servers set up before an app joined the catalogue get theirs here.
func (s *Service) SyncRows() {
	for i, entry := range Entries {
		s.Store.EnsureService(entry.Key, i)
	}
}

// Detect re-probes the machine for the named apps, or all of them when keys is
// empty.
func (s *Service) Detect(keys []string) {
	s.detect(keys, 0)
}

// DetectFresh is the same, skipping anything probed within maxAge. Request
// handlers use this; the installer uses Detect, because after an install the
// answer has just changed.
func (s *Service) DetectFresh(keys []string, maxAge time.Duration) {
	if maxAge <= 0 {
		maxAge = time.Second
	}

	s.detect(keys, maxAge)
}

func (s *Service) detect(keys []string, maxAge time.Duration) {
	wanted := map[string]bool{}

	for _, key := range keys {
		wanted[key] = true
	}

	for _, entry := range Entries {
		if len(keys) > 0 && !wanted[entry.Key] {
			continue
		}

		if maxAge > 0 {
			if last, ok := s.detected.Load(entry.Key); ok {
				if time.Since(last.(time.Time)) < maxAge {
					continue
				}
			}
		}

		s.detected.Store(entry.Key, time.Now())

		if !proc.Ok(entry.Detect) {
			// Only ever downgrade a row that claims to be installed. A queued
			// or installing row is mid-flight, and overwriting it here would
			// make the page flip back to "not installed" while the install is
			// running.
			if s.Store.ServiceInstalled(entry.Key) {
				s.Store.SetServiceStatus(entry.Key, store.ServiceNotInstalled, "", "")
			}

			continue
		}

		version := ""

		if entry.Version != "" {
			if result := proc.Run(entry.Version, 30*time.Second); result.OK() {
				version = strings.TrimSpace(result.Output)
			}
		}

		s.Store.SetServiceStatus(entry.Key, store.ServiceInstalled, version, "")
	}
}

// Queue marks apps (with their dependencies) for installation and returns how
// many rows changed.
func (s *Service) Queue(keys []string, force bool) int {
	s.SyncRows()

	queued := 0

	for _, key := range WithDependencies(keys) {
		if s.Store.QueueService(key, force) {
			queued++
		}
	}

	return queued
}

// Summary is the row shape the Apps page renders.
func (s *Service) Summary(ctx context.Context) []map[string]any {
	rows, err := s.Store.Services(ctx)
	if err != nil {
		rows = map[string]*store.Service{}
	}

	blockers := s.UninstallBlockers(ctx)

	summary := make([]map[string]any, 0, len(Entries))

	for _, entry := range Entries {
		row := rows[entry.Key]

		status := store.ServiceNotInstalled
		version, lastError := "", ""
		taskID := int64(0)

		if row != nil {
			status = row.Status
			version = row.Version
			lastError = row.LastError
			taskID = row.TaskID
		}

		// An empty list rather than null: the page filters and searches over
		// this, and `null.includes(…)` takes the whole card down with it.
		requires := entry.Requires
		if requires == nil {
			requires = []string{}
		}

		summary = append(summary, map[string]any{
			"key":         entry.Key,
			"label":       entry.Label,
			"group":       entry.Group,
			"description": entry.Description,
			"core":        entry.Core,
			"default":     entry.Default,
			"requires":    requires,
			"keywords":    entry.Keywords,
			"size_hint":   entry.SizeHint,
			"removable":   entry.Removable,
			"status":      status,
			"version":     version,
			"last_error":  lastError,
			"task_id":     taskID,
			"blocker":     nullable(blockers[entry.Key]),
		})
	}

	return summary
}

// Requirements is the subset a page depends on, with the status of each, so
// the page can offer to install what is missing rather than send you elsewhere.
func (s *Service) Requirements(ctx context.Context, keys []string) []map[string]any {
	rows, err := s.Store.Services(ctx)
	if err != nil {
		rows = map[string]*store.Service{}
	}

	requirements := make([]map[string]any, 0, len(keys))

	for _, key := range keys {
		entry := Find(key)
		if entry == nil {
			continue
		}

		status := store.ServiceNotInstalled
		version := ""

		if row := rows[key]; row != nil {
			status = row.Status
			version = row.Version
		}

		requirements = append(requirements, map[string]any{
			"key":       entry.Key,
			"label":     entry.Label,
			"status":    status,
			"version":   version,
			"size_hint": entry.SizeHint,
		})
	}

	return requirements
}

func nullable(value string) any {
	if value == "" {
		return nil
	}

	return value
}
