package web

import (
	"strings"
	"testing"
)

// Every shape of the panel's own vhost has to serve phpMyAdmin, except the
// plain-HTTP block kept for the bare IP address: that one exists so the panel
// stays reachable without a certificate, and a phpMyAdmin login form on it
// would put a MySQL password on the wire in clear.
func TestThePanelVhostsServePhpMyAdmin(t *testing.T) {
	for name, vhost := range map[string]string{
		"default": panelDefaultVhost(8088, "8.3"),
		"named":   panelVhost("panel.example.com 203.0.113.9", 8088, "8.3"),
		"tls":     panelTLSVhost("panel.example.com", "203.0.113.9", 8088, "8.3"),
	} {
		if !strings.Contains(vhost, "location ^~ /phpmyadmin {") {
			t.Errorf("the %s vhost does not serve phpMyAdmin:\n%s", name, vhost)
		}
	}

	// The IP block is the last server block of the TLS shape, and the only
	// plain-HTTP one that serves anything rather than redirecting.
	tls := panelTLSVhost("panel.example.com", "203.0.113.9", 8088, "8.3")

	blocks := strings.Split(tls, "server {")
	last := blocks[len(blocks)-1]

	if !strings.Contains(last, "server_name 203.0.113.9;") {
		t.Fatalf("the last server block is not the IP one:\n%s", last)
	}

	if strings.Contains(last, "/phpmyadmin") {
		t.Error("phpMyAdmin is served over plain HTTP on the IP address")
	}
}

// The guard has to be in every shape of the vhost that serves phpMyAdmin. A
// shape that serves it without the subrequest is a MariaDB administrator on a
// public URL.
func TestEveryServedPhpMyAdminIsGuarded(t *testing.T) {
	for name, vhost := range map[string]string{
		"default": panelDefaultVhost(8088, "8.3"),
		"named":   panelVhost("panel.example.com 203.0.113.9", 8088, "8.3"),
		"tls":     panelTLSVhost("panel.example.com", "203.0.113.9", 8088, "8.3"),
	} {
		for _, block := range strings.Split(vhost, "server {")[1:] {
			if !strings.Contains(block, "location ^~ /phpmyadmin {") {
				continue
			}

			if !strings.Contains(block, "auth_request "+phpMyAdminGatePath+";") {
				t.Errorf("a %s server block serves phpMyAdmin unguarded:\n%s", name, block)
			}

			if !strings.Contains(block, "location = "+phpMyAdminGatePath+" {") {
				t.Errorf("a %s server block has no location answering the guard:\n%s", name, block)
			}
		}
	}
}
