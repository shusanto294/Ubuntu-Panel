package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// Mail clients that configure themselves.
//
// A person setting up a mailbox knows their address and their password. They do
// not know that IMAP is on 993 with SSL and submission is on 587 with STARTTLS,
// and typing one of the four wrong is how mailbox setup fails in a way nothing
// explains. Mozilla's autoconfiguration is the fix: Thunderbird asks
// `autoconfig.<domain>` for one XML file, reads the hostnames and ports out of
// it, and the person only ever types the two things they know.
//
// It takes both halves. The DNS record — published in dns.SyncForMail beside the
// MX and the SPF — is what points the lookup at this machine; the vhost here is
// what answers it. Either one alone does nothing at all.
const autoconfigRoot = "/usr/share/ubuntu-panel/autoconfig"

// autoconfigXML is the file Thunderbird reads: Mozilla's clientConfig v1.1.
//
// The settings are this server's own, not a guess. IMAP on 993 and POP3 on 995
// are implicit TLS; submission is 587 with STARTTLS, which is the port Postfix
// is configured for and the one that works from a network that blocks 25. The
// username is the whole address, because Dovecot's virtual mailboxes are named
// that way — %EMAILADDRESS% is Mozilla's placeholder for what the person typed,
// and the client fills it in.
//
// `password-cleartext` is the authentication method, not the transport: the
// password is sent as-is inside a TLS session, which is what every mail client
// means by "normal password". Anything else here would have the client offer a
// mechanism Dovecot is not configured for and fail the first login.
func autoconfigXML(domain, mailHost string) string {
	return fmt.Sprintf(
		"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
			"<!-- Managed by %[3]s. Changes here are overwritten. -->\n"+
			"<clientConfig version=\"1.1\">\n"+
			"  <emailProvider id=\"%[1]s\">\n"+
			"    <domain>%[1]s</domain>\n"+
			"    <displayName>%[1]s</displayName>\n"+
			"    <displayShortName>%[1]s</displayShortName>\n"+
			"\n"+
			"    <incomingServer type=\"imap\">\n"+
			"      <hostname>%[2]s</hostname>\n"+
			"      <port>993</port>\n"+
			"      <socketType>SSL</socketType>\n"+
			"      <authentication>password-cleartext</authentication>\n"+
			"      <username>%%EMAILADDRESS%%</username>\n"+
			"    </incomingServer>\n"+
			"\n"+
			"    <incomingServer type=\"pop3\">\n"+
			"      <hostname>%[2]s</hostname>\n"+
			"      <port>995</port>\n"+
			"      <socketType>SSL</socketType>\n"+
			"      <authentication>password-cleartext</authentication>\n"+
			"      <username>%%EMAILADDRESS%%</username>\n"+
			"    </incomingServer>\n"+
			"\n"+
			"    <outgoingServer type=\"smtp\">\n"+
			"      <hostname>%[2]s</hostname>\n"+
			"      <port>587</port>\n"+
			"      <socketType>STARTTLS</socketType>\n"+
			"      <authentication>password-cleartext</authentication>\n"+
			"      <username>%%EMAILADDRESS%%</username>\n"+
			"    </outgoingServer>\n"+
			"\n"+
			"    <documentation url=\"https://%[2]s/\">\n"+
			"      <descr lang=\"en\">Webmail for %[1]s</descr>\n"+
			"    </documentation>\n"+
			"  </emailProvider>\n"+
			"</clientConfig>\n",
		domain, mailHost, config.Name)
}

// autoconfigPath is where this domain's XML lives on disk.
func autoconfigPath(domain string) string {
	return autoconfigRoot + "/" + domain + "/mail/config-v1.1.xml"
}

// autoconfigVhost serves that file, and nothing else.
//
// Port 80 serves it rather than redirecting, which is the difference between
// this and every other vhost the panel writes. A client tries HTTPS first and
// falls back to HTTP, and until certbot has issued a certificate for
// `autoconfig.<domain>` the HTTPS attempt meets the self-signed pair and is
// abandoned — so plain 80 is the port that actually answers on a domain set up
// a minute ago. There is nothing secret in the file: it is hostnames and port
// numbers, published so that anybody with an address on the domain can read
// them.
//
// The exact path matters. Mozilla's clients request `/mail/config-v1.1.xml`
// with the address in the query string; `location =` matches the path and
// ignores the query, which is what makes one static file answer every mailbox.
// Everything else on the hostname is a 404 — it exists to answer one question.
func autoconfigVhost(domain, host string, tls bool) string {
	cert := sites.DefaultCert + ".crt"
	key := sites.DefaultCert + ".key"

	if tls {
		cert = "/etc/letsencrypt/live/" + host + "/fullchain.pem"
		key = "/etc/letsencrypt/live/" + host + "/privkey.pem"
	}

	body := fmt.Sprintf(
		"    location = /mail/config-v1.1.xml {\n"+
			"        alias %s;\n"+
			"        default_type application/xml;\n"+
			"        add_header Cache-Control \"public, max-age=3600\";\n"+
			"    }\n"+
			"\n"+
			"    location / {\n"+
			"        default_type text/plain;\n"+
			"        return 404 \"This hostname answers mail client autoconfiguration "+
			"for %s and nothing else.\\n\";\n"+
			"    }\n",
		autoconfigPath(domain), domain)

	return fmt.Sprintf(
		"# Managed by %s — mail client autoconfiguration for %s\n"+
			"#\n"+
			"# Thunderbird and everything else that speaks Mozilla autoconfiguration\n"+
			"# fetches /mail/config-v1.1.xml from this hostname once somebody types an\n"+
			"# address on the domain. Port 80 serves it directly rather than redirecting:\n"+
			"# a client that meets a self-signed certificate on 443 gives up on the\n"+
			"# lookup, and there is nothing here worth hiding.\n"+
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
			"%s"+
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
			"%s"+
			"}\n",
		config.Name, domain, host, sites.ACMEWebroot, body, host, cert, key, body)
}

// serveAutoconfig writes the XML, puts a vhost in front of it and gets that
// hostname a certificate of its own.
//
// Optional in the same sense the webmail is: a domain whose autoconfiguration
// could not be set up is still a working mail domain, and its mailboxes are
// still set up by hand with the hostnames and ports the panel shows.
//
// The certificate is requested separately from the one for `mail.<domain>`
// rather than added to it as a second name. A hostname that does not resolve
// yet fails the whole certbot run it is part of, and putting both in one call
// means a missing autoconfig record costs the mail host its certificate — which
// is the one that matters.
func (s *Service) serveAutoconfig(ctx context.Context, domainID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		domain, err := s.Store.FindEmailDomain(ctx, domainID)
		if err != nil {
			return "", errors.New("mail domain vanished")
		}

		host := domain.AutoconfigHost()
		available := "/etc/nginx/sites-available/autoconfig-" + domain.Domain

		var log strings.Builder

		if err := proc.PutFile(autoconfigPath(domain.Domain),
			autoconfigXML(domain.Domain, domain.WebmailHost()), true); err != nil {
			return "", err
		}

		// The self-signed pair, for the 443 block: a vhost naming a certificate
		// that is not on disk is one nginx refuses for every site on the machine.
		sites.EnsureDefaultCert()

		if err := proc.PutFile(available, autoconfigVhost(domain.Domain, host, false), true); err != nil {
			return "", err
		}

		if err := runner.Must(fmt.Sprintf(
			"sudo ln -sfn '%s' '/etc/nginx/sites-enabled/autoconfig-%s' && "+
				"sudo nginx -t && sudo systemctl reload nginx", available, domain.Domain),
			time.Minute); err != nil {
			return "", err
		}

		fmt.Fprintf(&log, "%s answers /mail/config-v1.1.xml, so a mail client set up with an "+
			"address on %s finds its own settings.\n", host, domain.Domain)

		if !proc.Ok("command -v certbot >/dev/null 2>&1") {
			log.WriteString("Certbot is not installed, so it answers over plain HTTP only — " +
				"which Thunderbird falls back to, and which carries nothing but hostnames " +
				"and port numbers.\n")

			return log.String(), nil
		}

		// The record was published a moment ago and Let's Encrypt is about to ask
		// a resolver that has not heard of it. Same wait the mail host gets, and
		// for the same reason.
		expected := system.PublicIP()

		if resolution := system.WaitForHostname(ctx, host, expected, 2*time.Minute); len(resolution.Addresses) == 0 {
			fmt.Fprintf(&log, "%s does not resolve yet, so no certificate was requested and it "+
				"answers over plain HTTP. Add an A record pointing it at %s, then set this "+
				"domain up again.\n", host, expected)

			return log.String(), nil
		}

		runner.Exec(fmt.Sprintf(
			"sudo certbot certonly --webroot -w %s -d %s --non-interactive --agree-tos "+
				"--email %s --keep-until-expiring --no-eff-email 2>&1",
			sites.ACMEWebroot, proc.Quote(host), proc.Quote("admin@"+domain.Domain)), 10*time.Minute)

		if !proc.Ok("sudo test -f " + proc.Quote("/etc/letsencrypt/live/"+host+"/fullchain.pem")) {
			fmt.Fprintf(&log, "No certificate was issued for %s, so it stays on plain HTTP. "+
				"Mail clients fall back to it, so autoconfiguration still works.\n", host)

			return log.String(), nil
		}

		if err := proc.PutFile(available, autoconfigVhost(domain.Domain, host, true), true); err != nil {
			return "", err
		}

		// Tested before the reload: a vhost nginx refuses freezes the running
		// configuration for every site on the box.
		if err := runner.Must("sudo nginx -t && sudo systemctl reload nginx", time.Minute); err != nil {
			proc.PutFile(available, autoconfigVhost(domain.Domain, host, false), true)
			proc.Ok("sudo nginx -t && sudo systemctl reload nginx")

			return "", err
		}

		fmt.Fprintf(&log, "Certificate issued — https://%s answers as well.\n", host)

		return log.String(), nil
	}
}
