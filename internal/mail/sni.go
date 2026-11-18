package mail

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// One mail server, many mail domains, one certificate per domain.
//
// Every mail domain gets a Let's Encrypt certificate for mail.<domain>, and
// that is the hostname its mail clients and websites are told to connect to.
// Postfix and Dovecot used to present only the first domain's certificate —
// the one the server greets other servers as — to every client. A WordPress
// site sending through mail.tulibuilder.com:465 was handed the certificate for
// mail.coldireach.com, PHPMailer refused it as a hostname mismatch, and all
// anyone saw was "Could not connect to SMTP host".
//
// So every issued mail.<domain> certificate is also offered by name (SNI):
// Postfix through tls_server_sni_maps, Dovecot through local_name blocks. The
// server-wide default certificate stays as it is, for clients that send no
// name at all.

const (
	postfixSNISource = "/etc/postfix/tls_server_sni"
	postfixSNIMap    = "hash:" + postfixSNISource
	dovecotSNIConf   = "/etc/dovecot/conf.d/99-ubuntu-panel-sni.conf"

	// certbot runs this after every renewal. Postfix's SNI table holds a copy
	// of each certificate (postmap -F), not a path, so without rebuilding it a
	// renewed certificate would never reach SMTP clients and the old one would
	// expire in front of them.
	renewalHook = "/etc/letsencrypt/renewal-hooks/deploy/ubuntu-panel-mail-sni.sh"
)

// mailCertificate is one issued mail.<domain> certificate.
type mailCertificate struct {
	host, chain, key string
}

func liveCertificate(host string) mailCertificate {
	live := "/etc/letsencrypt/live/" + host

	return mailCertificate{host: host, chain: live + "/fullchain.pem", key: live + "/privkey.pem"}
}

// postfixSNITable is the source of the SNI map: hostname, then the key, then
// the chain — the order postmap -F wants them in.
func postfixSNITable(certs []mailCertificate) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Managed by %s — one line per mail domain with a certificate.\n", config.Name)

	for _, c := range certs {
		fmt.Fprintf(&b, "%s %s %s\n", c.host, c.key, c.chain)
	}

	return b.String()
}

// dovecotSNIConfig is the local_name block per certificate. Dovecot 2.4
// renamed the settings and dropped the "<" that 2.3 needs to read a file.
func dovecotSNIConfig(certs []mailCertificate, v24 bool) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Managed by %s — the certificate each mail.<domain> is reached by.\n", config.Name)

	for _, c := range certs {
		if v24 {
			fmt.Fprintf(&b, "local_name %s {\n  ssl_server_cert_file = %s\n  ssl_server_key_file = %s\n}\n",
				c.host, c.chain, c.key)
		} else {
			fmt.Fprintf(&b, "local_name %s {\n  ssl_cert = <%s\n  ssl_key = <%s\n}\n", c.host, c.chain, c.key)
		}
	}

	return b.String()
}

const renewalHookScript = `#!/bin/sh
# Managed by ` + config.Name + `: a renewed certificate reaches mail clients.
[ -f ` + postfixSNISource + ` ] && postmap -F ` + postfixSNIMap + `
systemctl reload postfix 2>/dev/null || true
systemctl reload dovecot 2>/dev/null || true
exit 0
`

// PresentCertificates makes Postfix and Dovecot offer every mail domain's own
// certificate to clients that ask for it by name.
//
// It reads the machine rather than remembering anything, so it is safe to run
// as often as wanted: after a domain's certificate is issued, after a domain
// is removed, and on every start of the panel — which is how a server set up
// by an older panel is repaired by updating it.
func (s *Service) PresentCertificates(ctx context.Context) (string, error) {
	if !s.Store.ServiceInstalled("mail") {
		return "", nil
	}

	domains, err := s.Store.EmailDomains(ctx)
	if err != nil {
		return "", err
	}

	var certs []mailCertificate

	for _, domain := range domains {
		if domain.Status == "deleting" {
			continue
		}

		c := liveCertificate(domain.WebmailHost())
		if proc.Ok("sudo test -s " + proc.Quote(c.chain) + " && sudo test -s " + proc.Quote(c.key)) {
			certs = append(certs, c)
		}
	}

	sort.Slice(certs, func(i, j int) bool { return certs[i].host < certs[j].host })

	if err := proc.PutFile(renewalHook, renewalHookScript, true); err != nil {
		return "", err
	}

	proc.Ok("sudo chmod 0755 " + proc.Quote(renewalHook))

	if len(certs) == 0 {
		proc.Run("sudo postconf -X tls_server_sni_maps; "+
			"sudo rm -f "+postfixSNISource+" "+postfixSNISource+".db "+dovecotSNIConf+"; "+
			"sudo systemctl reload postfix; sudo systemctl reload dovecot", 2*time.Minute)

		return "No mail domain has a certificate of its own yet.", nil
	}

	version := proc.Run("dovecot --version", 30*time.Second).Output
	v24 := strings.HasPrefix(strings.TrimSpace(version), "2.4") || strings.HasPrefix(strings.TrimSpace(version), "3.")

	if err := proc.PutFile(postfixSNISource, postfixSNITable(certs), true); err != nil {
		return "", err
	}

	if err := proc.PutFile(dovecotSNIConf, dovecotSNIConfig(certs, v24), true); err != nil {
		return "", err
	}

	var log strings.Builder

	// Postfix first. The map is built before main.cf names it, so a failed
	// postmap never leaves Postfix pointing at a table that is not there.
	postfix := proc.Run("sudo chmod 0755 /etc/letsencrypt/live /etc/letsencrypt/archive && "+
		"sudo postmap -F "+postfixSNIMap+" && "+
		"sudo chmod 0600 "+postfixSNISource+" "+postfixSNISource+".db && "+
		"sudo postconf -e 'tls_server_sni_maps = "+postfixSNIMap+"' && "+
		"sudo systemctl reload postfix", 2*time.Minute)

	if !postfix.OK() {
		proc.Run("sudo postconf -X tls_server_sni_maps; sudo systemctl reload postfix", time.Minute)
		fmt.Fprintf(&log, "Postfix kept its single certificate: %s\n", postfix.Output)
	} else {
		fmt.Fprintf(&log, "Postfix presents the certificate for each of %d mail domains.\n", len(certs))
	}

	// Dovecot is checked before it is reloaded: one that refuses its
	// configuration does not come back up, and no mail can be read at all.
	dovecot := proc.Run("sudo doveconf -n > /dev/null && sudo systemctl reload dovecot", 2*time.Minute)

	if !dovecot.OK() {
		proc.Run("sudo rm -f "+dovecotSNIConf+" && sudo systemctl reload dovecot", time.Minute)
		fmt.Fprintf(&log, "Dovecot kept its single certificate: %s\n", dovecot.Output)
	} else {
		fmt.Fprintf(&log, "Dovecot presents the certificate for each of %d mail domains.\n", len(certs))
	}

	if !postfix.OK() || !dovecot.OK() {
		return log.String(), fmt.Errorf("the mail certificates are not all being presented")
	}

	return log.String(), nil
}
