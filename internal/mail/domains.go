package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/dns"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
	"github.com/shusanto294/ubuntu-panel-go/internal/system"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// generateDKIM makes the key pair and reads the public half back for DNS.
func (s *Service) generateDKIM(ctx context.Context, domainID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		domain, err := s.Store.FindEmailDomain(ctx, domainID)
		if err != nil {
			return "", errors.New("mail domain vanished")
		}

		selector := domain.DKIMSelector
		if selector == "" {
			selector = "mail"
		}

		keyDir := "/etc/opendkim/keys/" + domain.Domain
		publicPath := fmt.Sprintf("%s/%s.txt", keyDir, selector)

		command := fmt.Sprintf(
			"sudo mkdir -p '%[1]s' && "+
				"sudo opendkim-genkey -b 2048 -d '%[2]s' -D '%[1]s' -s '%[3]s' -v && "+
				"sudo chown -R opendkim:opendkim '%[1]s' && sudo chmod 600 '%[1]s/%[3]s.private' && "+
				"grep -q '%[3]s._domainkey.%[2]s' /etc/opendkim/KeyTable || "+
				"echo '%[3]s._domainkey.%[2]s %[2]s:%[3]s:%[1]s/%[3]s.private' "+
				"| sudo tee -a /etc/opendkim/KeyTable > /dev/null; "+
				"grep -q '^\\*@%[2]s' /etc/opendkim/SigningTable || "+
				"echo '*@%[2]s %[3]s._domainkey.%[2]s' | sudo tee -a /etc/opendkim/SigningTable > /dev/null; "+
				"sudo systemctl restart opendkim",
			keyDir, domain.Domain, selector)

		if err := runner.Must(command, 3*time.Minute); err != nil {
			return "", err
		}

		// opendkim-genkey writes the public key as a BIND TXT record split
		// across quoted chunks. The DNS drivers want the bare base64, so the
		// quotes and the surrounding record syntax are stripped here rather
		// than in each of six providers.
		contents, err := proc.ReadFile(publicPath, true)
		if err != nil {
			return "DKIM key generated, but its public half could not be read back yet.", nil
		}

		key := extractDKIMKey(contents)
		if key == "" {
			return "DKIM key generated, but its public half could not be parsed.", nil
		}

		s.Store.SetEmailDomainDKIM(domain.ID, key)

		return fmt.Sprintf("DKIM key generated for %s (selector %s).", domain.Domain, selector), nil
	}
}

// extractDKIMKey pulls the base64 out of `... "v=DKIM1; k=rsa; p=MIIB..." ...`.
func extractDKIMKey(contents string) string {
	_, rest, found := strings.Cut(contents, "p=")
	if !found {
		return ""
	}

	var key strings.Builder

	for _, r := range rest {
		switch r {
		case '"', ' ', '\t', '\n', '\r':
			continue
		case ')', ';':
			return key.String()
		}

		key.WriteRune(r)
	}

	return key.String()
}

// registerDomain adds the row Postfix reads to decide it is responsible for a
// domain.
func (s *Service) registerDomain(ctx context.Context, domainID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		domain, err := s.Store.FindEmailDomain(ctx, domainID)
		if err != nil {
			return "", errors.New("mail domain vanished")
		}

		script := fmt.Sprintf("INSERT IGNORE INTO %s.virtual_domains (name) VALUES (%s);\n",
			mailDB, sqlQuote(domain.Domain))

		if err := runSQL(script); err != nil {
			return "", err
		}

		return domain.Domain + " registered with Postfix.", nil
	}
}

// PublishDNS writes SPF, DKIM, DMARC and MX for a domain that has DNS
// management enabled.
func (s *Service) PublishDNS(ctx context.Context, domainID int64) (string, error) {
	domain, err := s.Store.FindEmailDomain(ctx, domainID)
	if err != nil {
		return "", errors.New("That mail domain no longer exists.")
	}

	return dns.SyncForMail(ctx, s.Store, domain, system.PublicIP())
}

// CreateDomain provisions a domain: tables, Postfix row, DKIM key, DNS and
// webmail.
func (s *Service) CreateDomain(ctx context.Context, domainID int64) error {
	domain, err := s.Store.FindEmailDomain(ctx, domainID)
	if err != nil {
		return err
	}

	if !s.IsConfigured() {
		message := "The mail server is not installed yet — install it from the Apps page."
		s.Store.SetEmailDomainStatus(domainID, "failed", message)

		return errors.New(message)
	}

	taskID, err := s.Store.CreateTask(ctx, "mail", "mail.domain.create", domain.UserID, 0, domain.Domain)
	if err != nil {
		return err
	}

	list := &tasks.List{}

	// Before anything writes to them. Cheap, idempotent, and the difference
	// between a first-time setup and a repair is nothing at all.
	list.AddFn("Check the mail tables", func(runner *tasks.Runner) (string, error) {
		if err := s.EnsureSchema(); err != nil {
			return "", err
		}

		return "Mail tables are in place.", nil
	})

	list.AddFn("Register the domain", s.registerDomain(ctx, domainID))
	list.AddFn("Generate the DKIM key", s.generateDKIM(ctx, domainID))

	if domain.ManageDNS {
		list.AddFn("Publish the mail DNS records", func(runner *tasks.Runner) (string, error) {
			return s.PublishDNS(ctx, domainID)
		}).Optional()
	}

	// Optional, both of them: a mail domain whose webmail or certificate could
	// not be set up is still a working mail domain, reachable by any IMAP
	// client.
	list.AddFn("Serve mail."+domain.Domain, s.serveMailHost(ctx, domainID)).Optional()
	list.AddFn("Get a certificate for mail."+domain.Domain, s.secureMailHost(ctx, domainID)).Optional()

	if tasks.New(s.Store, taskID).Run(list) {
		s.Store.SetEmailDomainStatus(domainID, "active", "")

		return nil
	}

	message := "Setup failed."

	if task, err := s.Store.FindTask(ctx, taskID); err == nil && task.Message != "" {
		message = task.Message
	}

	s.Store.SetEmailDomainStatus(domainID, "failed", message)

	return errors.New(message)
}

// DeleteDomain removes the domain, its mailboxes, its DKIM key and its stored
// mail.
func (s *Service) DeleteDomain(ctx context.Context, domainID int64) error {
	domain, err := s.Store.FindEmailDomain(ctx, domainID)
	if err != nil {
		return err
	}

	taskID, err := s.Store.CreateTask(ctx, "mail", "mail.domain.delete", domain.UserID, 0, domain.Domain)
	if err != nil {
		return err
	}

	// Cascades to virtual_users, which is what removes every mailbox.
	runSQL(fmt.Sprintf("DELETE FROM %s.virtual_domains WHERE name = %s;\n",
		mailDB, sqlQuote(domain.Domain)))

	list := &tasks.List{}

	list.Add("Remove the DKIM key and mail store",
		fmt.Sprintf("sudo rm -rf '/etc/opendkim/keys/%[1]s' '%[2]s/%[1]s'; "+
			"sudo sed -i '/%[1]s/d' /etc/opendkim/KeyTable /etc/opendkim/SigningTable; "+
			"sudo systemctl restart opendkim || true",
			domain.Domain, vmailRoot)).Optional()

	list.Add("Remove the webmail vhost",
		fmt.Sprintf("sudo rm -f '/etc/nginx/sites-enabled/webmail-%[1]s' "+
			"'/etc/nginx/sites-available/webmail-%[1]s'; "+
			"sudo nginx -t && sudo systemctl reload nginx", domain.Domain)).Optional()

	// Not gated on manage_dns: a domain whose DNS management was switched off
	// after setup still has the records the panel published, and leaving an MX
	// behind means mail for a domain this server no longer accepts keeps
	// arriving at it.
	if domain.DNSAccountID > 0 {
		list.AddFn("Remove the mail DNS records", func(runner *tasks.Runner) (string, error) {
			return dns.RemoveForMail(ctx, s.Store, domain)
		}).Optional()
	}

	if !tasks.New(s.Store, taskID).Run(list) {
		s.Store.SetEmailDomainStatus(domainID, "failed", "Removal did not complete.")

		return errors.New("the mail domain was not fully removed")
	}

	s.Store.DeleteEmailDomain(domainID)

	return nil
}

// ------------------------------------------------------------------ webmail

const (
	roundcubeRoot = "/usr/share/ubuntu-panel/roundcube"
	roundcubeDB   = "ubuntu_panel_webmail"
	roundcubeUser = "panelwebmail"
)

func (s *Service) roundcubePassword() string {
	password, _ := s.Store.RememberSecret("roundcube_db_password", 28)

	return password
}

func (s *Service) roundcubeDESKey() string {
	key, _ := s.Store.RememberSecret("roundcube_des_key", 24)

	return key
}

func (s *Service) webmailDatabase() error {
	password := sqlQuote(s.roundcubePassword())

	script := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS %[3]s CHARACTER SET utf8mb4;\n"+
			"CREATE USER IF NOT EXISTS '%[1]s'@'127.0.0.1' IDENTIFIED BY %[2]s;\n"+
			"CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY %[2]s;\n"+
			"ALTER USER '%[1]s'@'127.0.0.1' IDENTIFIED BY %[2]s;\n"+
			"ALTER USER '%[1]s'@'localhost' IDENTIFIED BY %[2]s;\n"+
			"GRANT ALL PRIVILEGES ON %[3]s.* TO '%[1]s'@'127.0.0.1';\n"+
			"GRANT ALL PRIVILEGES ON %[3]s.* TO '%[1]s'@'localhost';\n"+
			"FLUSH PRIVILEGES;\n",
		roundcubeUser, password, roundcubeDB)

	return runSQL(script)
}

// webmailConfig writes Roundcube's configuration.
//
// IMAP and SMTP over the loopback: Dovecot and Postfix are on this machine, so
// there is no certificate to validate and nothing on the wire to overhear.
func (s *Service) webmailConfig(host string) error {
	contents := fmt.Sprintf(
		"<?php\n"+
			"/* Managed by %s. Changes here are overwritten. */\n"+
			"$config['db_dsnw'] = 'mysql://%s:%s@127.0.0.1/%s';\n"+
			"$config['imap_host'] = 'localhost:143';\n"+
			"$config['smtp_host'] = 'localhost:587';\n"+
			"$config['smtp_user'] = '%%u';\n"+
			"$config['smtp_pass'] = '%%p';\n"+
			"$config['support_url'] = '';\n"+
			"$config['product_name'] = 'Webmail';\n"+
			"$config['des_key'] = '%s';\n"+
			"$config['plugins'] = ['archive', 'zipdownload'];\n"+
			"$config['skin'] = 'elastic';\n"+
			"$config['enable_installer'] = false;\n"+
			"$config['mail_domain'] = '%s';\n",
		config.Name, roundcubeUser, s.roundcubePassword(), roundcubeDB,
		s.roundcubeDESKey(), host)

	return proc.PutFile(roundcubeRoot+"/config/config.inc.php", contents, true)
}

// webmailVhost is the nginx configuration for one domain's webmail. Port 80
// carries the ACME challenge and redirects; 443 answers from the first run, on
// the self-signed pair until certbot has been — exactly as a site with no
// certificate yet does.
func webmailVhost(host, php string, tls, roundcube bool) string {
	cert := sites.DefaultCert + ".crt"
	key := sites.DefaultCert + ".key"

	if tls {
		cert = "/etc/letsencrypt/live/" + host + "/fullchain.pem"
		key = "/etc/letsencrypt/live/" + host + "/privkey.pem"
	}

	if php == "" {
		php = "8.3"
	}

	// Without Roundcube there is nothing to serve, but the hostname still has
	// to answer: it is what the MX points at and what the certificate is for,
	// and a 443 that nothing listens on lands on the catch-all with a
	// self-signed certificate — which is what "mail.example.com has no SSL"
	// actually is.
	body := "    location / { return 200 \"Webmail is not installed on this server.\\n\"; }\n" +
		"    default_type text/plain;\n"

	if roundcube {
		body = fmt.Sprintf(
			"    root %s;\n"+
				"    index index.php;\n"+
				"    client_max_body_size 64M;\n"+
				"\n"+
				"    location / { try_files $uri $uri/ /index.php$is_args$args; }\n"+
				"\n"+
				"    location ~ ^/(config|temp|logs|bin|SQL|installer)/ { deny all; }\n"+
				"    location ~ /\\.(?!well-known).* { deny all; }\n"+
				"\n"+
				"    location ~ \\.php$ {\n"+
				"        include snippets/fastcgi-php.conf;\n"+
				"        fastcgi_pass unix:/run/php/php%s-fpm.sock;\n"+
				"        fastcgi_read_timeout 300;\n"+
				"        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;\n"+
				"        fastcgi_param HTTPS $panel_https;\n"+
				"    }\n", roundcubeRoot, php)
	}

	return fmt.Sprintf(
		"# Managed by %s — webmail for %s\n"+
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
			"    ssl_certificate     %s;\n"+
			"    ssl_certificate_key %s;\n"+
			"    ssl_protocols TLSv1.2 TLSv1.3;\n"+
			"\n"+
			"    access_log /var/log/nginx/%s-access.log;\n"+
			"    error_log  /var/log/nginx/%s-error.log;\n"+
			"\n"+
			"%s"+
			"}\n",
		config.Name, host, host, sites.ACMEWebroot, host, cert, key,
		host, host, body)
}

// webmailPHPVersion decides which PHP runs the webmail.
//
// Not the panel's default, which is whatever the newest version the panel
// offers is — Roundcube 1.6 was written against PHP 8.1 to 8.3 and fatals on
// anything newer, which arrives in the browser as a bare HTTP 500 with the
// reason in a log nobody is looking at. Sites can have the newest PHP; this one
// application needs a version it was tested against, and gets its own FPM pool
// if the machine has none.
func webmailPHPVersion(runner *tasks.Runner) (string, error) {
	for _, version := range []string{"8.3", "8.2", "8.1"} {
		if proc.Ok("test -S /run/php/php" + version + "-fpm.sock") {
			return version, nil
		}
	}

	runner.Write("No PHP version Roundcube supports is installed; adding PHP 8.3 for it.\n")

	packages := "php8.3-fpm php8.3-cli php8.3-common php8.3-mysql php8.3-curl " +
		"php8.3-mbstring php8.3-xml php8.3-zip php8.3-gd php8.3-intl"

	if err := runner.Must("sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "+packages+
		" && sudo systemctl enable --now php8.3-fpm", 15*time.Minute); err != nil {
		return "", err
	}

	if !proc.Ok("test -S /run/php/php8.3-fpm.sock") {
		return "", errors.New("PHP 8.3 installed but its FPM socket did not appear.")
	}

	return "8.3", nil
}

// serveMailHost puts a vhost behind mail.<domain>, whether or not Roundcube is
// there to fill it.
//
// The hostname is what the MX points at, what mail clients connect to and what
// the certificate is issued for, so it has to answer on both ports from the
// first setup. Serving it only when webmail happened to be installed is what
// left https://mail.<domain> landing on the catch-all behind the self-signed
// pair — a certificate warning for a mail domain that was otherwise set up
// correctly.
//
// Every piece here is written to be rewritten, so this runs again on every
// domain setup and lands in the same place.
func (s *Service) serveMailHost(ctx context.Context, domainID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		domain, err := s.Store.FindEmailDomain(ctx, domainID)
		if err != nil {
			return "", errors.New("mail domain vanished")
		}

		host := domain.WebmailHost()
		roundcube := s.Store.ServiceInstalled("roundcube")

		var log strings.Builder

		php := ""

		if roundcube {
			if err := s.webmailDatabase(); err != nil {
				return "", err
			}

			// Roundcube ships its schema as a file, and it is a CREATE script
			// rather than an upgrade one — so it is imported only when the
			// tables are absent.
			if err := runner.Must(fmt.Sprintf(
				"sudo mysql %[1]s -e 'SELECT 1 FROM users LIMIT 1' >/dev/null 2>&1 || "+
					"sudo mysql %[1]s < %[2]s/SQL/mysql.initial.sql", roundcubeDB, roundcubeRoot),
				2*time.Minute); err != nil {
				return "", err
			}

			// Roundcube writes to these two and nothing else.
			if err := runner.Must(fmt.Sprintf(
				"sudo mkdir -p %[1]s/temp %[1]s/logs && "+
					"sudo chown -R www-data:www-data %[1]s/temp %[1]s/logs",
				roundcubeRoot), time.Minute); err != nil {
				return "", err
			}

			if err := s.webmailConfig(domain.Domain); err != nil {
				return "", err
			}

			if php, err = webmailPHPVersion(runner); err != nil {
				return "", err
			}
		} else {
			log.WriteString("Roundcube is not installed, so " + host + " answers with a note " +
				"rather than a webmail. Install it from Apps and set this domain up again.\n")
		}

		available := "/etc/nginx/sites-available/webmail-" + domain.Domain

		// The plain form first: nginx will not load a vhost naming a
		// certificate that has not been issued.
		if err := proc.PutFile(available, webmailVhost(host, php, false, roundcube), true); err != nil {
			return "", err
		}

		if err := runner.Must(fmt.Sprintf(
			"sudo ln -sfn '%s' '/etc/nginx/sites-enabled/webmail-%s' && "+
				"sudo nginx -t && sudo systemctl reload nginx", available, domain.Domain),
			time.Minute); err != nil {
			return "", err
		}

		fmt.Fprintf(&log, "%s is being served.\n", host)

		return log.String(), nil
	}
}

// secureMailHost gets a certificate for mail.<domain> and puts everything that
// speaks TLS behind it.
//
// Optional, in the sense that failing leaves a working mail domain behind a
// browser warning rather than no mail domain at all.
func (s *Service) secureMailHost(ctx context.Context, domainID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		domain, err := s.Store.FindEmailDomain(ctx, domainID)
		if err != nil {
			return "", errors.New("mail domain vanished")
		}

		host := domain.WebmailHost()
		available := "/etc/nginx/sites-available/webmail-" + domain.Domain
		roundcube := s.Store.ServiceInstalled("roundcube")

		php := ""
		if roundcube {
			php, _ = webmailPHPVersion(runner)
		}

		var log strings.Builder

		if !proc.Ok("command -v certbot >/dev/null 2>&1") {
			s.webmailSmokeTest(runner, host)

			return "Certbot is not installed, so " + host + " stays on the self-signed " +
				"certificate. Install Certbot from Apps and set this domain up again.", nil
		}

		// The A record was published a moment ago and Let's Encrypt is about to
		// ask a resolver that has not heard about it yet. Waiting here is the
		// difference between a certificate and an error about a challenge that
		// says nothing about DNS.
		expected := system.PublicIP()
		resolution := system.WaitForHostname(ctx, host, expected, 2*time.Minute)

		switch {
		case len(resolution.Addresses) == 0:
			s.webmailSmokeTest(runner, host)

			return fmt.Sprintf(
				"%s does not resolve yet, so no certificate was requested — issuing against a "+
					"hostname nothing answers for fails every time. Add an A record pointing it "+
					"at %s, then set this domain up again.", host, expected), nil

		case !resolution.Matches:
			fmt.Fprintf(&log, "%s resolves to %s rather than %s. Trying anyway: a proxy in "+
				"front passes the challenge through, and a stale record does not.\n",
				host, strings.Join(resolution.Addresses, ", "), expected)

		default:
			fmt.Fprintf(&log, "%s resolves to this server.\n", host)
		}

		runner.Exec(fmt.Sprintf(
			"sudo certbot certonly --webroot -w %s -d %s --non-interactive --agree-tos "+
				"--email %s --keep-until-expiring --no-eff-email 2>&1",
			sites.ACMEWebroot, proc.Quote(host), proc.Quote("admin@"+domain.Domain)), 10*time.Minute)

		if !proc.Ok("sudo test -f " + proc.Quote("/etc/letsencrypt/live/"+host+"/fullchain.pem")) {
			fmt.Fprintf(&log,
				"No certificate was issued for %s, so it stays on the self-signed one. "+
					"The certbot output above says why.\n", host)

			s.webmailSmokeTest(runner, host)

			return log.String(), nil
		}

		if err := proc.PutFile(available, webmailVhost(host, php, true, roundcube), true); err != nil {
			return "", err
		}

		// Tested before the reload: a vhost nginx refuses freezes the running
		// configuration for every site on the box.
		if err := runner.Must("sudo nginx -t && sudo systemctl reload nginx", time.Minute); err != nil {
			proc.PutFile(available, webmailVhost(host, php, false, roundcube), true)
			proc.Ok("sudo nginx -t && sudo systemctl reload nginx")

			return "", err
		}

		fmt.Fprintf(&log, "Certificate issued — https://%s is live.\n", host)

		// The same certificate is what IMAP and SMTP should present. Without
		// this they keep the snakeoil pair, and a mail client says the server
		// cannot be verified for a domain whose webmail is perfectly secure.
		if s.Store.Setting("mail_hostname", host) == host {
			if err := s.useCertificate(runner, host); err != nil {
				fmt.Fprintf(&log, "The mail server kept its own certificate: %v\n", err)
			} else {
				fmt.Fprintf(&log, "Postfix and Dovecot now present the certificate for %s.\n", host)
			}
		}

		s.webmailSmokeTest(runner, host)

		return log.String(), nil
	}
}

// useCertificate points Postfix and Dovecot at an issued certificate.
//
// Both are asked to prove they will start with it before anything is reloaded:
// Dovecot refuses to start on a certificate it cannot read, and a mail server
// that will not come up is worse than one presenting a self-signed pair.
func (s *Service) useCertificate(runner *tasks.Runner, host string) error {
	live := "/etc/letsencrypt/live/" + host

	// The panel user cannot traverse /etc/letsencrypt, and neither can dovecot
	// unless the group is opened up — which is what certbot's own nginx plugin
	// does on install.
	command := fmt.Sprintf(
		"sudo chmod 0755 /etc/letsencrypt/live /etc/letsencrypt/archive && "+
			"sudo postconf -e 'smtpd_tls_cert_file = %[1]s/fullchain.pem' && "+
			"sudo postconf -e 'smtpd_tls_key_file = %[1]s/privkey.pem' && "+
			"printf '%%s\\n' 'ssl_cert = <%[1]s/fullchain.pem' 'ssl_key = <%[1]s/privkey.pem' "+
			"| sudo tee /etc/dovecot/conf.d/99-ubuntu-panel-tls.conf > /dev/null && "+
			"sudo doveconf -n > /dev/null && "+
			"sudo systemctl reload postfix && sudo systemctl restart dovecot", live)

	if err := runner.Must(command, 2*time.Minute); err != nil {
		// Put Dovecot back on whatever it had rather than leaving it refusing
		// to start.
		proc.Ok("sudo rm -f /etc/dovecot/conf.d/99-ubuntu-panel-tls.conf && " +
			"sudo systemctl restart dovecot")

		return err
	}

	return nil
}

// webmailSmokeTest asks for the page.
//
// A vhost that loads and a webmail that runs are different claims, and the
// difference used to reach the operator as a bare HTTP 500 in a browser with
// the reason sitting in a log on the machine. Not fatal — the webmail is
// installed either way — but the console should not be the last place to find
// out.
func (s *Service) webmailSmokeTest(runner *tasks.Runner, host string) {
	runner.Exec(fmt.Sprintf(
		"code=$(curl -sS -k -o /dev/null -w '%%{http_code}' --max-time 20 "+
			"  --resolve '%[1]s:443:127.0.0.1' 'https://%[1]s/' 2>/dev/null); "+
			"if [ \"$code\" = 200 ] || [ \"$code\" = 302 ]; then "+
			"  echo \"webmail answered HTTP $code\"; "+
			"else "+
			"  echo \"webmail answered HTTP $code — the last lines of its error log:\"; "+
			"  sudo tail -n 25 '/var/log/nginx/%[1]s-error.log' 2>/dev/null || true; "+
			"fi", host), time.Minute)
}
