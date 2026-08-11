package sites

// The applications the panel installs itself, rather than deploying whatever a
// repository contains: Nextcloud, n8n and OpenClaw. WordPress is the same idea
// and lives in service.go, where it was before these joined it.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// NextcloudArchive is the release the panel installs. `latest` rather than a
// pinned version: Nextcloud's own upgrader takes over from here, and pinning
// would install a version that asks to be updated on first login.
const NextcloudArchive = "https://download.nextcloud.com/server/releases/latest.zip"

// nextcloudModules are the PHP extensions Nextcloud checks for on its setup
// page and complains about ever afterwards. Installed up front, because the
// complaint arrives after the operator has already been told the deployment
// succeeded.
var nextcloudModules = []string{
	"gd", "curl", "mbstring", "xml", "zip", "intl", "bcmath", "gmp",
	"imagick", "apcu", "mysql", "bz2", "opcache",
}

// nextcloudPHP is the php.ini Nextcloud asks for. 512M is its own documented
// minimum; the default 128M is what makes a large upload or an app install
// fail with a blank page.
const nextcloudPHP = "; Managed by Ubuntu Panel for Nextcloud\n" +
	"memory_limit = 512M\n" +
	"upload_max_filesize = 512M\n" +
	"post_max_size = 512M\n" +
	"max_execution_time = 3600\n" +
	"max_input_time = 3600\n" +
	"output_buffering = Off\n" +
	"opcache.enable = 1\n" +
	"opcache.interned_strings_buffer = 16\n" +
	"opcache.max_accelerated_files = 10000\n" +
	"opcache.memory_consumption = 128\n" +
	"opcache.save_comments = 1\n" +
	"opcache.revalidate_freq = 60\n" +
	"apc.enable_cli = 1\n"

// ensureNodeRuntime makes sure the Node.js on this machine is new enough for
// the application about to be installed.
//
// n8n and OpenClaw both refuse to start on a Node older than their minimum,
// and the way that shows up otherwise is a systemd unit that restarts for ever
// with a message nobody reads. The panel's default is 22, so this is usually
// a no-op; it is the server installed a year ago that needs it.
func (s *Service) ensureNodeRuntime(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		minimum := site.NodeMinimum()
		if minimum == 0 {
			return "No minimum Node.js version for this type.", nil
		}

		current := nodeMajor()

		if current >= minimum {
			return fmt.Sprintf("Node.js %d is new enough (%s needs %d or later).",
				current, site.TypeLabel(), minimum), nil
		}

		// Upgraded to the type's minimum rather than to the newest release: an
		// upgrade is a machine-wide change, and the smallest one that works is
		// the one least likely to disturb another site on the box.
		install := minimum
		if install < 22 {
			install = 22
		}

		runner.Writef("Node.js %d is older than %s needs (%d). Installing Node.js %d.\n",
			current, site.TypeLabel(), minimum, install)

		if err := runner.Must(fmt.Sprintf(
			"curl -fsSL https://deb.nodesource.com/setup_%d.x | sudo -E bash - && "+
				"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs", install),
			15*time.Minute); err != nil {
			return "", err
		}

		if after := nodeMajor(); after < minimum {
			return "", fmt.Errorf(
				"%s needs Node.js %d or later and this machine still has %d after the upgrade. "+
					"Install a newer Node.js from the Apps page, then redeploy.",
				site.TypeLabel(), minimum, after)
		}

		return fmt.Sprintf("Node.js upgraded to %d.", nodeMajor()), nil
	}
}

// nodeMajor is the major version of the node on PATH, or zero when there is
// none.
func nodeMajor() int {
	output := proc.Run("node -v 2>/dev/null", 30*time.Second).Output

	digits := strings.TrimPrefix(strings.TrimSpace(output), "v")
	major, _, _ := strings.Cut(digits, ".")

	value, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}

	return value
}

// installGlobally installs an npm package for the whole machine, and copes
// with the cache npm keeps under root's home.
//
// npm answers from that cache's copy of the registry index, and a copy written
// before a dependency published its newest version reports that version as one
// that does not exist:
//
//	npm error notarget No matching version found for @ai-sdk/openai@4.0.38
//
// on a machine where it exists and is the current release. --prefer-online
// makes npm revalidate rather than trust what it has; a second attempt after
// emptying the cache covers the case where the cache is not stale but damaged.
func installGlobally(runner *tasks.Runner, spec string, flags ...string) error {
	extra := ""
	if len(flags) > 0 {
		extra = " " + strings.Join(flags, " ")
	}

	install := "sudo npm install -g " + proc.Quote(spec) +
		" --no-audit --no-fund --prefer-online" + extra

	first := runner.Must(install, 25*time.Minute)
	if first == nil {
		return nil
	}

	runner.Write("npm refused that. Emptying its cache and trying once more, " +
		"which is what an index it wrote before the package was published needs.\n")

	if err := runner.Must("sudo npm cache clean --force", 5*time.Minute); err != nil {
		// Not fatal on its own: the install below is the thing being asked for.
		runner.Write("npm would not clean its cache; trying the install anyway.\n")
	}

	if err := runner.Must(install, 25*time.Minute); err != nil {
		return err
	}

	return nil
}

// writeAppEnv writes the systemd EnvironmentFile a managed proxied app is
// configured through.
//
// The unit already names `<root>/.env` as an optional EnvironmentFile, so this
// is configuration rather than another moving part — and it is the file the
// operator edits to change a setting without the panel in the way.
func writeAppEnv(site *store.Site, values [][2]string) error {
	var out strings.Builder

	out.WriteString("# Managed by Ubuntu Panel. Read by systemd as this site's service starts.\n")
	out.WriteString("# Edit and `systemctl restart " + site.ServiceName() + "` to apply.\n\n")

	for _, pair := range values {
		fmt.Fprintf(&out, "%s=%s\n", pair[0], pair[1])
	}

	path := site.RootPath + "/.env"

	if err := proc.PutFile(path, out.String(), true); err != nil {
		return err
	}

	// It holds the gateway token, so it is not world-readable the way the rest
	// of the site directory is.
	proc.Ok(fmt.Sprintf("sudo chown www-data:www-data %s && sudo chmod 640 %s",
		proc.Quote(path), proc.Quote(path)))

	return nil
}

// appSecret returns the site's generated admin password, creating and storing
// one the first time.
func (s *Service) appSecret(site *store.Site, length int) string {
	if site.WPAdminPassword != "" {
		return site.WPAdminPassword
	}

	password := crypto.Password(length)
	s.Store.SetSiteWPPassword(site.ID, password)

	return password
}

// startAndWait restarts a proxied site's service and waits for it to answer,
// and says what went wrong when it does not.
//
// The step this replaces printed "app not answering yet" and carried on, so a
// service that never came up was a successful deployment in the panel and a
// 502 in the browser — with the reason sitting in a journal nobody had been
// told to read. A unit that is not running is now a failed deployment with the
// log in the console.
func (s *Service) startAndWait(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		service := site.ServiceName()
		address := fmt.Sprintf("http://127.0.0.1:%d/", site.AppPort)

		if err := runner.Must("sudo systemctl daemon-reload && sudo systemctl restart "+service,
			2*time.Minute); err != nil {
			return "", err
		}

		// Plain curl rather than `curl -f`: an application that answers 401 or
		// 404 is an application that is up, which is all this is asking.
		answering := false

		for attempt := 0; attempt < 45; attempt++ {
			if proc.Ok("curl -s -o /dev/null --max-time 5 " + proc.Quote(address)) {
				answering = true
				break
			}

			time.Sleep(2 * time.Second)
		}

		if answering {
			code := proc.Run("curl -s -o /dev/null -w '%{http_code}' --max-time 10 "+
				proc.Quote(address), 30*time.Second).Output

			return fmt.Sprintf("%s answered on port %d with HTTP %s.",
				site.TypeLabel(), site.AppPort, code), nil
		}

		log := proc.Run("sudo journalctl -u "+service+" -n 40 --no-pager 2>&1", time.Minute).Output
		if log == "" {
			log = proc.Run("sudo tail -n 40 /var/log/"+service+".log 2>&1", time.Minute).Output
		}

		// Which ports the process actually opened, not only whether it opened
		// the one the panel expected: an application that ignored the port it
		// was given and took its own default is the difference between a
		// puzzling 502 and a one-line fix.
		listening := proc.Run(fmt.Sprintf(
			"pid=$(sudo systemctl show -p MainPID --value %s 2>/dev/null); "+
				"if [ -n \"$pid\" ] && [ \"$pid\" != 0 ]; then "+
				"sudo ss -ltnp 2>/dev/null | grep -E \"pid=$pid,|pid=$pid\\\"\" || "+
				"echo 'the process has no listening socket at all'; "+
				"else echo 'the service has no running process'; fi", service), time.Minute).Output

		// A dead unit is a failure. A running one that is not answering where
		// the panel put nginx is worth saying out loud, but the deployment did
		// its part — the application may simply be slow, or listening
		// somewhere else, and stopping here would undo a working vhost.
		if !proc.Ok("sudo systemctl is-active --quiet " + service) {
			return "", fmt.Errorf(
				"%s is not running, so nginx has nothing to proxy to and the site answers 502.\n"+
					"The last of its log:\n%s", service, log)
		}

		runner.Writef("%s is running but did not answer on port %d within 90 seconds.\n"+
			"Listening sockets for that port:\n%s\nThe last of its log:\n%s\n",
			service, site.AppPort, listening, log)

		return fmt.Sprintf("%s is running but has not answered on port %d yet.",
			site.TypeLabel(), site.AppPort), nil
	}
}

// checkThroughNginx asks the site for its own front page the way a visitor
// would, so a 502 turns up in the deployment console rather than in a browser
// an hour later.
func (s *Service) checkThroughNginx(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		// Asked of nginx on this machine, with the site's own Host header:
		// this has to work whether or not the domain resolves yet, and whether
		// or not a CDN sits in front of it.
		scheme, port, flags := "http", 80, ""
		if site.SSL {
			scheme, port, flags = "https", 443, " --insecure"
		}

		command := fmt.Sprintf(
			"curl -s -o /dev/null -w '%%{http_code}' --max-time 15%s --resolve %s %s",
			flags,
			proc.Quote(fmt.Sprintf("%s:%d:127.0.0.1", site.Domain, port)),
			proc.Quote(scheme+"://"+site.Domain+"/"))

		code := strings.TrimSpace(proc.Run(command, time.Minute).Output)

		switch {
		case code == "502" || code == "503" || code == "504":
			return "", fmt.Errorf(
				"nginx answered %s for %s, which means it could not reach the application "+
					"behind it. The step above has the application's log.", code, site.URL())

		case code == "" || code == "000":
			return "nginx could not be asked for the site from this machine; check it yourself.", nil

		default:
			return fmt.Sprintf("nginx answered HTTP %s for %s.", code, site.URL()), nil
		}
	}
}

// ---------------------------------------------------------------- Nextcloud

// installNextcloudPHP installs the extensions and the php.ini Nextcloud needs.
//
// Separate from ensurePHPRuntime, which only runs when the FPM socket is
// missing: a machine that already serves WordPress has the socket and none of
// these.
func (s *Service) installNextcloudPHP(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		php := site.PHPVersion

		names := make([]string, 0, len(nextcloudModules))
		for _, module := range nextcloudModules {
			names = append(names, "php"+php+"-"+module)
		}

		// imagick and apcu come from PECL packages that are not in every
		// mirror, and Nextcloud runs without them — a warning on its overview
		// page rather than a broken site. So the whole line is allowed to fail
		// and the required ones are checked afterwards.
		runner.Exec("sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "+
			strings.Join(names, " ")+" || true", 15*time.Minute)

		// The ones Nextcloud refuses to install without, checked by asking PHP
		// rather than by trusting apt: a package that installed into a
		// different PHP version is the usual way this goes wrong. The second
		// name is the package to install, which is not always the extension's.
		required := [][2]string{
			{"gd", "gd"}, {"curl", "curl"}, {"mbstring", "mbstring"},
			{"xml", "xml"}, {"zip", "zip"}, {"intl", "intl"},
			{"bcmath", "bcmath"}, {"pdo_mysql", "mysql"},
		}

		for _, pair := range required {
			if !proc.Ok("php" + php + " -m | grep -qix " + proc.Quote(pair[0])) {
				return "", fmt.Errorf(
					"PHP extension %s is missing and Nextcloud will not install without it. "+
						"Install php%s-%s on this machine, then redeploy.", pair[0], php, pair[1])
			}
		}

		for _, sapi := range []string{"fpm", "cli"} {
			path := fmt.Sprintf("/etc/php/%s/%s/conf.d/99-nextcloud.ini", php, sapi)

			if err := proc.PutFile(path, nextcloudPHP, true); err != nil {
				return "", err
			}
		}

		runner.Exec("sudo systemctl restart php"+php+"-fpm", 2*time.Minute)

		return "PHP prepared for Nextcloud.", nil
	}
}

// installNextcloud downloads the release and runs its own installer.
//
// Both halves skip themselves when they have already run, because a redeploy
// of a working Nextcloud must not download over the operator's files or try to
// install a second time on top of a database that is already set up.
func (s *Service) installNextcloud(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		if site.DatabaseID <= 0 {
			return "", errors.New("The application database was not created.")
		}

		database, err := s.Store.FindDatabase(ctx, site.DatabaseID)
		if err != nil {
			return "", errors.New("The application database was not created.")
		}

		root := site.DocumentRoot()
		data := site.RootPath + "/data"
		occ := fmt.Sprintf("cd %s && sudo -u www-data php%s occ", proc.Quote(root), site.PHPVersion)

		if !proc.Ok("sudo test -f " + proc.Quote(root+"/occ")) {
			// Unpacked into a staging directory first: the archive contains a
			// `nextcloud/` directory, and the document root is where its
			// contents go, not where that directory goes.
			staging := "/tmp/ubuntu-panel-nextcloud-" + strconv.FormatInt(site.ID, 10)

			if err := runner.Must(fmt.Sprintf(
				"command -v unzip >/dev/null || "+
					"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y unzip; "+
					"sudo rm -rf %[1]s && sudo mkdir -p %[1]s && "+
					"sudo curl -fsSL -o %[1]s/nextcloud.zip %[2]s && "+
					"sudo unzip -q %[1]s/nextcloud.zip -d %[1]s && "+
					"sudo mkdir -p %[3]s && "+
					"sudo cp -a %[1]s/nextcloud/. %[3]s/ && "+
					"sudo rm -rf %[1]s && "+
					"sudo chown -R www-data:www-data %[4]s",
				staging, NextcloudArchive, proc.Quote(root), proc.Quote(site.RootPath)),
				20*time.Minute); err != nil {
				return "", err
			}
		}

		// The data directory lives beside the document root rather than inside
		// it: anything under the root is one nginx misconfiguration away from
		// being downloadable, and this is everybody's files.
		if err := runner.Must(fmt.Sprintf(
			"sudo mkdir -p %[1]s && sudo chown -R www-data:www-data %[1]s && sudo chmod 750 %[1]s",
			proc.Quote(data)), 2*time.Minute); err != nil {
			return "", err
		}

		if proc.Ok("sudo test -f " + proc.Quote(root+"/config/config.php")) {
			return "Nextcloud is already installed here; left as it is.", nil
		}

		user := site.WPAdminUser
		if user == "" {
			user = "admin"
		}

		password := s.appSecret(site, 20)

		if err := runner.Must(fmt.Sprintf(
			"%s maintenance:install --database mysql --database-host 127.0.0.1 "+
				"--database-name %s --database-user %s --database-pass %s "+
				"--admin-user %s --admin-pass %s --data-dir %s",
			occ, proc.Quote(database.Name), proc.Quote(database.Username),
			proc.Quote(database.Password), proc.Quote(user), proc.Quote(password),
			proc.Quote(data)), 15*time.Minute); err != nil {
			return "", err
		}

		// Its background jobs are what keep sharing, previews and cleanup
		// working. Without this Nextcloud runs them during page loads, and
		// tells the operator so on its admin page.
		cron := fmt.Sprintf(
			"# Managed by Ubuntu Panel — Nextcloud background jobs for %s\n"+
				"*/5 * * * * www-data php%s -f %s/cron.php\n",
			site.Domain, site.PHPVersion, root)

		if err := proc.PutFile("/etc/cron.d/ubuntu-panel-nextcloud-"+
			strings.ReplaceAll(site.Domain, ".", "-"), cron, true); err != nil {
			return "", err
		}

		runner.Exec(occ+" background:cron", 2*time.Minute)

		return "Nextcloud installed. Admin user: " + user, nil
	}
}

// pointNextcloudAtURL tells Nextcloud which hostname and scheme it answers on.
//
// Nextcloud refuses any request whose Host header is not in trusted_domains —
// with a page that says so and nothing else — and it builds its own links from
// overwrite.cli.url. Both have to be set after the certificate step, because
// until then the panel does not know whether the site ended up on HTTPS.
func (s *Service) pointNextcloudAtURL(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		root := site.DocumentRoot()
		occ := fmt.Sprintf("cd %s && sudo -u www-data php%s occ", proc.Quote(root), site.PHPVersion)

		scheme := "http"
		if site.SSL {
			scheme = "https"
		}

		var commands []string

		for index, hostname := range site.Hostnames() {
			commands = append(commands, fmt.Sprintf("%s config:system:set trusted_domains %d --value=%s",
				occ, index, proc.Quote(hostname)))
		}

		commands = append(commands,
			fmt.Sprintf("%s config:system:set overwrite.cli.url --value=%s",
				occ, proc.Quote(site.URL())),
			fmt.Sprintf("%s config:system:set overwriteprotocol --value=%s", occ, scheme),
			// Behind nginx every request arrives from the loopback address, so
			// without this Nextcloud rate-limits and logs the wrong client.
			fmt.Sprintf("%s config:system:set trusted_proxies 0 --value=127.0.0.1", occ),
			fmt.Sprintf("%s config:system:set default_phone_region --value=US", occ),
		)

		// Joined with && rather than ;: a trusted_domains that did not take is
		// a Nextcloud that answers every request with "access through untrusted
		// domain", and a step separator that swallows the exit code would
		// report that as a successful deployment.
		if err := runner.Must(strings.Join(commands, " && "), 10*time.Minute); err != nil {
			return "", err
		}

		// APCu is a recommendation rather than a requirement, and it is only
		// installable on some mirrors — so a machine without it gets a
		// Nextcloud that runs, not a deployment that stops here.
		runner.Exec(fmt.Sprintf(
			"php%s -m | grep -qix apcu && %s config:system:set memcache.local --value='\\OC\\Memcache\\APCu'",
			site.PHPVersion, occ), 2*time.Minute)

		return "Nextcloud answers on " + site.URL(), nil
	}
}

// ---------------------------------------------------------------------- n8n

func (s *Service) installN8N(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		if err := installGlobally(runner, "n8n@latest"); err != nil {
			return "", err
		}

		if !proc.Ok("command -v n8n >/dev/null 2>&1") {
			return "", errors.New("npm reported success but there is no n8n on PATH afterwards")
		}

		// n8n keeps its database, encryption key and settings under
		// N8N_USER_FOLDER/.n8n. Inside the site directory, so a site backup is
		// a backup of the workflows too.
		if err := runner.Must(fmt.Sprintf(
			"sudo mkdir -p %[1]s/.n8n && sudo chown -R www-data:www-data %[1]s/.n8n && "+
				"sudo chmod 700 %[1]s/.n8n", proc.Quote(site.RootPath)), 2*time.Minute); err != nil {
			return "", err
		}

		if err := writeAppEnv(site, n8nEnvironment(site)); err != nil {
			return "", err
		}

		return "n8n installed. Its data lives in " + site.RootPath + "/.n8n", nil
	}
}

// n8nEnvironment is the whole of n8n's configuration.
func n8nEnvironment(site *store.Site) [][2]string {
	scheme := "http"
	if site.SSL {
		scheme = "https"
	}

	return [][2]string{
		{"N8N_HOST", site.Domain},
		{"N8N_PORT", strconv.Itoa(site.AppPort)},
		{"N8N_LISTEN_ADDRESS", "127.0.0.1"},
		{"N8N_PROTOCOL", scheme},
		{"N8N_EDITOR_BASE_URL", site.URL()},
		{"WEBHOOK_URL", site.URL() + "/"},
		{"N8N_USER_FOLDER", site.RootPath},
		// One proxy in front, so n8n reads the real client address out of the
		// X-Forwarded-For nginx sets rather than trusting all of it.
		{"N8N_PROXY_HOPS", "1"},
		{"N8N_RUNNERS_ENABLED", "true"},
		{"N8N_ENFORCE_SETTINGS_FILE_PERMISSIONS", "true"},
		{"N8N_DIAGNOSTICS_ENABLED", "false"},
		{"GENERIC_TIMEZONE", "UTC"},
		{"NODE_ENV", "production"},
		// The editor's session cookie is refused over plain HTTP unless this is
		// false, and the symptom is a login page that reloads itself for ever
		// on a site whose certificate has not been issued yet.
		{"N8N_SECURE_COOKIE", strconv.FormatBool(site.SSL)},
	}
}

// restartOnFinalURL rewrites a managed app's environment once the certificate
// step has decided whether the site is on HTTPS, and restarts it.
//
// n8n builds webhook URLs from what it is told here and refuses its own
// session cookie if it is told https while the site is still plain HTTP, so
// the value written before certbot ran is the wrong one about half the time.
func (s *Service) restartOnFinalURL(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		switch site.Type {
		case "n8n":
			err = writeAppEnv(site, n8nEnvironment(site))
		case "openclaw":
			err = writeAppEnv(site, openClawEnvironment(site, s.appSecret(site, 32)))
		default:
			return "Nothing to rewrite for this type.", nil
		}

		if err != nil {
			return "", err
		}

		// The unit itself may need rewriting too: its start command carries the
		// port, and nothing else would put a repaired one on disk for a site
		// deployed by an older panel.
		if _, err := s.createSystemdUnit(ctx, siteID)(runner); err != nil {
			return "", err
		}

		if _, err := s.startAndWait(ctx, siteID)(runner); err != nil {
			return "", err
		}

		return site.TypeLabel() + " restarted on " + site.URL(), nil
	}
}

// ----------------------------------------------------------------- OpenClaw

func (s *Service) installOpenClaw(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		// --allow-scripts: npm 12 and later refuse to run a package's install
		// scripts without it, and OpenClaw's are what put its binary together.
		// An npm too old to know the flag is not a reason to stop — it is also
		// too old to be blocking the scripts — so the plain form is the
		// fallback.
		if err := installGlobally(runner, "openclaw@latest", "--allow-scripts", "openclaw"); err != nil {
			if plain := installGlobally(runner, "openclaw@latest"); plain != nil {
				return "", plain
			}
		}

		if !proc.Ok("command -v openclaw >/dev/null 2>&1") {
			return "", errors.New("npm reported success but there is no openclaw on PATH afterwards")
		}

		// The gateway requires authentication and has no login of its own, so
		// the panel generates the shared secret and shows it on the site page.
		token := s.appSecret(site, 32)

		if err := runner.Must(fmt.Sprintf(
			"sudo mkdir -p %[1]s/.openclaw && sudo chown -R www-data:www-data %[1]s && "+
				"sudo chmod 700 %[1]s/.openclaw && "+
				"sudo mkdir -p /var/tmp/openclaw-compile-cache && "+
				"sudo chown www-data:www-data /var/tmp/openclaw-compile-cache",
			proc.Quote(site.RootPath)), 2*time.Minute); err != nil {
			return "", err
		}

		if err := writeAppEnv(site, openClawEnvironment(site, token)); err != nil {
			return "", err
		}

		// Non-fatal: it writes the default configuration and reports what is
		// missing, and a first run with no model provider configured is not a
		// failed deployment — that part is the operator's to fill in.
		runner.Exec(fmt.Sprintf(
			"sudo -u www-data HOME=%s openclaw doctor 2>&1 | tail -40 || true",
			proc.Quote(site.RootPath)), 5*time.Minute)

		return "OpenClaw installed. The gateway token is on this site's page.", nil
	}
}

// openClawEnvironment is the gateway's configuration.
func openClawEnvironment(site *store.Site, token string) [][2]string {
	return [][2]string{
		// Everything OpenClaw keeps — config, state, credentials — lives under
		// $HOME, so pointing HOME at the site directory keeps one site's agent
		// out of another's.
		{"HOME", site.RootPath},
		{"OPENCLAW_GATEWAY_PORT", strconv.Itoa(site.AppPort)},
		{"OPENCLAW_GATEWAY_TOKEN", token},
		// Loopback is its default and what the nginx proxy in front of it
		// needs; named rather than assumed, because the default changes inside
		// containers.
		{"OPENCLAW_GATEWAY_BIND", "loopback"},
		// systemd is already restarting this; a second supervisor inside the
		// process fights it.
		{"OPENCLAW_NO_RESPAWN", "1"},
		{"NODE_COMPILE_CACHE", "/var/tmp/openclaw-compile-cache"},
		{"NODE_ENV", "production"},
	}
}
