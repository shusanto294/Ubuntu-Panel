package mail

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/sites"
)

// The webmail and autoconfig hostnames log to files of their own, so their
// redirects and 404s are not counted as the server's bare IP answering.
func TestMailVhostsLogEveryBlock(t *testing.T) {
	for name, vhost := range map[string]string{
		"webmail":        webmailVhost("mail.example.com", "8.3", false, true),
		"webmail tls":    webmailVhost("mail.example.com", "8.3", true, false),
		"autoconfig":     autoconfigVhost("example.com", "autoconfig.example.com", false),
		"autoconfig tls": autoconfigVhost("example.com", "autoconfig.example.com", true),
	} {
		if _, changed := sites.AddMissingAccessLogs(vhost); changed {
			t.Errorf("%s has a block with no access_log:\n%s", name, vhost)
		}
	}
}

// The autoconfig hostname answers the one file mail clients ask for, and
// closes every other request without a reply.
func TestAutoconfigAnswersOnlyTheMailSettings(t *testing.T) {
	vhost := autoconfigVhost("example.com", "autoconfig.example.com", true)

	if !strings.Contains(vhost, "location = /mail/config-v1.1.xml {") {
		t.Error("the settings file is no longer served")
	}
	if !strings.Contains(vhost, "location ^~ /.well-known/acme-challenge/") {
		t.Error("the certificate challenge is no longer served")
	}
	if strings.Contains(vhost, "return 404") || strings.Count(vhost, "return 444;") != 2 {
		t.Errorf("other paths are still answered:\n%s", vhost)
	}
}
