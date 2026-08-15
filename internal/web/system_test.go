package web

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// The three shapes the panel's own vhost comes in.
func panelVhosts() map[string]string {
	return map[string]string{
		"no hostname":                 panelVhost("", 8443, 8088, "8.3", false),
		"hostname":                    panelVhost("panel.example.com", 8443, 8088, "8.3", false),
		"hostname with a certificate": panelVhost("panel.example.com", 8443, 8088, "8.3", true),
	}
}

// The panel is on its own port, and the panel is not on 80 or 443.
//
// This is the whole point of the port: somebody who types this machine's bare
// address into a browser is not shown a control panel's login form. What
// answers there is the catch-all's 404, and the only thing the panel puts on
// those ports is a redirect for a hostname somebody deliberately set.
func TestThePanelIsServedOnlyOnItsOwnPort(t *testing.T) {
	for name, vhost := range panelVhosts() {
		if !strings.Contains(vhost, "listen 8443 ") {
			t.Errorf("the %s vhost does not listen on the panel's port:\n%s", name, vhost)
		}

		for _, block := range strings.Split(vhost, "server {")[1:] {
			onPanelPort := strings.Contains(block, "listen 8443")

			if onPanelPort {
				if !strings.Contains(block, "proxy_pass http://127.0.0.1:8088") {
					t.Errorf("the %s vhost's panel-port block does not proxy to the panel:\n%s",
						name, block)
				}

				continue
			}

			// Everything else is a signpost: the ACME challenge, and a redirect
			// to where the panel is. Never the panel itself.
			if strings.Contains(block, "proxy_pass") {
				t.Errorf("the %s vhost serves the panel on a port other than its own:\n%s",
					name, block)
			}

			if !strings.Contains(block, "return 301") {
				t.Errorf("the %s vhost has a block on 80 or 443 that is not a redirect:\n%s",
					name, block)
			}
		}
	}
}

// With no hostname there is nothing on 80 or 443 at all — not even a redirect,
// because there is no name to redirect. The bare address belongs to the
// catch-all, which is what answers "No site is configured for this hostname."
func TestWithNoHostnameThePanelTouchesNeitherPort(t *testing.T) {
	vhost := panelVhost("", 8443, 8088, "8.3", false)

	for _, listen := range []string{"listen 80", "listen [::]:80", "listen 443"} {
		if strings.Contains(vhost, listen+";") || strings.Contains(vhost, listen+" ") {
			t.Errorf("a panel with no hostname still claims %q:\n%s", listen, vhost)
		}
	}
}

// A hostname is a signpost to the panel's real address, on both ports it might
// be typed on. Without a certificate there is only the plain one, because a 443
// block naming a certificate that is not there is a configuration nginx refuses
// — for every site on the machine, not just this one.
func TestAHostnameRedirectsToThePanelPort(t *testing.T) {
	plain := panelVhost("panel.example.com", 8443, 8088, "8.3", false)

	if !strings.Contains(plain, "return 301 http://panel.example.com:8443$request_uri;") {
		t.Errorf("the hostname does not redirect to the panel's port:\n%s", plain)
	}

	if strings.Contains(plain, "listen 443") {
		t.Errorf("a hostname with no certificate claims 443:\n%s", plain)
	}

	secure := panelVhost("panel.example.com", 8443, 8088, "8.3", true)

	if strings.Count(secure, "return 301 https://panel.example.com:8443$request_uri;") != 2 {
		t.Errorf("both ports should redirect to the HTTPS panel address:\n%s", secure)
	}

	if !strings.Contains(secure, "listen 443 ssl;") {
		t.Errorf("the certificate shape does not answer on 443 to redirect:\n%s", secure)
	}

	// Never the default server on ports the catch-all owns: nginx does not warn
	// about a second one, it refuses the whole configuration.
	for _, wrong := range []string{"listen 80 default_server", "listen 443 ssl default_server"} {
		if strings.Contains(secure, wrong) {
			t.Errorf("the panel vhost claims %q, which the catch-all already owns:\n%s",
				wrong, secure)
		}
	}
}

// The ACME challenge has to survive on port 80, or the hostname's certificate
// can never be issued or renewed.
func TestTheHostnameKeepsTheACMEChallengeOnPortEighty(t *testing.T) {
	for _, secure := range []bool{false, true} {
		vhost := panelVhost("panel.example.com", 8443, 8088, "8.3", secure)

		challenge := strings.Index(vhost, "location ^~ /.well-known/acme-challenge/")
		if challenge < 0 {
			t.Fatalf("the ACME challenge is not served at all (secure=%v):\n%s", secure, vhost)
		}

		// It belongs to the plain block, which is the one certbot's webroot
		// check reaches.
		if !strings.Contains(vhost[:challenge], "listen 80;") {
			t.Errorf("the ACME challenge is not on port 80 (secure=%v):\n%s", secure, vhost)
		}
	}
}

// phpMyAdmin is served from the panel's own origin, which is now its own port.
func TestThePanelVhostsServePhpMyAdmin(t *testing.T) {
	for name, vhost := range panelVhosts() {
		if !strings.Contains(vhost, "location ^~ /phpmyadmin {") {
			t.Errorf("the %s vhost does not serve phpMyAdmin:\n%s", name, vhost)
		}
	}
}

// The guard has to be in every block that serves phpMyAdmin. A block that
// serves it without the subrequest is a MariaDB administrator on a public URL.
func TestEveryServedPhpMyAdminIsGuarded(t *testing.T) {
	for name, vhost := range panelVhosts() {
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

// The address the panel prints for itself always names the port.
//
// This is the one that matters outside the browser: it is what an MCP client is
// pointed at, what the AI access page shows, and what Settings links to. An
// address without the port goes to whichever site answers on 80, or to the
// catch-all's 404 — never to the panel.
func TestThePanelAddressNamesItsPort(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	defer db.Close()

	app := &App{
		Store:  db,
		Config: &config.Config{BindHost: "127.0.0.1", BindPort: 8088, PanelPort: 8443},
	}

	db.SetSetting("panel_domain", "panel.example.com")

	if url := app.panelURL(); url != "http://panel.example.com:8443" {
		t.Errorf("a hostname with no certificate gave %q", url)
	}

	db.SetSetting("panel_domain_tls", "1")

	if url := app.panelURL(); url != "https://panel.example.com:8443" {
		t.Errorf("a hostname with a certificate gave %q", url)
	}

	// The operator's own choice of port wins over the one in the configuration
	// file, because it is the one they made from the panel.
	db.SetSetting(panelPortSetting, "9443")

	if url := app.panelURL(); url != "https://panel.example.com:9443" {
		t.Errorf("a moved panel gave %q", url)
	}

	// And 80 or 443 are refused wherever they come from: they are the ports the
	// sites are on, and the panel being off them is the whole point.
	db.SetSetting(panelPortSetting, "80")

	if port := app.panelPort(); port != config.DefaultPanelPort {
		t.Errorf("port 80 was accepted as the panel's own port: %d", port)
	}
}
