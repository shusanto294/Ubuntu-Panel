package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

func wordpress() *store.Site {
	return &store.Site{
		ID:           1,
		Domain:       "example.com",
		Aliases:      []string{"www.example.com"},
		Type:         "wordpress",
		RootPath:     "/var/www/example.com",
		WebDirectory: "",
		PHPVersion:   "8.3",
	}
}

// A vhost naming a certificate that is not there is a vhost nginx will not
// load, and nginx refuses the whole configuration rather than that one file —
// so the plain form has to name the self-signed pair and nothing else.
func TestPlainVhostNamesOnlyTheSelfSignedCertificate(t *testing.T) {
	vhost := RenderVhost(wordpress(), false, true)

	if strings.Contains(vhost, "/etc/letsencrypt/live/") {
		t.Fatal("the plain vhost names a Let's Encrypt certificate that has not been issued yet")
	}

	if !strings.Contains(vhost, DefaultCert+".crt") {
		t.Fatal("the plain vhost does not fall back to the self-signed pair")
	}

	// Both ports answer from the first deployment: without the 443 block, HTTPS
	// for a site that works on 80 lands on the catch-all and 404s.
	if !strings.Contains(vhost, "listen 80;") || !strings.Contains(vhost, "listen 443 ssl;") {
		t.Fatal("the plain vhost does not answer on both ports")
	}

	if strings.Contains(vhost, "return 301 https://") {
		t.Fatal("the plain vhost redirects to a certificate it does not have")
	}
}

func TestTLSVhostRedirectsAndNamesTheIssuedCertificate(t *testing.T) {
	vhost := RenderVhost(wordpress(), true, true)

	if !strings.Contains(vhost, "/etc/letsencrypt/live/example.com/fullchain.pem") {
		t.Fatal("the TLS vhost does not name the issued certificate")
	}

	if !strings.Contains(vhost, "return 301 https://$host$request_uri;") {
		t.Fatal("the TLS vhost does not redirect HTTP")
	}
}

func TestVhostServesEveryHostname(t *testing.T) {
	vhost := RenderVhost(wordpress(), false, false)

	if !strings.Contains(vhost, "server_name example.com www.example.com;") {
		t.Fatalf("aliases are missing from server_name:\n%s", vhost)
	}
}

// `http2 on;` is not a warning on nginx older than 1.25.1: the configuration
// fails to load, for every site on the box.
func TestHTTP2DirectiveOnlyOnNginxThatUnderstandsIt(t *testing.T) {
	cases := map[string]bool{
		"nginx version: nginx/1.24.0 (Ubuntu)": false,
		"nginx version: nginx/1.25.0":          false,
		"nginx version: nginx/1.25.1":          true,
		"nginx version: nginx/1.26.2":          true,
		"nginx version: nginx/2.0.0":           true,
		"":                                     false,
		"something else entirely":              false,
	}

	for output, want := range cases {
		if got := SupportsHTTP2Directive(output); got != want {
			t.Errorf("SupportsHTTP2Directive(%q) = %v, want %v", output, got, want)
		}
	}
}

func TestProxiedSiteGetsAnUpstreamOutsideTheServerBlock(t *testing.T) {
	site := &store.Site{
		Domain:   "app.example.com",
		Type:     "nextjs",
		RootPath: "/var/www/app.example.com",
		AppPort:  3001,
	}

	vhost := RenderVhost(site, false, true)

	upstream := "upstream ubuntu-panel-app-example-com_upstream {"

	if !strings.HasPrefix(strings.SplitN(vhost, "\n", 2)[1], upstream) &&
		!strings.Contains(vhost, upstream) {
		t.Fatalf("no upstream block:\n%s", vhost)
	}

	if strings.Index(vhost, upstream) > strings.Index(vhost, "server {") {
		t.Fatal("the upstream is inside the server block, which nginx will not load")
	}

	if !strings.Contains(vhost, "server 127.0.0.1:3001;") {
		t.Fatal("the upstream does not point at the app's port")
	}
}

func TestDocumentRootJoinsWithoutDoubleSlashes(t *testing.T) {
	site := &store.Site{RootPath: "/var/www/example.com/", WebDirectory: "/public"}

	if root := site.DocumentRoot(); root != "/var/www/example.com/public" {
		t.Fatalf("document root is %q", root)
	}
}

// nginx allows exactly one default_server per address, and the panel's own
// vhost holds port 80 so the panel answers on the server's bare IP. A
// catch-all that claims it too is not a warning: nginx refuses the whole
// configuration, for every site on the box and for the panel with them.
func TestCatchAllYieldsPortEightyWhenSomethingElseHasIt(t *testing.T) {
	shared := catchAll(true)

	if strings.Contains(shared, "listen 80 default_server") {
		t.Fatalf("the catch-all claimed a port 80 default server that is already taken:\n%s", shared)
	}

	if !strings.Contains(shared, "listen 443 ssl default_server") {
		t.Fatal("the catch-all gave up the 443 slot as well")
	}

	if !strings.Contains(shared, "No site is configured for this hostname.") {
		t.Fatal("the 443 half lost its body")
	}
}

// With nothing else holding it — a machine where the panel's own vhost has not
// been written yet — the catch-all takes both, so an unknown hostname gets a
// 404 rather than whichever site happens to be first.
func TestCatchAllTakesBothSlotsWhenTheyAreFree(t *testing.T) {
	shared := catchAll(false)

	if !strings.Contains(shared, "listen 80 default_server") ||
		!strings.Contains(shared, "listen 443 ssl default_server") {
		t.Fatalf("the catch-all did not claim both free slots:\n%s", shared)
	}

	// The ACME webroot has to stay reachable on 80, or no certificate can ever
	// be issued for a hostname that has no site yet.
	if !strings.Contains(shared, "/.well-known/acme-challenge/") {
		t.Fatal("the port 80 half does not serve the ACME challenge")
	}
}

// The location that serves phpMyAdmin uses `root` with a prefix that matches
// the last path segment, so /phpmyadmin/x resolves to
// /usr/share/ubuntu-panel/phpmyadmin/x. An `alias` here — the usual way this
// is written — silently breaks try_files and SCRIPT_FILENAME.
func TestPhpMyAdminIsServedByRootRatherThanAlias(t *testing.T) {
	block := PhpMyAdminLocation("8.3", "/internal/phpmyadmin-gate", 8088)

	if strings.Contains(block, "alias") {
		t.Error("the block uses alias, which breaks $document_root under it")
	}

	for _, want := range []string{
		"location ^~ /phpmyadmin {",
		"root /usr/share/ubuntu-panel;",
		"fastcgi_pass unix:/run/php/php8.3-fpm.sock;",
		"fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the block is missing %q:\n%s", want, block)
		}
	}
}

// $panel_https is defined in a conf.d file the panel writes when it configures
// nginx, and this block also goes into the vhost the installer writes before
// any of that exists. A vhost naming an undefined variable is one nginx
// refuses to load — and it is the vhost serving the panel.
func TestPhpMyAdminBlockNamesNoPanelVariables(t *testing.T) {
	if strings.Contains(PhpMyAdminLocation("", "", 8088), "$panel_https") {
		t.Error("the block references $panel_https, which is not defined on a fresh install")
	}
}

// An empty version still has to produce a socket path, because the vhost is
// written on machines where no PHP default has been chosen yet.
func TestPhpMyAdminFallsBackToTheDefaultPHP(t *testing.T) {
	if !strings.Contains(PhpMyAdminLocation("", "", 8088), "php8.3-fpm.sock") {
		t.Error("no PHP version produced no socket")
	}
}

// The auto-login server phpMyAdmin is configured with signs itself in as a
// MariaDB administrator with no prompt. The subrequest is the only thing
// standing in front of it, so the location has to carry it — and the internal
// location that answers it has to be there too, or nginx refuses the file.
func TestPhpMyAdminIsGuardedByTheSubrequest(t *testing.T) {
	block := PhpMyAdminLocation("8.3", "/internal/phpmyadmin-gate", 8088)

	for _, want := range []string{
		"auth_request /internal/phpmyadmin-gate;",
		"location = /internal/phpmyadmin-gate {",
		"internal;",
		"proxy_pass http://127.0.0.1:8088;",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the guarded block is missing %q:\n%s", want, block)
		}
	}
}

// And no subrequest at all when the panel could not install one, so a machine
// whose nginx has no auth_request module still serves phpMyAdmin — with its
// own login form, and without the auto-login server the panel then declines to
// write.
func TestPhpMyAdminCanBeServedWithoutTheGuard(t *testing.T) {
	block := PhpMyAdminLocation("8.3", "", 8088)

	if strings.Contains(block, "auth_request") {
		t.Errorf("an unguarded block still names auth_request:\n%s", block)
	}

	if !strings.Contains(block, "location ^~ /phpmyadmin {") {
		t.Errorf("the location itself went missing:\n%s", block)
	}
}
