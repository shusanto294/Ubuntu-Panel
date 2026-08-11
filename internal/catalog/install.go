package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/databases"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// SharedNginxConfig is what the nginx entry's configure step calls to write the
// default server. It is set by the web layer, because the sites package owns
// the configuration and importing it here would be a cycle.
var SharedNginxConfig func(runner *tasks.Runner) error

// ConfigureMail is the same arrangement for the mail server, which has to be
// configured once its packages are down.
var ConfigureMail func(ctx context.Context, userID int64) error

// packagesFor is the apt line for one app.
func (s *Service) packagesFor(key string) string {
	php := s.Store.PHPVersion()

	switch key {
	case "base":
		return "software-properties-common curl wget unzip zip git ufw " +
			"ca-certificates gnupg lsb-release apt-transport-https rsync"

	case "php":
		extensions := []string{"fpm", "cli", "common", "mysql", "pgsql", "curl", "mbstring",
			"xml", "zip", "gd", "bcmath", "intl", "redis"}

		names := make([]string, 0, len(extensions))
		for _, extension := range extensions {
			names = append(names, "php"+php+"-"+extension)
		}

		return strings.Join(names, " ")

	case "mongodb":
		return "mongodb-org"

	case "node":
		return "nodejs"

	case "mail":
		// `ssl-cert` is named explicitly rather than left to a Recommends: the
		// Dovecot configuration points ssl_cert at the snakeoil pair by path,
		// and Dovecot refuses to start when that file is not there — which
		// turns a missing optional package into a mail server that installs
		// and then will not run.
		return "postfix postfix-mysql dovecot-core dovecot-imapd " +
			"dovecot-pop3d dovecot-lmtpd dovecot-mysql opendkim opendkim-tools ssl-cert"
	}

	if entry := Find(key); entry != nil {
		return entry.Packages
	}

	return ""
}

// removePackagesFor is what `apt-get remove` is given.
func (s *Service) removePackagesFor(key string) string {
	entry := Find(key)
	if entry == nil {
		return ""
	}

	if entry.RemovePackages != "" {
		return entry.RemovePackages
	}

	if key == "php" {
		// Only the extensions this panel installs, by name. A bare `php*`
		// would sweep up whatever else is on the box, and removing the whole of
		// PHP is not what "remove PHP-FPM" was asked to do.
		return s.packagesFor("php")
	}

	return entry.Packages
}

// waitForApt is the first step of anything that installs: dpkg allows one
// writer, and on a freshly booted machine that writer is unattended-upgrades.
func waitForApt(list *tasks.List) {
	list.Add("Wait for apt locks",
		"while sudo fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do "+
			"echo \"waiting for another apt process…\"; sleep 3; done; echo \"apt is free\"")
}

// preSteps are repositories, keys and debconf answers: before any apt install.
func (s *Service) preSteps(list *tasks.List, key string) {
	switch key {
	case "mongodb":
		list.Add("Add the MongoDB repository",
			"curl -fsSL https://www.mongodb.org/static/pgp/server-8.0.asc | "+
				"sudo gpg -o /usr/share/keyrings/mongodb-server-8.0.gpg --dearmor --yes",
			"echo \"deb [ arch=amd64,arm64 signed-by=/usr/share/keyrings/mongodb-server-8.0.gpg ] "+
				"https://repo.mongodb.org/apt/ubuntu $(lsb_release -cs)/mongodb-org/8.0 multiverse\" "+
				"| sudo tee /etc/apt/sources.list.d/mongodb-org-8.0.list").Group(key)

	case "node":
		list.Add("Add the Node.js repository",
			fmt.Sprintf("curl -fsSL https://deb.nodesource.com/setup_%s.x | sudo -E bash -",
				s.Store.NodeVersion())).Group(key)

	case "mail":
		// Postfix asks for this at install time and never again. A better name
		// arrives with the first mail domain; this is only what it starts with.
		hostname := s.Store.Setting("mail_hostname", system.Hostname())

		list.Add("Pre-answer the Postfix prompts",
			"echo \"postfix postfix/main_mailer_type select Internet Site\" | sudo debconf-set-selections",
			fmt.Sprintf("echo \"postfix postfix/mailname string %s\" | sudo debconf-set-selections",
				hostname)).Group(key)

	case "php":
		// The ondrej PPA does not publish for every Ubuntu release, and a
		// source that 404s breaks every later `apt-get update`. Prefer the
		// distro's own packages; add the PPA only when it actually publishes
		// for this release.
		list.Add("Choose a PHP source", fmt.Sprintf(
			"wanted=%s; "+
				"if apt-cache policy \"php${wanted}-fpm\" 2>/dev/null | grep -q 'Candidate:' && "+
				"   ! apt-cache policy \"php${wanted}-fpm\" 2>/dev/null | grep -q 'Candidate: (none)'; then "+
				"  echo \"php${wanted}-fpm is available from the configured repositories; no PPA needed.\"; "+
				"else "+
				"  codename=$(lsb_release -cs 2>/dev/null || . /etc/os-release && echo \"$VERSION_CODENAME\"); "+
				"  if curl -fsI --max-time 15 "+
				"\"https://ppa.launchpadcontent.net/ondrej/php/ubuntu/dists/${codename}/Release\" >/dev/null; then "+
				"    echo 'Adding ppa:ondrej/php.'; sudo add-apt-repository -y ppa:ondrej/php; "+
				"    sudo DEBIAN_FRONTEND=noninteractive apt-get update -y; "+
				"  else "+
				"    echo \"ppa:ondrej/php does not publish for ${codename}; using the distro's own PHP packages.\"; "+
				"    sudo rm -f /etc/apt/sources.list.d/ondrej-ubuntu-php-*.sources "+
				"/etc/apt/sources.list.d/ondrej-ubuntu-php-*.list; "+
				"  fi; "+
				"fi", s.Store.PHPVersion())).Group(key)
	}
}

// installSteps are the installs that do not go through apt.
func (s *Service) installSteps(list *tasks.List, key string) {
	switch key {
	case "composer":
		list.Add("Install Composer",
			"command -v composer >/dev/null && composer --version || "+
				"(curl -sS https://getcomposer.org/installer -o /tmp/composer-setup.php && "+
				"sudo php /tmp/composer-setup.php --install-dir=/usr/local/bin --filename=composer && "+
				"rm -f /tmp/composer-setup.php)",
			"composer --version").Group(key)

	case "wpcli":
		list.Add("Install WP-CLI",
			"curl -fsSL -o /tmp/wp-cli.phar "+
				"https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar",
			"sudo mv /tmp/wp-cli.phar /usr/local/bin/wp && sudo chmod +x /usr/local/bin/wp",
			"wp --info --allow-root").Group(key)

	case "pm2":
		list.Add("Install PM2", "sudo npm install -g pm2", "pm2 --version").Group(key)
		list.Add("Let PM2 survive a reboot",
			"sudo env PATH=$PATH:/usr/bin pm2 startup systemd -u $(whoami) --hp $HOME").
			Optional().Group(key)

	case "phpmyadmin":
		// Upstream rather than apt: Debian's package pulls in a web server and
		// wants to configure it, and this machine already has one serving the
		// panel. Replaced wholesale rather than merged — a half-upgraded
		// phpMyAdmin is worse than either version of it.
		list.Add("Install phpMyAdmin",
			"sudo rm -rf /tmp/panel-pma && mkdir -p /tmp/panel-pma",
			"curl -fsSL 'https://files.phpmyadmin.net/phpMyAdmin/5.2.2/phpMyAdmin-5.2.2-all-languages.tar.gz' "+
				"-o /tmp/panel-pma/pma.tar.gz",
			"tar -xzf /tmp/panel-pma/pma.tar.gz -C /tmp/panel-pma --strip-components=1",
			"sudo rm -rf /usr/share/ubuntu-panel/phpmyadmin",
			"sudo mkdir -p /usr/share/ubuntu-panel",
			"sudo mv /tmp/panel-pma /usr/share/ubuntu-panel/phpmyadmin",
			"sudo mkdir -p /var/lib/ubuntu-panel/phpmyadmin-tmp").Group(key)

	case "roundcube":
		list.Add("Install Roundcube",
			"sudo rm -rf /tmp/panel-rc && mkdir -p /tmp/panel-rc",
			"curl -fsSL 'https://github.com/roundcube/roundcubemail/releases/download/1.6.9/"+
				"roundcubemail-1.6.9-complete.tar.gz' -o /tmp/panel-rc/rc.tar.gz",
			"tar -xzf /tmp/panel-rc/rc.tar.gz -C /tmp/panel-rc --strip-components=1",
			"sudo rm -rf /usr/share/ubuntu-panel/roundcube",
			"sudo mkdir -p /usr/share/ubuntu-panel",
			"sudo mv /tmp/panel-rc /usr/share/ubuntu-panel/roundcube").Group(key)
	}
}

// configureSteps are everything after the packages are on disk.
func (s *Service) configureSteps(list *tasks.List, key string) {
	php := s.Store.PHPVersion()
	ram := system.MemoryMB()

	switch key {
	case "base":
		// The web ports are opened here, before the firewall is switched on,
		// and not in the nginx group where they used to live.
		//
		// That ordering locked people out of their own panel: this step enabled
		// ufw with SSH as the only rule, and the rule for 80 and 443 came later
		// with nginx — so anything that failed in between left a firewall up
		// and the panel, which is served over 80, behind it.
		list.Add("Configure the firewall",
			"sudo ufw allow OpenSSH",
			"sudo ufw allow 80/tcp",
			"sudo ufw allow 443/tcp",
			"sudo ufw --force enable",
			"sudo ufw status").Optional().Group(key)

		// apt fails outright when something else holds the dpkg lock, and on a
		// freshly booted machine that something is unattended-upgrades. This
		// tells apt to wait instead — as a drop-in rather than a flag, so it
		// covers the apt runs inside NodeSource's setup script too.
		list.Add("Prepare the web root and apt",
			"sudo mkdir -p /var/www",
			"printf '// Managed by Ubuntu Panel\\nDPkg::Lock::Timeout \"600\";\\n' | "+
				"sudo tee /etc/apt/apt.conf.d/99-ubuntu-panel > /dev/null").Group(key)

	case "nginx":
		list.AddFn("Refuse unknown hostnames", func(runner *tasks.Runner) (string, error) {
			if SharedNginxConfig == nil {
				return "", errors.New("the nginx configuration writer is not wired up")
			}

			if err := SharedNginxConfig(runner); err != nil {
				return "", err
			}

			return "Default server installed; unknown hostnames get a 404.", nil
		}).Group(key)

		list.Add("Start nginx",
			"sudo systemctl enable --now nginx",
			"sudo rm -f /etc/nginx/sites-enabled/default",
			"sudo nginx -t && sudo systemctl reload nginx").Group(key)

		list.Add("Open the web ports", "sudo ufw allow 'Nginx Full'").Optional().Group(key)

	case "php":
		// imagick has been missing from a new Ubuntu on release day more than
		// once, and soap is not always built. Neither is worth failing PHP
		// over — and PHP is a core service, so failing it makes a working
		// install look broken.
		list.Add("Install the optional PHP extensions",
			fmt.Sprintf("sudo DEBIAN_FRONTEND=noninteractive apt-get install -y php%s-imagick php%s-soap || "+
				"echo \"one or more optional extensions are not packaged for this release; continuing\"",
				php, php)).Optional().Group(key)

		// memory_limit is a ceiling per request, not an allocation — but on a
		// 1 GB machine a ceiling of 512M means one runaway page can take the
		// box with it. 256M is above anything WordPress or Laravel needs.
		limit := int64(512)
		if ram > 0 && ram < 2048 {
			limit = 256
		}

		restart := fmt.Sprintf("sudo systemctl restart php%s-fpm", php)

		list.Add("Tune PHP and start PHP-FPM",
			fmt.Sprintf("sudo sed -i \"s/^upload_max_filesize = .*/upload_max_filesize = 128M/; "+
				"s/^post_max_size = .*/post_max_size = 128M/; "+
				"s/^memory_limit = .*/memory_limit = %dM/; "+
				"s/^max_execution_time = .*/max_execution_time = 300/\" /etc/php/%s/fpm/php.ini",
				limit, php),
			fmt.Sprintf("sudo systemctl enable --now php%s-fpm", php),
			restart,
			fmt.Sprintf("php%s -v", php)).Group(key)

		// Ubuntu ships pm = dynamic with five children that stay resident. At
		// 40-80 MB each for WordPress that is most of a 1 GB machine held
		// against requests that are not arriving. `ondemand` spawns per request
		// and lets workers exit again. Above 2 GB there is room to keep them
		// warm, so the default stands and only the ceiling is sized.
		if ram > 0 {
			children := ram / 150 // ~60 MB a worker, 40% of RAM

			if children < 2 {
				children = 2
			}
			if children > 12 {
				children = 12
			}

			pool := fmt.Sprintf(
				"sudo sed -i \"s/^pm.max_children = .*/pm.max_children = %d/; "+
					"s/^;*pm.max_requests = .*/pm.max_requests = 500/\" /etc/php/%s/fpm/pool.d/www.conf",
				children, php)

			if ram < 2048 {
				pool = fmt.Sprintf(
					"sudo sed -i \"s/^pm = .*/pm = ondemand/; "+
						"s/^pm.max_children = .*/pm.max_children = %d/; "+
						"s/^;*pm.process_idle_timeout = .*/pm.process_idle_timeout = 10s/; "+
						"s/^;*pm.max_requests = .*/pm.max_requests = 500/\" /etc/php/%s/fpm/pool.d/www.conf",
					children, php)
			}

			// Optional and separate: a pool file this cannot match is a PHP-FPM
			// that keeps its own defaults, not one that fails to start.
			list.Add("Size the PHP-FPM pool to this machine", pool, restart).Optional().Group(key)
		}

	case "mysql":
		list.Add("Start and secure MariaDB",
			"sudo systemctl enable --now mariadb",
			"sudo mysql -e \"DELETE FROM mysql.user WHERE User=''; "+
				"DROP DATABASE IF EXISTS test; "+
				"DELETE FROM mysql.db WHERE Db='test' OR Db='test\\\\_%'; FLUSH PRIVILEGES;\"",
			"sudo mysql -e \"SELECT VERSION();\"").Group(key)

		// MariaDB's defaults assume a machine with memory to spare. On the 1 GB
		// VPS this panel is most often installed on, the stock buffer pools
		// plus PHP-FPM plus nginx do not fit, and the OOM killer takes
		// whichever it reaches first.
		if ram > 0 {
			pool := int64(64)
			connections := int64(40)
			small := int64(8)

			switch {
			case ram >= 4096:
				pool, connections, small = ram/4, 151, 32
			case ram >= 2048:
				pool, connections, small = 192, 80, 32
			}

			write := fmt.Sprintf(
				"printf '%%s\\n' "+
					"'# Written by Ubuntu Panel, sized for a machine with %d MB of RAM.' "+
					"'# Delete this file and restart MariaDB to go back to the defaults.' "+
					"'[mysqld]' "+
					"'innodb_buffer_pool_size = %dM' "+
					"'innodb_log_buffer_size = 8M' "+
					"'key_buffer_size = %dM' "+
					"'aria_pagecache_buffer_size = %dM' "+
					"'tmp_table_size = %dM' "+
					"'max_heap_table_size = %dM' "+
					"'max_connections = %d' "+
					"'performance_schema = OFF' "+
					"| sudo tee /etc/mysql/mariadb.conf.d/60-ubuntu-panel.cnf > /dev/null",
				ram, pool, small, small, small, small, connections)

			// A configuration file MariaDB will not start with is worse than no
			// tuning at all, and this one is written on a machine nobody is
			// watching. So it is applied and then proved: if the restart fails,
			// the file goes and MariaDB comes back on its own defaults.
			apply := "if sudo systemctl restart mariadb; then " +
				"  echo 'MariaDB restarted with the panel tuning.'; " +
				"else " +
				"  echo 'MariaDB would not start with the tuning; reverting to the defaults.'; " +
				"  sudo rm -f /etc/mysql/mariadb.conf.d/60-ubuntu-panel.cnf; " +
				"  sudo systemctl restart mariadb; " +
				"fi"

			list.Add("Size MariaDB to this machine", write, apply).Optional().Group(key)
		}

	case "redis":
		// Generated once and kept: Redis is configured with it, so regenerating
		// would lock the panel out of its own service.
		password, _ := s.Store.RememberSecret("redis_password", 28)

		list.Add("Set the Redis password",
			fmt.Sprintf("sudo sed -i \"s/^# *requirepass .*/requirepass %[1]s/; "+
				"s/^requirepass .*/requirepass %[1]s/\" /etc/redis/redis.conf", password),
			fmt.Sprintf("grep -q \"^requirepass\" /etc/redis/redis.conf || "+
				"echo \"requirepass %s\" | sudo tee -a /etc/redis/redis.conf > /dev/null", password),
			"sudo systemctl enable --now redis-server",
			"sudo systemctl restart redis-server").Group(key)

		// Redis with no ceiling grows until the kernel picks a victim, and on a
		// small machine the victim is usually MariaDB. A tenth of RAM is plenty
		// for the object cache a WordPress or Laravel site puts here, and
		// allkeys-lru means reaching the ceiling evicts rather than starts
		// refusing writes.
		if ram > 0 {
			cap := ram / 10

			if cap < 32 {
				cap = 32
			}
			if cap > 512 {
				cap = 512
			}

			list.Add("Cap the Redis memory",
				fmt.Sprintf("sudo sed -i \"s/^# *maxmemory .*/maxmemory %[1]dmb/; "+
					"s/^maxmemory .*/maxmemory %[1]dmb/; "+
					"s/^# *maxmemory-policy .*/maxmemory-policy allkeys-lru/; "+
					"s/^maxmemory-policy .*/maxmemory-policy allkeys-lru/\" /etc/redis/redis.conf; "+
					"grep -q '^maxmemory ' /etc/redis/redis.conf || "+
					"echo 'maxmemory %[1]dmb' | sudo tee -a /etc/redis/redis.conf > /dev/null; "+
					"grep -q '^maxmemory-policy' /etc/redis/redis.conf || "+
					"echo 'maxmemory-policy allkeys-lru' | sudo tee -a /etc/redis/redis.conf > /dev/null", cap),
				"sudo systemctl restart redis-server").Optional().Group(key)
		}

	case "mongodb":
		// The whole of it — the administrator, authorization, the listener and
		// the firewall — is one routine shared with the database pages, which
		// run it again before every create. See databases.EnsureMongoServer.
		list.AddFn("Configure MongoDB", func(runner *tasks.Runner) (string, error) {
			if err := databases.EnsureMongoServer(s.Store, runner); err != nil {
				return "", err
			}

			return "MongoDB is ready.", nil
		}).Group(key)

	case "node":
		list.Add("Check Node.js", "node -v && npm -v").Group(key)
		list.Add("Install package managers", "sudo npm install -g pnpm yarn --silent").
			Optional().Group(key)

	case "phpmyadmin":
		user := s.Store.Setting("system_user", "ubuntupanel")

		list.Add("Configure phpMyAdmin",
			fmt.Sprintf("sudo chown -R %[1]s:%[1]s /var/lib/ubuntu-panel/phpmyadmin-tmp && "+
				"sudo chmod 700 /var/lib/ubuntu-panel/phpmyadmin-tmp", user)).Group(key)

	case "mail":
		list.Add("Start the mail server",
			"sudo systemctl enable --now postfix dovecot opendkim",
			"sudo systemctl restart postfix dovecot").Group(key)

		list.Add("Open the mail ports", "sudo ufw allow 25,465,587,993,995/tcp").
			Optional().Group(key)
	}
}

// InstallQueued installs everything currently queued, as one task. Runs on a
// queue worker, never in a request.
func (s *Service) InstallQueued(ctx context.Context, userID int64) error {
	s.SyncRows()

	queued := s.Store.QueuedServices(ctx)
	if len(queued) == 0 {
		return nil
	}

	taskID, err := s.Store.CreateTask(ctx, "provision", "services.install", userID, 0,
		"Installing software")
	if err != nil {
		return err
	}

	for _, key := range queued {
		s.Store.SetServiceStatus(key, store.ServiceInstalling, "", "")
		s.Store.SetServiceTask(key, taskID)
	}

	list := &tasks.List{}

	waitForApt(list)

	// Every app's pre-steps first, then one apt transaction for all of them,
	// then each app's own configuration.
	for _, key := range queued {
		s.preSteps(list, key)
	}

	var packages []string

	for _, key := range queued {
		if line := s.packagesFor(key); line != "" {
			packages = append(packages, line)
		}
	}

	if len(packages) > 0 {
		// One app in the batch means this shared transaction is entirely its
		// work, so attribute it and the page can show the apt install as that
		// app's own step — which is the step it spends nearly all of its time
		// in. With several queued the packages genuinely go down together and
		// belong to no single one; the page falls back to the task's current
		// step for those.
		attribute := func() {
			if len(queued) == 1 {
				list.Group(queued[0])
			}
		}

		list.Add("Update package lists",
			"sudo DEBIAN_FRONTEND=noninteractive apt-get update -y || "+
				"echo \"apt-get update reported errors; continuing\"")
		attribute()

		list.Add("Install the packages",
			"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "+strings.Join(packages, " "))
		attribute()
	}

	for _, key := range queued {
		s.installSteps(list, key)
		s.configureSteps(list, key)
	}

	ok := tasks.New(s.Store, taskID).Run(list)

	// Re-probe rather than trusting the steps: an app whose configure step
	// failed may still be installed and usable, and one whose install
	// "succeeded" may not have produced a working binary.
	s.Detect(queued)

	for _, key := range queued {
		if !s.Store.ServiceInstalled(key) {
			s.Store.SetServiceStatus(key, store.ServiceFailed, "",
				"The install finished but the app was not detected afterwards.")
		}
	}

	// The mail server is configured here, once its packages are down. apt
	// having succeeded says nothing about whether Postfix and Dovecot know
	// about virtual domains.
	for _, key := range queued {
		if key != "mail" || !s.Store.ServiceInstalled("mail") || ConfigureMail == nil {
			continue
		}

		if err := ConfigureMail(ctx, userID); err != nil {
			s.Store.SetServiceStatus("mail", store.ServiceFailed, "",
				"The packages installed but the mail server could not be configured. "+
					"Its log says where it stopped.")
			ok = false
		}
	}

	if !ok {
		return errors.New("part of the install did not succeed")
	}

	return nil
}

// ------------------------------------------------------------ uninstalling

// resourcesHolding is the panel's own rows that live inside an engine.
//
// Removing MariaDB with databases in it, or nginx with sites on it, destroys
// something that is not the package's to destroy — so those are refusals rather
// than warnings.
func (s *Service) resourcesHolding(ctx context.Context, key string) string {
	plural := func(count int, singular, many string) string {
		if count == 1 {
			return singular
		}

		return many
	}

	switch key {
	case "mysql", "postgres", "mongodb":
		if count := s.Store.DatabaseCount(ctx, key); count > 0 {
			return fmt.Sprintf("%d database%s still live in %s. Delete them from the Databases page first.",
				count, plural(count, "", "s"), Label(key))
		}

	case "nginx":
		if count := s.Store.SiteCount(ctx); count > 0 {
			return fmt.Sprintf("%d site%s served by nginx. Delete them from the Sites page first.",
				count, plural(count, " is", "s are"))
		}

	case "mail":
		if count := s.Store.EmailDomainCount(ctx); count > 0 {
			return fmt.Sprintf("%d mail domain%s still configured, and their mailboxes hold mail. "+
				"Delete them from the Email page first.", count, plural(count, " is", "s are"))
		}
	}

	return ""
}

// UninstallBlockers is why each app cannot be removed right now, keyed by app.
//
// The Apps page renders one row per catalogue entry, and each row wants to know
// whether its Remove button is live. This reads the installed set once and does
// the rest in memory.
func (s *Service) UninstallBlockers(ctx context.Context) map[string]string {
	installed := map[string]bool{}

	for _, key := range s.Store.InstalledServiceKeys(ctx) {
		installed[key] = true
	}

	blockers := map[string]string{}

	for _, entry := range Entries {
		if !entry.Removable {
			blockers[entry.Key] = entry.Label +
				" is what the panel itself runs on, so it cannot be removed from here."
			continue
		}

		if !installed[entry.Key] {
			continue // nothing to block; it is not there
		}

		if holding := s.resourcesHolding(ctx, entry.Key); holding != "" {
			blockers[entry.Key] = holding
			continue
		}

		// Anything installed that lists this in Requires would break.
		var dependents []string

		for _, other := range Entries {
			if !installed[other.Key] {
				continue
			}

			for _, requirement := range other.Requires {
				if requirement == entry.Key {
					dependents = append(dependents, other.Label)
					break
				}
			}
		}

		if len(dependents) == 0 {
			continue
		}

		is, needs, those := "are", "need", "those"
		if len(dependents) == 1 {
			is, needs, those = "is", "needs", "it"
		}

		blockers[entry.Key] = fmt.Sprintf("%s %s installed and %s %s. Remove %s first.",
			strings.Join(dependents, ", "), is, needs, entry.Label, those)
	}

	return blockers
}

// UninstallBlocker is the same answer for one app.
func (s *Service) UninstallBlocker(ctx context.Context, key string) string {
	entry := Find(key)
	if entry == nil {
		return "Unknown app."
	}

	if entry.Removable && !s.Store.ServiceInstalled(key) {
		return "It is not installed."
	}

	return s.UninstallBlockers(ctx)[key]
}

// removeSteps are removals apt knows nothing about, because the install did not
// use it.
func (s *Service) removeSteps(list *tasks.List, key string) {
	switch key {
	case "composer":
		list.Add("Remove Composer", "sudo rm -f /usr/local/bin/composer").Group(key)
	case "wpcli":
		list.Add("Remove WP-CLI", "sudo rm -f /usr/local/bin/wp").Group(key)
	case "pm2":
		list.Add("Remove PM2", "sudo npm rm -g pm2 || true").Group(key)
	case "phpmyadmin":
		list.Add("Remove phpMyAdmin",
			"sudo rm -rf /usr/share/ubuntu-panel/phpmyadmin /var/lib/ubuntu-panel/phpmyadmin-tmp").Group(key)
	case "roundcube":
		list.Add("Remove Roundcube", "sudo rm -rf /usr/share/ubuntu-panel/roundcube").Group(key)
	}
}

// Uninstall removes one app: apt-get remove (never purge — configuration and
// data stay, so reinstalling brings it back), then autoremove, then a re-probe.
func (s *Service) Uninstall(ctx context.Context, key string, userID int64) error {
	entry := Find(key)
	if entry == nil {
		return errors.New("unknown app")
	}

	// Re-checked here and not only in the handler: this runs on a worker some
	// time after the click, and a site or database may have been created in
	// between.
	if blocker := s.UninstallBlocker(ctx, key); blocker != "" {
		s.Store.SetServiceStatus(key, store.ServiceInstalled, "", blocker)

		return errors.New(blocker)
	}

	taskID, err := s.Store.CreateTask(ctx, "provision", "services.uninstall", userID, 0,
		"Removing "+entry.Label)
	if err != nil {
		return err
	}

	s.Store.SetServiceTask(key, taskID)

	list := &tasks.List{}

	waitForApt(list)

	// Stopped before the packages go, so the removal is not fighting a running
	// daemon for its own files. Optional: plenty of these have no unit.
	list.Add("Stop the service",
		fmt.Sprintf("sudo systemctl stop %s 2>/dev/null || true", entry.Key)).Optional().Group(key)

	if packages := s.removePackagesFor(key); packages != "" {
		// remove, never purge. Configuration and data stay on disk, so
		// installing it again gets the machine back rather than starting it
		// over — and a misclick here costs a download, not a database.
		list.Add("Remove "+entry.Label,
			"sudo DEBIAN_FRONTEND=noninteractive apt-get remove -y "+packages).Group(key)
	}

	s.removeSteps(list, key)

	list.Add("Remove packages nothing needs any more",
		"sudo DEBIAN_FRONTEND=noninteractive apt-get autoremove -y").Optional().Group(key)

	ok := tasks.New(s.Store, taskID).Run(list)

	// The row follows the machine, not the exit status: a removal that failed
	// halfway may still have taken the binary with it.
	s.Detect([]string{key})

	if s.Store.ServiceInstalled(key) {
		s.Store.SetServiceStatus(key, store.ServiceInstalled, "",
			"The removal ran but the app is still detected on the machine.")

		return errors.New("the app is still installed")
	}

	if !ok {
		return errors.New("part of the removal did not succeed")
	}

	return nil
}
