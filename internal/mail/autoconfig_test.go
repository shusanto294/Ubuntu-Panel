package mail

import (
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
)

// The file Thunderbird reads has to describe this server, not a guess at it.
//
// The whole point of autoconfiguration is that nobody types a hostname or a
// port. So every value in it is one the mail server is actually configured for:
// get one wrong and the client sets itself up confidently and then cannot log
// in, which is worse than not answering the lookup at all.
func TestTheAutoconfigFileDescribesThisServer(t *testing.T) {
	xml := autoconfigXML("example.com", "mail.example.com")

	for _, want := range []string{
		"<clientConfig version=\"1.1\">",
		"<domain>example.com</domain>",
		"<incomingServer type=\"imap\">",
		"<port>993</port>",
		"<socketType>SSL</socketType>",
		"<outgoingServer type=\"smtp\">",
		"<port>587</port>",
		"<socketType>STARTTLS</socketType>",
		// Dovecot's virtual mailboxes are named by the whole address, and
		// %EMAILADDRESS% is what the client substitutes for what was typed.
		"<username>%EMAILADDRESS%</username>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("the autoconfig file does not contain %q:\n%s", want, xml)
		}
	}

	// Every hostname in it is this domain's mail host. A stray `example.com` in
	// a hostname element is a client dialling the wrong machine.
	for _, line := range strings.Split(xml, "\n") {
		if !strings.Contains(line, "<hostname>") {
			continue
		}

		if !strings.Contains(line, "<hostname>mail.example.com</hostname>") {
			t.Errorf("a hostname in the autoconfig file is not the mail host: %s", line)
		}
	}
}

// The vhost answers the exact path a mail client asks for, on plain HTTP.
//
// Port 80 serves the file rather than redirecting to it. A client tries HTTPS
// first and gives the lookup up when it meets a self-signed certificate, so
// until certbot has issued one for this hostname plain 80 is the only port that
// answers — and there is nothing in the file but hostnames and port numbers.
func TestTheAutoconfigVhostAnswersOnPlainHTTP(t *testing.T) {
	vhost := autoconfigVhost("example.com", "autoconfig.example.com", false)

	blocks := strings.Split(vhost, "server {")[1:]

	if len(blocks) != 2 {
		t.Fatalf("the autoconfig vhost has %d server blocks, wanted 80 and 443:\n%s",
			len(blocks), vhost)
	}

	for _, block := range blocks {
		if !strings.Contains(block, "server_name autoconfig.example.com;") {
			t.Errorf("an autoconfig block is not named for the hostname:\n%s", block)
		}

		// `location =` matches the path and ignores the query string, which is
		// what lets one static file answer for every address on the domain.
		if !strings.Contains(block, "location = /mail/config-v1.1.xml {") {
			t.Errorf("an autoconfig block does not answer the path a client asks for:\n%s", block)
		}

		if !strings.Contains(block, "alias "+autoconfigPath("example.com")+";") {
			t.Errorf("an autoconfig block does not serve this domain's own file:\n%s", block)
		}
	}

	if strings.Contains(blocks[0], "return 301") {
		t.Errorf("port 80 redirects instead of answering the lookup:\n%s", blocks[0])
	}

	// Before certbot has been the 443 block presents the self-signed pair. A
	// vhost naming a certificate that is not on disk is one nginx refuses for
	// every site on the machine, not just this one.
	if !strings.Contains(vhost, "ssl_certificate     "+sites.DefaultCert+".crt;") {
		t.Errorf("the autoconfig vhost names a certificate that has not been issued:\n%s", vhost)
	}

	secure := autoconfigVhost("example.com", "autoconfig.example.com", true)

	if !strings.Contains(secure,
		"ssl_certificate     /etc/letsencrypt/live/autoconfig.example.com/fullchain.pem;") {
		t.Errorf("the autoconfig vhost does not use its own certificate once it has one:\n%s", secure)
	}
}
