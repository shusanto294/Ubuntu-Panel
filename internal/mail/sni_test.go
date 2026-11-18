package mail

import (
	"strings"
	"testing"
)

// Every mail domain's host has to get its own certificate, in the shape each
// daemon reads: a client connecting to mail.b.com that is shown a.com's
// certificate refuses the connection.
func TestEveryMailHostIsPresentedItsOwnCertificate(t *testing.T) {
	certs := []mailCertificate{liveCertificate("mail.a.com"), liveCertificate("mail.b.com")}

	table := postfixSNITable(certs)
	for _, want := range []string{
		"mail.a.com /etc/letsencrypt/live/mail.a.com/privkey.pem /etc/letsencrypt/live/mail.a.com/fullchain.pem\n",
		"mail.b.com /etc/letsencrypt/live/mail.b.com/privkey.pem /etc/letsencrypt/live/mail.b.com/fullchain.pem\n",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("postfix SNI table is missing %q:\n%s", want, table)
		}
	}

	v23 := dovecotSNIConfig(certs, false)
	if !strings.Contains(v23, "local_name mail.b.com {\n  ssl_cert = </etc/letsencrypt/live/mail.b.com/fullchain.pem\n") {
		t.Errorf("dovecot 2.3 block is wrong:\n%s", v23)
	}

	v24 := dovecotSNIConfig(certs, true)
	if !strings.Contains(v24, "ssl_server_cert_file = /etc/letsencrypt/live/mail.b.com/fullchain.pem") ||
		strings.Contains(v24, "= <") {
		t.Errorf("dovecot 2.4 block is wrong:\n%s", v24)
	}
}

// A renewal has to rebuild the table: postmap -F copies the certificate in.
func TestRenewalRebuildsThePostfixTable(t *testing.T) {
	if !strings.Contains(renewalHookScript, "postmap -F hash:/etc/postfix/tls_server_sni") {
		t.Errorf("the renewal hook does not rebuild the SNI map:\n%s", renewalHookScript)
	}
}
