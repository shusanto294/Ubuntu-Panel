package sites

// The applications the panel installs itself, rather than deploying whatever a
// repository contains: Nextcloud, n8n and OpenClaw. WordPress is the same idea
// and lives in service.go, where it was before these joined it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
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

		// Both logs, always. The unit sends the application's own output to a
		// file of its own, so the journal holds systemd's account — started,
		// exited, restarting — and not one word of what the application said
		// about why. The first failure of an OpenClaw site was diagnosed from
		// an exit code because of this.
		log := proc.Run("sudo journalctl -u "+service+" -n 25 --no-pager 2>&1", time.Minute).Output

		if own := proc.Run("sudo tail -n 40 "+proc.Quote("/var/log/"+service+".log")+" 2>&1",
			time.Minute).Output; own != "" {
			log += "\n\nWhat " + site.TypeLabel() + " itself wrote:\n" + own
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

		// An application that took a port of its own is followed rather than
		// argued with. The vhost has not been written yet at this point in the
		// deployment, so recording the real port here is all it takes for
		// nginx to be pointed at it.
		if port := answeringPort(listening); port > 0 && port != site.AppPort {
			runner.Writef("%s is listening on port %d rather than the %d it was given. "+
				"Pointing nginx at %d.\n", site.TypeLabel(), port, site.AppPort, port)

			s.Store.SetSiteAppPort(site.ID, port)

			return fmt.Sprintf("%s answered on port %d, which is where nginx will be pointed.",
				site.TypeLabel(), port), nil
		}

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

// answeringPort reads a listening TCP port out of `ss -ltnp` output and
// reports it when the process opened exactly one, so there is no guessing
// about which of several the proxy should be pointed at.
//
// Lines look like:
//
//	LISTEN 0 511 127.0.0.1:18789 0.0.0.0:* users:(("node",pid=1234,fd=21))
//	LISTEN 0 511    [::1]:18789     [::]:* users:(("node",pid=1234,fd=22))
//
// The same port on both address families is one port, not two.
func answeringPort(listening string) int {
	found := map[int]bool{}

	for _, line := range strings.Split(listening, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// The local address is the fourth column, and the port is whatever
		// follows its last colon — which is the only part of an IPv6 address
		// that can be read without parsing it.
		address := fields[3]

		index := strings.LastIndex(address, ":")
		if index < 0 {
			continue
		}

		port, err := strconv.Atoi(address[index+1:])
		if err != nil || port <= 0 || port > 65535 {
			continue
		}

		found[port] = true
	}

	if len(found) != 1 {
		return 0
	}

	for port := range found {
		return port
	}

	return 0
}

// checkDomainResolves asks whether a visitor can get here at all.
//
// Every other check in the deployment is asked of this machine — the app on
// its port, nginx with the site's own Host header and `--resolve`. All of them
// pass on a site whose name resolves to nothing, because none of them is
// looking at DNS. So the deployment goes green, the panel says the site is
// live, and the browser says DNS_PROBE_FINISHED_NXDOMAIN, which reads as the
// panel having built a broken site.
//
// Optional in the recipe: the machine's part is genuinely finished, and a name
// that has not been pointed here yet, or was pointed here a minute ago, is not
// a reason to throw away a working deployment. It is a reason to say so.
func (s *Service) checkDomainResolves(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		expected := system.PublicIP()

		// The record may have been published by the first step of this same
		// deployment, minutes ago, so this waits rather than asking once. Its
		// own answer is what the site page will show next, so the cached one
		// goes.
		system.ForgetResolution(site.Domain)

		resolution := system.WaitForHostname(ctx, site.Domain, expected, 90*time.Second)

		switch {
		case !resolution.Resolves():
			hint := fmt.Sprintf("Point an A record for %s at %s", site.Domain, expected)

			if site.ManageDNS {
				hint = fmt.Sprintf(
					"The panel published the record for %s, so either it has not spread yet or the "+
						"zone at the provider is not the one serving this name. Check it, then Sync DNS",
					site.Domain)
			}

			return "", fmt.Errorf(
				"%s does not resolve to anything, so a browser cannot reach this site no matter "+
					"what nginx does — it shows DNS_PROBE_FINISHED_NXDOMAIN. Everything on this "+
					"machine is built and answering. %s.",
				site.Domain, hint)

		case !resolution.Matches:
			// Cloudflare's proxy, another CDN, or a stale record: the first two
			// work, the third does not, and they look identical from here.
			runner.Writef("%s resolves to %s rather than to %s, this machine. That is what a CDN "+
				"or proxy in front looks like and is fine; if there is none, the record is "+
				"pointing somewhere else.\n",
				site.Domain, strings.Join(resolution.Addresses, ", "), expected)

			return fmt.Sprintf("%s resolves to %s, not to this machine.",
				site.Domain, strings.Join(resolution.Addresses, ", ")), nil

		default:
			return fmt.Sprintf("%s resolves to %s, this machine.", site.Domain, expected), nil
		}
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

		// A proxied application may still be finishing its first boot while
		// nginx is already answering for it. Given another minute before the
		// deployment calls it broken, because "it needed longer" and "it is
		// never coming up" look identical for the first few seconds.
		for attempt := 0; site.IsProxied() && attempt < 20 &&
			(code == "502" || code == "503" || code == "504"); attempt++ {
			time.Sleep(3 * time.Second)

			code = strings.TrimSpace(proc.Run(command, time.Minute).Output)
		}

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
			if err = writeAppEnv(site, openClawEnvironment(site, s.appSecret(site, 32))); err == nil {
				// The allowed origins carry the scheme, and whether the site
				// ended up on HTTPS was only decided by the step above. A
				// gateway still allowing the http:// origin refuses the
				// dashboard on the https:// page it is now served from.
				err = s.configureOpenClaw(site, runner)
			}

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

		// The gateway's own token, which is what its dashboard asks for and
		// what a phone's setup code carries. Generated once and stored, so the
		// site page can show it; nothing else on this machine keeps a copy.
		s.appSecret(site, 32)

		// An older panel put an nginx password in front of this site and
		// authenticated on the gateway's behalf. Nothing reads this now.
		proc.Ok("sudo rm -f " + proc.Quote(PasswordFilePath(site)))

		if err := runner.Must(fmt.Sprintf(
			"sudo mkdir -p %[1]s/.openclaw && sudo chown -R www-data:www-data %[1]s && "+
				"sudo chmod 700 %[1]s/.openclaw && "+
				"sudo mkdir -p /var/tmp/openclaw-compile-cache && "+
				"sudo chown www-data:www-data /var/tmp/openclaw-compile-cache",
			proc.Quote(site.RootPath)), 2*time.Minute); err != nil {
			return "", err
		}

		if err := writeAppEnv(site, openClawEnvironment(site, s.appSecret(site, 32))); err != nil {
			return "", err
		}

		if err := s.configureOpenClaw(site, runner); err != nil {
			return "", err
		}

		// Non-fatal: it reports what is missing, and a first run with no model
		// provider configured is not a failed deployment — that part is the
		// operator's to fill in.
		runner.Exec(fmt.Sprintf(
			"sudo -u www-data HOME=%s openclaw doctor 2>&1 | tail -40 || true",
			proc.Quote(site.RootPath)), 5*time.Minute)

		return "OpenClaw installed. The gateway token is on this site's page.", nil
	}
}

// configureOpenClaw writes the configuration the gateway refuses to start
// without.
//
// A gateway whose config does not say what mode it is in exits 78 — EX_CONFIG,
// "Gateway start blocked: set gateway.mode=local" — about ten seconds after
// every start, for ever, which systemd faithfully repeats. The interactive
// `openclaw onboard` is what normally writes this, and there is no operator
// sitting in front of a deployment to answer it.
//
// Written through OpenClaw's own CLI so it lands in whatever shape this
// version expects, with a hand-written config as the fallback for a version
// whose CLI cannot do it. `local` is the mode for a gateway that runs its own
// agent on this machine, which is what a site on this panel is.
func (s *Service) configureOpenClaw(site *store.Site, runner *tasks.Runner) error {
	command := fmt.Sprintf("sudo -u www-data HOME=%s openclaw", proc.Quote(site.RootPath))
	token := s.appSecret(site, 32)

	// The dashboard is served from the site's own hostname, and the gateway
	// refuses a Control UI connection from an origin it was not told about —
	// "Browser origin not allowed", with the login form still on screen. It
	// ships knowing about localhost and nothing else, so the site's own origin
	// has to be added or the dashboard can never be used from the domain it is
	// published on.
	origins := openClawOrigins(site)

	runner.Exec(fmt.Sprintf(
		"%[1]s config set gateway.mode local; "+
			"%[1]s config set gateway.bind loopback; "+
			"%[1]s config set gateway.port %[2]d; "+
			"%[1]s config set gateway.controlUi.allowedOrigins %[3]s || "+
			"%[1]s config set gateway.controlUi.allowedOrigins %[4]s",
		command, site.AppPort,
		proc.Quote(originsJSON(origins)), proc.Quote(origins[0])), 5*time.Minute)

	// The gateway authenticates for itself now, with the token on this site's
	// page. The panel sets that token and nothing else about how the gateway
	// lets anybody in — it is the application's own business, and the
	// arrangement that replaced it broke the application's own mobile pairing.
	//
	// trustedProxies is not authentication: it is how the gateway knows whose
	// X-Forwarded-For to believe, so a visitor is logged and rate limited as
	// themselves rather than as nginx.
	runner.Exec(fmt.Sprintf(
		"%[1]s config set gateway.auth.token %[2]s; "+
			"%[1]s config set gateway.trustedProxies '[\"127.0.0.1\",\"::1\"]' || "+
			"%[1]s config set gateway.trustedProxies 127.0.0.1",
		command, proc.Quote(token)), 5*time.Minute)

	// Then written properly, whatever the CLI made of it.
	//
	// `config set` is handed a list on the command line for the origins and the
	// trusted proxies, and a version that stores those as a string produces a
	// configuration the gateway rejects. This writes the keys as JSON, puts the
	// token where the gateway reads it, takes out the keys an older panel wrote
	// to authenticate on the gateway's behalf, and leaves the rest of the file
	// alone.
	return mergeOpenClawConfig(site, runner, origins, token)
}

// openClawOrigins are the browser origins the dashboard is served from: the
// site itself and its aliases, on the scheme the site ended up on.
//
// Full origins, never wildcards — the gateway compares them literally, and a
// pattern is simply an origin nothing matches.
func openClawOrigins(site *store.Site) []string {
	scheme := "http://"
	if site.SSL {
		scheme = "https://"
	}

	origins := make([]string, 0, len(site.Hostnames()))
	for _, hostname := range site.Hostnames() {
		origins = append(origins, scheme+hostname)
	}

	if len(origins) == 0 {
		origins = append(origins, scheme+site.Domain)
	}

	return origins
}

// originsJSON is the list as OpenClaw's config wants it.
func originsJSON(origins []string) string {
	encoded, err := json.Marshal(origins)
	if err != nil {
		return "[]"
	}

	return string(encoded)
}

// mergeOpenClawConfig writes the keys the gateway needs into its configuration
// file without touching anything else in it.
//
// A rewrite would take the operator's providers, channels and pairings with
// it, so the file is read, one key is set, and it is written back — by Python,
// which is on every Ubuntu the panel supports and can be trusted with JSON in
// a way that a shell one-liner cannot.
func mergeOpenClawConfig(site *store.Site, runner *tasks.Runner, origins []string,
	token string) error {
	command := fmt.Sprintf(
		"sudo mkdir -p %[1]s && sudo chown www-data:www-data %[1]s && "+
			"CONFIG=%[2]s ORIGINS=%[3]s PORT=%[4]d TOKEN=%[5]s "+
			"sudo -u www-data --preserve-env=CONFIG,ORIGINS,PORT,TOKEN python3 - <<'PYTHON'\n%[6]s\nPYTHON",
		proc.Quote(site.RootPath+"/.openclaw"),
		proc.Quote(site.RootPath+"/.openclaw/openclaw.json"),
		proc.Quote(originsJSON(origins)), site.AppPort, proc.Quote(token),
		openClawConfigScript)

	if err := runner.Must(command, 2*time.Minute); err != nil {
		return fmt.Errorf("could not write OpenClaw's gateway configuration: %w", err)
	}

	return nil
}

// openClawConfigScript is that merge, as its own constant so the test can run
// it and read what it wrote.
//
// Every key here is one the gateway validates strictly — its schema rejects
// anything it does not recognise — and getting one wrong is a gateway that
// either refuses to start or quietly asks the operator for a token instead.
// Guessing at them is what put the token prompt there in the first place.
const openClawConfigScript = `
import json, os, sys

path = os.environ["CONFIG"]
origins = json.loads(os.environ["ORIGINS"])
token = os.environ["TOKEN"]

try:
    with open(path) as handle:
        config = json.load(handle)
except FileNotFoundError:
    config = {}
except ValueError as error:
    sys.exit("the existing configuration is not valid JSON: %s" % error)

gateway = config.setdefault("gateway", {})

# What running behind this panel actually decides: the gateway runs its own
# agent on this machine, listens on loopback because nginx is in front of it,
# on the port the panel assigned. Everything else is OpenClaw's own default.
gateway["mode"] = "local"
gateway["bind"] = "loopback"
gateway["port"] = int(os.environ["PORT"])

# Not authentication — which address the gateway believes X-Forwarded-For
# from, so a visitor is logged and rate limited as themselves and not as
# nginx.
gateway["trustedProxies"] = ["127.0.0.1", "::1"]

auth = gateway.setdefault("auth", {})

# The gateway's own token, which is what its dashboard asks for and what a
# phone's setup code carries. An operator who sets their own is not overruled.
auth.setdefault("token", token)

# What an older panel wrote to authenticate on the gateway's behalf: nginx
# asked for a password and named the visitor in a header, and the gateway was
# told to believe it. It worked, and it was the panel answering a question the
# application answers for itself — with the mobile pairing broken as the
# price, because a gateway in trusted-proxy mode may not also hold the token a
# setup code needs. Removed rather than left: trusted-proxy mode alongside a
# token is refused at startup, so a site deployed by that panel would not come
# back up.
if auth.get("mode") == "trusted-proxy":
    auth.pop("mode", None)

auth.pop("trustedProxy", None)

control = gateway.setdefault("controlUi", {})

# The break-glass switch that same arrangement needed, because a Control UI
# session with no paired device was admitted and then had its scopes cleared.
# The gateway does its own device pairing again, over HTTPS, which is the
# secure context its WebCrypto needs.
control.pop("dangerouslyDisableDeviceAuth", None)

# The gateway refuses a Control UI connection from an origin it was not told
# about — "Browser origin not allowed", with the login form still on screen.
# It ships knowing about localhost and nothing else, so the site's own origin
# has to be added or the dashboard can never be used from the domain it is
# published on.
existing = control.get("allowedOrigins") or []
if not isinstance(existing, list):
    existing = []

for origin in origins:
    if origin not in existing:
        existing.append(origin)

control["allowedOrigins"] = existing

with open(path, "w") as handle:
    json.dump(config, handle, indent=2)
    handle.write("\n")

print("gateway: local on loopback, its own token auth, origins %s" % ", ".join(existing))
`

// openClawEnvironment is the gateway's configuration.
func openClawEnvironment(site *store.Site, token string) [][2]string {
	return [][2]string{
		// Everything OpenClaw keeps — config, state, credentials — lives under
		// $HOME, so pointing HOME at the site directory keeps one site's agent
		// out of another's.
		{"HOME", site.RootPath},
		{"OPENCLAW_GATEWAY_PORT", strconv.Itoa(site.AppPort)},
		// The gateway's own token, which is the whole of what stands in front
		// of it. Set here as well as in the configuration file because either
		// is enough on its own, and a config file an operator edits by hand is
		// the one more likely to lose it.
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

// ---------------------------------------------- the OpenClaw dashboard link

// dashboardURLPattern is the first URL in whatever `openclaw dashboard`
// prints. The command's prose changes between versions; a URL in its output
// does not.
var dashboardURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// ansiPattern is the colour a command writes for a terminal.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// localHostPattern is any way the gateway names this machine to itself, with
// the port that follows it — percent-encoded too, because the address inside a
// query parameter is escaped and `agent.example.com%3A18801` is a host that
// answers for nothing.
var localHostPattern = regexp.MustCompile(
	`(?i)(?:127\.0\.0\.1|\[::1\]|::1|localhost)(?::\d+|%3A\d+)?`)

// OpenClawDashboardURL turns the link the gateway prints for itself into one
// that works from a browser somewhere else.
//
// `openclaw dashboard` mints a tokenized URL and points it at the address the
// gateway is bound to, which here is loopback — right for somebody sitting at
// the machine's terminal and useless in a browser anywhere else. The token,
// the path and the parameters are the parts that matter, so the host is
// swapped for the site's own and everything else is left exactly as it was.
//
// The parameters carry a WebSocket address of their own, pointing at the same
// loopback port, so the substitution runs over the whole string rather than
// over the host alone — and ws:// becomes wss:// on a site served over HTTPS,
// which a browser on an https page will not open otherwise.
func OpenClawDashboardURL(site *store.Site, output string) string {
	// A terminal command writes for a terminal: the link comes wrapped in
	// colour codes, and a token with an escape sequence in the middle of it is
	// a token the gateway does not recognise.
	found := dashboardURLPattern.FindString(ansiPattern.ReplaceAllString(output, ""))
	if found == "" {
		return ""
	}

	scheme := "http"
	if site.SSL {
		scheme = "https"
	}

	// The host, wherever it appears: the URL's own, and any inside a parameter.
	rewritten := localHostPattern.ReplaceAllString(found, site.Domain)

	if site.SSL {
		rewritten = strings.ReplaceAll(rewritten, "ws://"+site.Domain, "wss://"+site.Domain)
		rewritten = strings.ReplaceAll(rewritten, "ws%3A%2F%2F"+site.Domain, "wss%3A%2F%2F"+site.Domain)
	}

	// http:// on a site that is served over https is a link browsers upgrade,
	// downgrade or refuse depending on where it appears. Named rather than
	// left to them.
	if strings.HasPrefix(rewritten, "http://") {
		rewritten = scheme + "://" + strings.TrimPrefix(rewritten, "http://")
	}

	return rewritten
}

// OpenClawDashboardCommand asks the gateway for that link, as the user it runs
// as and with the HOME its configuration lives under — the two things that
// decide which site's gateway answers.
//
// `--no-open` is the whole of it working. Without the flag the command's job
// is to open a browser, and on a server there is no browser to open: it prints
// its prose, tries, and the link either never appears on stdout or appears
// after something that does not return. The dashboard's own error box says as
// much — "reopen the dashboard with openclaw dashboard --no-open to recopy the
// current URL and auth details".
//
// The plain form is still run if the flag is not understood, for a version
// that predates it.
func OpenClawDashboardCommand(site *store.Site) string {
	home := proc.Quote(site.RootPath)

	return fmt.Sprintf(
		"sudo -u www-data HOME=%[1]s openclaw dashboard --no-open 2>&1 || "+
			"sudo -u www-data HOME=%[1]s openclaw dashboard 2>&1", home)
}
