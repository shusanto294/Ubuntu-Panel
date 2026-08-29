package sites

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"
	"github.com/shusanto294/Ubuntu-Panel/internal/databases"
	"github.com/shusanto294/Ubuntu-Panel/internal/dns"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Service deploys and tears down sites.
type Service struct {
	Store     *store.Store
	Databases *databases.Service
}

// New wires it up.
func New(st *store.Store, db *databases.Service) *Service {
	return &Service{Store: st, Databases: db}
}

// WriteSharedConfig writes the pieces every vhost depends on: the
// `$panel_https` map, the ACME webroot, the self-signed pair the plain vhost's
// 443 block names, and the catch-all that refuses unknown hostnames.
func (s *Service) WriteSharedConfig(runner *tasks.Runner) error {
	if err := proc.PutFile("/etc/nginx/conf.d/panel-proxy.conf", helperConfig, true); err != nil {
		return err
	}

	command := fmt.Sprintf("sudo mkdir -p %s/.well-known/acme-challenge && sudo chown -R www-data:www-data %s",
		ACMEWebroot, ACMEWebroot)

	if runner != nil {
		if err := runner.Must(command, 2*time.Minute); err != nil {
			return err
		}
	} else if !proc.Ok(command) {
		return errors.New("could not create the ACME webroot")
	}

	// A self-signed pair for the 443 blocks that have no certificate of their
	// own yet, plus removing Debian's own default-server, which would otherwise
	// be a second one.
	command = defaultCertCommand + "; sudo rm -f /etc/nginx/sites-enabled/default"

	if runner != nil {
		runner.Exec(command, 2*time.Minute)
	} else {
		proc.Ok(command)
	}

	return proc.PutFile(catchAllPath, catchAll(portEightyClaimed()), true)
}

// defaultCertCommand writes the self-signed pair if it is not already there.
// Idempotent, and quiet about it.
var defaultCertCommand = fmt.Sprintf(
	"test -f %s.crt || sudo openssl req -x509 -nodes -newkey rsa:2048 -days 3650 "+
		"-keyout %s.key -out %s.crt -subj \"/CN=unconfigured.invalid\" 2>/dev/null",
	DefaultCert, DefaultCert, DefaultCert)

// EnsureDefaultCert makes sure the self-signed pair exists before something
// that names it is written.
//
// Every vhost that names a certificate it cannot open is a configuration nginx
// refuses outright — not for that site, for the whole machine. The catch-all
// names this pair, and so does a panel hostname that has no Let's Encrypt
// certificate yet, and the two are written from different places: whichever
// gets there first has to be able to count on the file.
func EnsureDefaultCert() { proc.Ok(defaultCertCommand) }

// catchAllPath is the panel's own default-server file. It is named to sort
// first in conf.d, so nginx reads it before anything else that might want the
// slot.
const catchAllPath = "/etc/nginx/conf.d/000-panel-default.conf"

// catchAll is the default-server configuration, minus whichever half something
// else already owns.
//
// nginx allows exactly one default_server per address, and a second is not a
// warning: it refuses to load the configuration at all — for every site on the
// box, and for the panel itself, which is served through the same nginx. The
// panel's own vhost claims port 80 so that the panel answers on the server's
// bare IP address, so the catch-all only claims what is free.
func catchAll(portEightyTaken bool) string {
	full := defaultServer()

	if !portEightyTaken {
		return full
	}

	// Keep only the 443 half.
	_, tls, found := strings.Cut(full, "server {\n    listen 443")
	if !found {
		return full
	}

	return "# Managed by Ubuntu Panel — refuses unknown hostnames on 443.\n" +
		"# Port 80's default server is the panel's own vhost, which is what makes\n" +
		"# the panel answer on this machine's IP address.\n" +
		"server {\n    listen 443" + tls
}

// portEightyClaimed reports whether another configuration file already holds
// the port 80 default-server slot.
//
// `grep -R` rather than `-r`: everything in sites-enabled is a symlink into
// sites-available, and `-r` skips symlinks it meets while recursing. It found
// nothing, the catch-all claimed a slot the panel's own vhost already had, and
// nginx refused the whole configuration with
//
//	a duplicate default server for 0.0.0.0:80 in /etc/nginx/sites-enabled/ubuntu-panel:3
//
// which failed the nginx install and would have failed every site deployment
// after it.
func portEightyClaimed() bool {
	return proc.Ok(
		"grep -RlE '^[[:space:]]*listen[[:space:]]+(\\[::\\]:)?80[^;]*default_server' " +
			"/etc/nginx/sites-enabled/ /etc/nginx/conf.d/ 2>/dev/null " +
			"| grep -v " + proc.Quote(catchAllPath) + " | grep -q .")
}

// http2Supported asks the nginx on this machine.
func http2Supported() bool {
	return SupportsHTTP2Directive(proc.Run("nginx -v 2>&1", 15*time.Second).Output)
}

// ------------------------------------------------------------------ steps

// ensurePHPRuntime makes sure the PHP-FPM socket this site's vhost names
// exists and is listening.
//
// A site can be created against any version the catalogue knows, and the
// machine only ever has the ones somebody installed. Installed here if it is
// missing, rather than discovered later as a 502 the panel has no opinion
// about.
func (s *Service) ensurePHPRuntime(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		socket := "/run/php/php" + site.PHPVersion + "-fpm.sock"
		test := "sudo test -S " + proc.Quote(socket)

		if proc.Ok(test) {
			return fmt.Sprintf("PHP %s is listening on %s.", site.PHPVersion, socket), nil
		}

		packages := []string{"fpm", "cli", "common", "mysql", "curl", "mbstring",
			"xml", "zip", "gd", "bcmath", "intl"}

		names := make([]string, 0, len(packages))
		for _, suffix := range packages {
			names = append(names, "php"+site.PHPVersion+"-"+suffix)
		}

		runner.Exec("sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "+strings.Join(names, " "),
			15*time.Minute)
		runner.Exec("sudo systemctl enable --now php"+site.PHPVersion+"-fpm", 2*time.Minute)

		if proc.Ok(test) {
			return fmt.Sprintf("PHP %s installed and listening on %s.", site.PHPVersion, socket), nil
		}

		present := proc.Run("ls /run/php/*.sock 2>/dev/null | tr '\\n' ' '", 15*time.Second).Output
		if present == "" {
			present = "none"
		}

		// Naming what *is* there is the whole point: "8.3 is missing" on a box
		// that only has 8.5 tells you exactly what to change.
		return "", fmt.Errorf(
			"PHP %s could not be installed, so nothing would serve this site — "+
				"nginx would answer every request with a 502.\n"+
				"PHP-FPM sockets on this machine: %s.\n"+
				"Pick one of those for the site, or install PHP %s from the Apps page.",
			site.PHPVersion, present, site.PHPVersion)
	}
}

// ensureDatabase creates the database record for a WordPress or Laravel site
// that has none, then provisions it.
func (s *Service) ensureDatabase(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		databaseID := site.DatabaseID

		if databaseID <= 0 {
			base := sanitiseIdentifier(site.Domain)
			if len(base) > 40 {
				base = base[:40]
			}

			suffix := strings.ToLower(crypto.Password(4))

			user := base
			if len(user) > 20 {
				user = user[:20]
			}

			databaseID, err = s.Store.CreateDatabase(ctx, &store.Database{
				UserID:        site.UserID,
				Engine:        "mysql",
				Name:          base + "_" + suffix,
				Username:      user + "_" + suffix,
				Password:      crypto.Password(24),
				Charset:       "utf8mb4",
				ManagedBySite: true,
			})
			if err != nil || databaseID <= 0 {
				return "", errors.New("Could not create the application database record.")
			}

			s.Store.SetSiteDatabase(site.ID, databaseID)
		}

		if err := s.Databases.Provision(ctx, databaseID, runner); err != nil {
			return "", err
		}

		return "Application database ready.", nil
	}
}

// sanitiseIdentifier turns a domain into something SQL will take as a name.
func sanitiseIdentifier(text string) string {
	var out strings.Builder

	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}

	return out.String()
}

func (s *Service) writeVhost(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		// Writes the self-signed pair the plain vhost's 443 block names, among
		// other things — so it has to come first, or nginx -t fails on a
		// certificate file that is not there yet.
		if err := s.WriteSharedConfig(runner); err != nil {
			return "", err
		}

		// The cache zone before the vhost that names it. nginx refuses a
		// configuration that refers to a zone nothing declared, and refuses it
		// for every site on the machine — a redeploy is not the place to find
		// that out.
		if err := s.writeCacheConfig(site); err != nil {
			return "", err
		}

		// No Let's Encrypt certificate yet — nginx will not start with an
		// ssl_certificate that does not exist, so the real one cannot be named
		// until certbot has run. The self-signed 443 block holds the port in
		// the meantime.
		available := AvailablePath(site)

		if err := proc.PutFile(available, RenderVhost(site, false, http2Supported()), true); err != nil {
			return "", err
		}

		link := fmt.Sprintf("sudo ln -sfn %s %s", proc.Quote(available), proc.Quote(EnabledPath(site)))

		if err := runner.Must(link, time.Minute); err != nil {
			return "", err
		}

		return "vhost written to " + available, nil
	}
}

func (s *Service) publishDNS(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		// The site page's answer for this name is now out of date, and the
		// operator syncing records by hand is the person about to look at it.
		system.ForgetResolution(site.Domain)

		output, err := dns.SyncForSite(ctx, s.Store, site)

		return dnsOutcome(site, output, err, system.PublicIP())
	}
}

// dnsOutcome decides what a DNS step that did not publish means for the rest of
// the deployment.
//
// A credential that holds no zone for this name is not a broken deployment: the
// domain lives somewhere the panel was never connected to, everything after
// this step builds a site that answers the moment the name points here, and
// stopping now would leave a machine with no vhost, no application and no
// certificate as well as no record — which is a worse NXDOMAIN than the one it
// was trying to avoid. So it is said plainly, in the record the operator has to
// add, and the recipe carries on.
//
// Anything else — an API that refused the write, a token that expired — is a
// real failure and stops the step, because those are the ones a retry fixes.
func dnsOutcome(site *store.Site, output string, err error, publicIP string) (string, error) {
	if !errors.Is(err, dns.ErrNoZone) {
		return output, err
	}

	content := site.RecordContent()
	if content == "" {
		content = publicIP
	}

	return fmt.Sprintf(
		"No connected DNS credential holds a zone for %s, so the panel published nothing. "+
			"Add this record wherever %s's DNS is run: %s  %s  ->  %s. Nothing needs "+
			"redeploying afterwards.",
		site.Domain, site.Domain, site.RecordType(), site.Domain, content), nil
}

// edgeTLS asks the site's DNS provider whether it is already serving the
// certificate.
//
// A provider that cannot be reached, or that answers something this panel does
// not recognise, is treated as not serving one: the cost of being wrong that
// way is a certificate nobody looks at, and the cost of being wrong the other
// way is a site with no TLS at all.
func (s *Service) edgeTLS(ctx context.Context, site *store.Site) dns.EdgeTLS {
	edge, err := dns.EdgeTLSForSite(ctx, s.Store, site)
	if err != nil {
		return dns.EdgeTLS{OriginNeedsCertificate: true}
	}

	return edge
}

// useEdgeTLS records that this site's certificate is the provider's, and says
// so in the console where the certificate step would have been.
func (s *Service) useEdgeTLS(ctx context.Context, siteID int64, mode string) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		// `ssl` stays false: it means a certificate on this machine, and there
		// is not one. The site's URL is https either way — EdgeTLS decides that
		// — and nginx keeps serving plain HTTP to the proxy, which is what the
		// proxy is asking for.
		s.Store.SetSiteTLSMode(site.ID, "edge")
		s.Store.SetSiteSSL(site.ID, false)

		return fmt.Sprintf("Cloudflare is set to %s for this zone, so it answers visitors with its "+
			"own certificate and fetches from this server over HTTP. No certificate is needed "+
			"here, and the deployment skips the two or three minutes of asking for one. Switch the "+
			"zone to Full and redeploy if you want the traffic between Cloudflare and this server "+
			"encrypted too.", mode), nil
	}
}

func (s *Service) requestCertificate(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		// The record was published a moment ago, and Let's Encrypt is about to
		// ask a resolver that has not heard about it. Waiting turns "certbot
		// did not issue a certificate" into a certificate a minute later.
		expected := system.PublicIP()
		resolution := system.WaitForHostname(ctx, site.Domain, expected, 2*time.Minute)

		switch {
		case len(resolution.Addresses) == 0:
			return "", fmt.Errorf(
				"%s does not resolve yet, so no certificate was requested — issuing against a "+
					"hostname nothing answers for fails every time. Point it at %s, then redeploy.",
				site.Domain, expected)

		case !resolution.Matches:
			runner.Writef("%s resolves to %s rather than %s. Trying anyway: a proxy in front "+
				"passes the challenge through, and a stale record does not.\n",
				site.Domain, strings.Join(resolution.Addresses, ", "), expected)
		}

		// Certbot is asked for every name in one request and refuses the whole
		// of it over any one of them. An alias nobody published a record for
		// would therefore cost the site the certificate its own domain had
		// earned — so an alias that answers nowhere is left out, named in the
		// log, and picked up by the next redeploy once its record exists. The
		// domain itself is never dropped: it resolved, or this returned above.
		var domains strings.Builder

		for _, hostname := range site.Hostnames() {
			// A shorter wait than the domain's: the record was published in
			// the same deployment, so it is either seconds young or absent.
			if hostname != site.Domain &&
				!system.WaitForHostname(ctx, hostname, expected, 30*time.Second).Resolves() {
				runner.Writef("%s does not resolve, so it is left out of this certificate. "+
					"Publish its record and redeploy to have it covered.\n", hostname)

				continue
			}

			domains.WriteString(" -d " + proc.Quote(hostname))
		}

		email := "admin@" + site.Domain

		if site.UserID > 0 {
			if owner, err := s.Store.FindUser(ctx, site.UserID); err == nil && owner.Email != "" {
				email = owner.Email
			}
		}

		// webroot rather than the nginx plugin: the plugin rewrites the vhost
		// the panel just wrote, and the next deployment would then overwrite
		// certbot's edits. The shared ACME webroot is served by every vhost on
		// port 80.
		command := fmt.Sprintf(
			"sudo certbot certonly --webroot -w %s%s "+
				"--non-interactive --agree-tos --email %s --keep-until-expiring --no-eff-email 2>&1",
			ACMEWebroot, domains.String(), proc.Quote(email))

		if code := runner.Exec(command, 10*time.Minute); code != 0 {
			return "", errors.New("certbot did not issue a certificate. The site stays on HTTP — " +
				"check that the domain resolves to this server, then redeploy.")
		}

		return "Certificate issued.", nil
	}
}

func (s *Service) enableHTTPS(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		if !proc.Ok("sudo test -f " + proc.Quote(CertificatePath(site)+"/fullchain.pem")) {
			s.Store.SetSiteSSL(site.ID, false)

			return "No certificate was issued, so the site stays on HTTP. " +
				"Check that the domain resolves to this server, then redeploy.", nil
		}

		http2 := http2Supported()
		available := AvailablePath(site)

		if err := proc.PutFile(available, RenderVhost(site, true, http2), true); err != nil {
			return "", err
		}

		// A file nginx will not load is worse than no HTTPS at all: the reload
		// is refused, so the running configuration freezes for *every* site on
		// the box, and the panel goes on saying the deployment succeeded. Put
		// the working HTTP vhost back and say what nginx objected to.
		if check := proc.Run("sudo nginx -t 2>&1", time.Minute); !check.OK() {
			proc.PutFile(available, RenderVhost(site, false, http2), true)
			proc.Ok("sudo systemctl reload nginx")
			s.Store.SetSiteSSL(site.ID, false)

			return "", fmt.Errorf("nginx rejected the HTTPS vhost, so the site was put back on HTTP:\n%s",
				check.Output)
		}

		if err := runner.Must("sudo systemctl reload nginx", time.Minute); err != nil {
			return "", err
		}

		s.Store.SetSiteSSL(site.ID, true)

		// A site that used to be served by its provider's certificate and now
		// has one of its own says so, or the pages would keep crediting
		// Cloudflare for a certificate certbot issued.
		s.Store.SetSiteTLSMode(site.ID, "letsencrypt")

		return "HTTPS enabled; HTTP now redirects to it.", nil
	}
}

// pointWordPressAtURL tells WordPress which scheme won: it stores absolute
// URLs, so it has to be told.
func (s *Service) pointWordPressAtURL(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", nil
		}

		root := proc.Quote(site.DocumentRoot())
		url := proc.Quote(site.URL())

		runner.Exec(fmt.Sprintf(
			"sudo -u www-data wp --path=%s option update home %s; "+
				"sudo -u www-data wp --path=%s option update siteurl %s",
			root, url, root, url), 2*time.Minute)

		return "WordPress home and siteurl set to " + site.URL(), nil
	}
}

// writePlaceholder writes a page for a site with no repository.
func (s *Service) writePlaceholder(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		root := site.DocumentRoot()

		var path, contents string

		if site.Type == "static" {
			path = root + "/index.html"
			contents = fmt.Sprintf(
				"<!doctype html>\n<meta charset=\"utf-8\">\n<title>%s</title>\n"+
					"<h1>%s is live</h1>\n"+
					"<p>Deployed by Ubuntu Panel. Upload your static files to replace this page.</p>\n",
				site.Domain, site.Domain)
		} else {
			path = root + "/index.php"
			contents = fmt.Sprintf(
				"<?php\n// Placeholder created by Ubuntu Panel for %s\n"+
					"echo '<!doctype html><meta charset=\"utf-8\"><title>%s</title>';\n"+
					"echo '<h1>%s is live</h1>';\n"+
					"echo '<p>Deployed by Ubuntu Panel. Replace this file with your application.</p>';\n",
				site.Domain, site.Domain, site.Domain)
		}

		if err := proc.PutFile(path, contents, true); err != nil {
			return "", err
		}

		proc.Ok("sudo chown -R www-data:www-data " + proc.Quote(site.RootPath))

		return "placeholder written to " + path, nil
	}
}

func (s *Service) writeLaravelEnv(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
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

		scheme := "http"
		if site.SSL {
			scheme = "https"
		}

		path := site.RootPath + "/.env"
		env := fmt.Sprintf(
			"APP_NAME=\"%s\"\n"+
				"APP_ENV=production\n"+
				"APP_KEY=\n"+
				"APP_DEBUG=false\n"+
				"APP_URL=%s://%s\n"+
				"\n"+
				"LOG_CHANNEL=stack\n"+
				"LOG_LEVEL=error\n"+
				"\n"+
				"DB_CONNECTION=mysql\n"+
				"DB_HOST=127.0.0.1\n"+
				"DB_PORT=3306\n"+
				"DB_DATABASE=%s\n"+
				"DB_USERNAME=%s\n"+
				"DB_PASSWORD=\"%s\"\n"+
				"\n"+
				"SESSION_DRIVER=database\n"+
				"QUEUE_CONNECTION=database\n"+
				"CACHE_STORE=database\n",
			site.Domain, scheme, site.Domain,
			database.Name, database.Username, database.Password)

		if err := proc.PutFile(path, env, true); err != nil {
			return "", err
		}

		proc.Ok(fmt.Sprintf("sudo chown www-data:www-data %s && sudo chmod 640 %s",
			proc.Quote(path), proc.Quote(path)))

		return ".env written with database credentials for " + database.Name, nil
	}
}

func (s *Service) installWordPress(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
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
		wp := "sudo -u www-data wp --path=" + proc.Quote(root)

		// A password the operator has not chosen is generated once and stored,
		// so the site page can show it.
		password := site.WPAdminPassword

		if password == "" {
			password = crypto.Password(20)
			s.Store.SetSiteWPPassword(site.ID, password)
		}

		user := site.WPAdminUser
		if user == "" {
			user = "admin"
		}

		title := site.WPTitle
		if title == "" {
			title = store.DefaultWPTitle
		}

		email := site.WPAdminEmail

		if email == "" && site.UserID > 0 {
			if owner, err := s.Store.FindUser(ctx, site.UserID); err == nil {
				email = owner.Email
			}
		}

		if email == "" {
			email = "admin@" + site.Domain
		}

		runner.Exec(fmt.Sprintf(
			"command -v wp >/dev/null || (curl -fsSL -o /tmp/wp-cli.phar "+
				"https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar && "+
				"sudo mv /tmp/wp-cli.phar /usr/local/bin/wp && sudo chmod +x /usr/local/bin/wp); "+
				"%s core is-installed 2>/dev/null || %s core download --force", wp, wp),
			10*time.Minute)

		if err := runner.Must(fmt.Sprintf(
			"%s config create --dbname=%s --dbuser=%s --dbpass=%s --dbhost=127.0.0.1 --skip-check --force && "+
				"%s config set WP_DEBUG false --raw && "+
				"%s config set DISALLOW_FILE_EDIT true --raw",
			wp, proc.Quote(database.Name), proc.Quote(database.Username), proc.Quote(database.Password),
			wp, wp), 5*time.Minute); err != nil {
			return "", err
		}

		scheme := "http"
		if site.SSL {
			scheme = "https"
		}

		if err := runner.Must(fmt.Sprintf(
			"%s core install --url=%s --title=%s --admin_user=%s "+
				"--admin_password=%s --admin_email=%s --skip-email; "+
				"%s rewrite structure \"/%%postname%%/\" --hard || true; "+
				"%s plugin delete hello akismet 2>/dev/null || true",
			wp, proc.Quote(scheme+"://"+site.Domain), proc.Quote(title), proc.Quote(user),
			proc.Quote(password), proc.Quote(email), wp, wp), 10*time.Minute); err != nil {
			return "", err
		}

		runner.Exec(fmt.Sprintf(
			"sudo chown -R www-data:www-data %s && "+
				"sudo find %s -type d -exec chmod 755 {} \\; && "+
				"sudo find %s -type f -exec chmod 644 {} \\;",
			proc.Quote(site.RootPath), proc.Quote(root), proc.Quote(root)), 5*time.Minute)

		return "WordPress installed.", nil
	}
}

func (s *Service) scaffoldNodeApp(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		packageJSON := "{\n" +
			"  \"name\": \"ubuntu-panel-app\",\n" +
			"  \"private\": true,\n" +
			"  \"version\": \"1.0.0\",\n" +
			"  \"scripts\": {\n" +
			"    \"start\": \"node server.js\"\n" +
			"  }\n" +
			"}\n"

		serverJS := fmt.Sprintf(
			"const http = require('http');\n"+
				"const port = process.env.PORT || 3000;\n"+
				"\n"+
				"http\n"+
				"  .createServer((req, res) => {\n"+
				"    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });\n"+
				"    res.end('<h1>%s is live</h1><p>Deployed by Ubuntu Panel. "+
				"Point this site at your repository to replace this starter.</p>');\n"+
				"  })\n"+
				"  .listen(port, '127.0.0.1', () => console.log('listening on ' + port));\n",
			site.Domain)

		if err := proc.PutFile(site.RootPath+"/package.json", packageJSON, true); err != nil {
			return "", err
		}

		if err := proc.PutFile(site.RootPath+"/server.js", serverJS, true); err != nil {
			return "", err
		}

		proc.Ok("sudo chown -R www-data:www-data " + proc.Quote(site.RootPath))

		return "Starter Node app scaffolded. Replace it with your repository when ready.", nil
	}
}

func (s *Service) createSystemdUnit(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		if err := proc.PutFile(UnitPath(site), RenderUnit(site), true); err != nil {
			return "", err
		}

		service := site.ServiceName()

		if err := runner.Must("sudo systemctl daemon-reload && sudo systemctl enable "+service,
			time.Minute); err != nil {
			return "", err
		}

		return "systemd unit " + service + " installed", nil
	}
}
