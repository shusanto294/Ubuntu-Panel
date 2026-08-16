package web

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
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

// The panel answers on its own port, and on 80 or 443 only under a hostname
// somebody deliberately set.
//
// This is what keeps the bare address quiet: a stranger who types this machine's
// IP into a browser gets the catch-all's 404 rather than a control panel's login
// form, and never learns there is one here. The catch-all owns `default_server`
// on both ports, so nothing here may claim it — and nginx does not warn about a
// second default server, it refuses the whole configuration.
func TestThePanelClaimsNoBarePort(t *testing.T) {
	for name, vhost := range panelVhosts() {
		if !strings.Contains(vhost, "listen 8443 ") {
			t.Errorf("the %s vhost does not listen on the panel's port:\n%s", name, vhost)
		}

		for _, wrong := range []string{"listen 80 default_server", "listen [::]:80 default_server",
			"listen 443 ssl default_server", "listen [::]:443 ssl default_server"} {
			if strings.Contains(vhost, wrong) {
				t.Errorf("the %s vhost claims %q, which the catch-all already owns:\n%s",
					name, wrong, vhost)
			}
		}

		for _, block := range strings.Split(vhost, "server {")[1:] {
			if strings.Contains(block, "listen 8443") {
				// The block on that port either serves the panel or refuses the
				// name outright — the second is the one that stands where the
				// bare IP used to reach the login form.
				if !strings.Contains(block, "proxy_pass http://127.0.0.1:8088") &&
					!strings.Contains(block, "return 404") {
					t.Errorf("the %s vhost's panel-port block neither proxies nor refuses:\n%s",
						name, block)
				}

				continue
			}

			// Everything else is on 80 or 443, where it has to be named: a block
			// there answering `_` would be serving the panel to whoever found
			// the IP address.
			if !strings.Contains(block, "server_name panel.example.com;") {
				t.Errorf("the %s vhost has a block on 80 or 443 that is not the hostname's:\n%s",
					name, block)
			}
		}
	}
}

// A hostname takes the panel off the bare address altogether, the port
// included.
//
// Before a hostname there is nowhere else to go: the panel's port answers to
// every name, because the IP with the port on it is the only address there is.
// Once the operator has pointed a name at this machine and typed it into
// Settings, that name is the address — and the IP goes quiet on the port the way
// it already is on 80 and 443, with the catch-all's own 404 rather than a login
// form.
func TestAHostnameTakesThePanelOffTheBareAddress(t *testing.T) {
	// With no hostname the port is the address, and answers to anything.
	bare := panelVhost("", 8443, 8088, "8.3", false)

	if !strings.Contains(bare, "listen 8443 default_server;") ||
		!strings.Contains(bare, "server_name _;") {
		t.Errorf("with no hostname the panel's port is not the address that works:\n%s", bare)
	}

	if strings.Contains(bare, "return 404") {
		t.Errorf("with no hostname the panel's port refuses the address it is reached on:\n%s", bare)
	}

	for _, secure := range []bool{false, true} {
		vhost := panelVhost("panel.example.com", 8443, 8088, "8.3", secure)

		for _, block := range strings.Split(vhost, "server {")[1:] {
			if !strings.Contains(block, "listen 8443") {
				continue
			}

			// The block that serves the panel is named. A `_` there is the
			// bare IP opening the login form again, which is the bug.
			if strings.Contains(block, "proxy_pass") &&
				!strings.Contains(block, "server_name panel.example.com;") {
				t.Errorf("the panel's port serves the panel to any name (secure=%v):\n%s",
					secure, block)
			}

			if strings.Contains(block, "server_name _;") {
				if !strings.Contains(block, "return 404 \"No site is configured for this hostname.") {
					t.Errorf("the panel's port answers an unknown name with something other "+
						"than the catch-all's 404 (secure=%v):\n%s", secure, block)
				}

				// A default server on an `ssl` port with no certificate is a
				// configuration nginx refuses for the whole machine.
				if strings.Contains(block, " ssl ") &&
					!strings.Contains(block, "ssl_certificate     "+sites.DefaultCert+".crt;") {
					t.Errorf("the refusing block speaks TLS with no certificate (secure=%v):\n%s",
						secure, block)
				}
			}
		}

		// Exactly one default server on the port, and it is the refusing one.
		if got := strings.Count(vhost, "listen 8443 default_server;") +
			strings.Count(vhost, "listen 8443 ssl default_server;"); got != 1 {
			t.Errorf("the panel's port has %d default servers, wanted 1 (secure=%v):\n%s",
				got, secure, vhost)
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

// A hostname serves the panel on the ports a browser does not have to be told
// about, so the address is the name and nothing else.
//
// Both shapes answer on 443, with the self-signed pair until a real certificate
// exists. A hostname with nothing on 443 is a TLS handshake that fails, which is
// what a CDN in front of it reports as an origin SSL error — and what stopped
// the certificate being issued in the first place.
func TestAHostnameServesThePanelWithoutAPort(t *testing.T) {
	plain := panelVhost("panel.example.com", 8443, 8088, "8.3", false)

	if !strings.Contains(plain, "listen 443 ssl;") {
		t.Errorf("a hostname with no certificate does not answer on 443 at all:\n%s", plain)
	}

	if !strings.Contains(plain, "ssl_certificate     "+sites.DefaultCert+".crt;") {
		t.Errorf("a hostname with no certificate does not present the self-signed pair:\n%s", plain)
	}

	// Plain 80 serves the panel rather than redirecting into a certificate
	// warning, so the hostname works the moment it is set.
	if strings.Contains(plain, "return 301") {
		t.Errorf("a hostname with no certificate redirects into its own warning:\n%s", plain)
	}

	secure := panelVhost("panel.example.com", 8443, 8088, "8.3", true)

	if !strings.Contains(secure, "return 301 https://panel.example.com$request_uri;") {
		t.Errorf("port 80 does not redirect to the certificate's address:\n%s", secure)
	}

	if !strings.Contains(secure,
		"ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;") {
		t.Errorf("the hostname does not present its own certificate:\n%s", secure)
	}

	// Two blocks proxy to the panel on the hostname's ports — 80 and 443 in the
	// plain shape, 443 alone once 80 redirects — plus the panel's own port.
	for name, want := range map[string]int{"plain": 3, "secure": 2} {
		vhost := plain
		if name == "secure" {
			vhost = secure
		}

		if got := strings.Count(vhost, "proxy_pass http://127.0.0.1:8088;\n"+
			"        proxy_http_version 1.1;"); got != want {
			t.Errorf("the %s shape proxies to the panel from %d blocks, wanted %d:\n%s",
				name, got, want, vhost)
		}
	}
}

// The ACME challenge has to be answered on 80 and on 443, or the hostname's
// certificate can never be issued or renewed.
//
// 443 as well as 80, because a CDN with "always use HTTPS" switched on redirects
// certbot's own check before it reaches this machine — Cloudflare does it by
// default. With the challenge on 80 alone it lands on the catch-all's 404, the
// certificate is never issued, and nothing on the machine can see why.
func TestTheHostnameAnswersTheACMEChallengeOnBothPorts(t *testing.T) {
	for _, secure := range []bool{false, true} {
		vhost := panelVhost("panel.example.com", 8443, 8088, "8.3", secure)

		for _, block := range strings.Split(vhost, "server {")[1:] {
			if strings.Contains(block, "listen 8443") {
				continue
			}

			if !strings.Contains(block, "location ^~ /.well-known/acme-challenge/") {
				t.Errorf("a hostname block does not answer the challenge (secure=%v):\n%s",
					secure, block)
			}
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

// The address the panel prints for itself names a port only when it has to.
//
// This is the one that matters outside the browser: it is what an MCP client is
// pointed at, what the AI access page shows, and what Settings links to. On a
// hostname it is the name alone, because the hostname's own vhost serves the
// panel on 80 and 443. On the bare IP it names the panel's port, because 80 and
// 443 there belong to the catch-all's 404.
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

	// https before the certificate is a real one as well: 443 answers either
	// way, and the address should not change under somebody when certbot
	// finally runs.
	if url := app.panelURL(); url != "https://panel.example.com" {
		t.Errorf("a hostname with no certificate gave %q", url)
	}

	db.SetSetting("panel_domain_tls", "1")

	if url := app.panelURL(); url != "https://panel.example.com" {
		t.Errorf("a hostname with a certificate gave %q", url)
	}

	// The panel's own port does not appear in a hostname's address at all: the
	// hostname is served on 80 and 443, and moving the port leaves it alone.
	db.SetSetting(panelPortSetting, "9443")

	if url := app.panelURL(); url != "https://panel.example.com" {
		t.Errorf("a moved panel changed its hostname address to %q", url)
	}

	// With no hostname it is the bare address, which is only ever the panel's
	// own port.
	db.ForgetSetting("panel_domain")

	if url := app.panelURL(); !strings.HasSuffix(url, ":9443") {
		t.Errorf("the bare address does not name the panel's port: %q", url)
	}

	// And 80 or 443 are refused wherever they come from: they are the ports the
	// sites are on, and the panel being off them is the whole point.
	db.SetSetting(panelPortSetting, "80")

	if port := app.panelPort(); port != config.DefaultPanelPort {
		t.Errorf("port 80 was accepted as the panel's own port: %d", port)
	}
}
