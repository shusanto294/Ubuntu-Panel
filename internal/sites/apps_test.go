package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

func nextcloud() *store.Site {
	return &store.Site{
		ID:           2,
		Domain:       "cloud.example.com",
		Type:         "nextcloud",
		RootPath:     "/var/www/cloud.example.com",
		WebDirectory: "/public",
		PHPVersion:   "8.3",
	}
}

// Nextcloud's clients find the server through these redirects, and its own
// setup check complains until they are there. A generic PHP vhost has none of
// them, which is a Nextcloud that logs in and then never syncs.
func TestNextcloudVhostServesWhatItsClientsLookFor(t *testing.T) {
	vhost := RenderVhost(nextcloud(), false, true)

	for _, needed := range []string{
		"location = /.well-known/carddav { return 301 /remote.php/dav/; }",
		"location = /.well-known/caldav  { return 301 /remote.php/dav/; }",
		"location /remote { return 301 /remote.php$request_uri; }",
		"fastcgi_param PATH_INFO $path_info;",
		"fastcgi_pass unix:/run/php/php8.3-fpm.sock;",
	} {
		if !strings.Contains(vhost, needed) {
			t.Errorf("the Nextcloud vhost is missing %q", needed)
		}
	}

	// Everybody's files are one bad location block away from being
	// downloadable.
	if !strings.Contains(vhost, "|templates|data)(?:$|/) { return 404; }") {
		t.Error("the Nextcloud vhost does not refuse its own data and config directories")
	}

	// The ACME challenge is served by every site on port 80; a `^~ /.well-known`
	// block that swallowed it would mean no certificate could ever be issued.
	if !strings.Contains(vhost, "location /.well-known/acme-challenge    { try_files $uri $uri/ =404; }") {
		t.Error("the Nextcloud vhost swallows the ACME challenge")
	}
}

// The data directory is beside the document root, never inside it.
func TestNextcloudKeepsItsDataOutOfTheDocumentRoot(t *testing.T) {
	site := nextcloud()

	if !strings.HasPrefix(site.DocumentRoot(), site.RootPath+"/") {
		t.Fatalf("document root %q is not below %q", site.DocumentRoot(), site.RootPath)
	}

	data := site.RootPath + "/data"

	if strings.HasPrefix(data, site.DocumentRoot()+"/") {
		t.Fatalf("the data directory %q is inside the document root %q", data, site.DocumentRoot())
	}
}

// n8n's editor talks to itself over a websocket, and OpenClaw's gateway
// streams. A proxy without the upgrade headers is an editor that says
// "connection lost" on a site that is running perfectly.
func TestManagedNodeAppsAreProxiedWithUpgradeHeaders(t *testing.T) {
	for _, kind := range []string{"n8n", "openclaw"} {
		site := &store.Site{
			ID: 3, Domain: kind + ".example.com", Type: kind,
			RootPath: "/var/www/" + kind + ".example.com", AppPort: 3001,
		}

		vhost := RenderVhost(site, false, true)

		for _, needed := range []string{
			"upstream " + site.ServiceName() + "_upstream {",
			"server 127.0.0.1:3001;",
			"proxy_set_header Upgrade $http_upgrade;",
			"proxy_set_header X-Forwarded-Proto $scheme;",
		} {
			if !strings.Contains(vhost, needed) {
				t.Errorf("the %s vhost is missing %q", kind, needed)
			}
		}
	}
}

// OpenClaw takes its port from --port before it looks at the environment, and
// falls back to 18789 — which nginx does not proxy to. A gateway on the wrong
// port is a 502 on a service that is running perfectly.
func TestOpenClawUnitCarriesThePortOnTheCommandLine(t *testing.T) {
	site := &store.Site{
		ID: 5, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3007,
		StartCommand: "openclaw gateway",
	}

	unit := RenderUnit(site)

	if !strings.Contains(unit, "openclaw gateway --port 3007") {
		t.Errorf("the unit does not pass the port on the command line:\n%s", unit)
	}

	// An operator who put the flag there themselves does not get a second one.
	site.StartCommand = "openclaw gateway --port 4000"

	if strings.Count(RenderUnit(site), "--port") != 1 {
		t.Error("the panel added a second --port to a command that already had one")
	}
}

// An application that took a port of its own is followed rather than argued
// with — but only when there is one port to follow.
func TestTheAnsweringPortIsReadFromTheListeningSockets(t *testing.T) {
	// The same port on both address families is one port.
	both := `LISTEN 0 511 127.0.0.1:18789 0.0.0.0:* users:(("node",pid=1234,fd=21))
LISTEN 0 511    [::1]:18789     [::]:* users:(("node",pid=1234,fd=22))`

	if got := answeringPort(both); got != 18789 {
		t.Errorf("answeringPort = %d, want 18789", got)
	}

	// Two different ports is not a choice the panel should make silently.
	two := `LISTEN 0 511 127.0.0.1:18789 0.0.0.0:* users:(("node",pid=1234,fd=21))
LISTEN 0 511 127.0.0.1:9229  0.0.0.0:* users:(("node",pid=1234,fd=22))`

	if got := answeringPort(two); got != 0 {
		t.Errorf("answeringPort picked %d out of two candidates", got)
	}

	for _, nothing := range []string{
		"", "the process has no listening socket at all", "the service has no running process",
	} {
		if got := answeringPort(nothing); got != 0 {
			t.Errorf("answeringPort(%q) = %d, want 0", nothing, got)
		}
	}
}

// The gateway compares browser origins literally and ships knowing about
// localhost only, so the dashboard is refused on the domain it is published on
// until the site's own origin is in the list — scheme included, aliases too.
func TestOpenClawAllowsTheOriginsItsDashboardIsServedFrom(t *testing.T) {
	site := &store.Site{
		ID: 7, Domain: "agent.example.com", Type: "openclaw",
		Aliases: []string{"www.agent.example.com"},
		SSL:     true,
	}

	origins := openClawOrigins(site)

	if len(origins) != 2 || origins[0] != "https://agent.example.com" ||
		origins[1] != "https://www.agent.example.com" {
		t.Fatalf("origins = %v", origins)
	}

	if got := originsJSON(origins); got !=
		`["https://agent.example.com","https://www.agent.example.com"]` {
		t.Errorf("originsJSON = %s", got)
	}

	// Before the certificate is issued the dashboard really is on http, and an
	// https origin would be the one thing that does not match.
	site.SSL = false

	if got := openClawOrigins(site)[0]; got != "http://agent.example.com" {
		t.Errorf("origin without a certificate = %s, want http://agent.example.com", got)
	}
}

// A configuration OpenClaw objects to is not something restarting fixes: it
// exits 78 ten seconds after every start, and the unit that relaunches it
// anyway burned a core for three minutes on the first site that hit this.
func TestOpenClawUnitStopsRestartingOnAConfigurationError(t *testing.T) {
	site := &store.Site{
		ID: 6, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3009,
		StartCommand: "openclaw gateway",
	}

	if !strings.Contains(RenderUnit(site), "RestartPreventExitStatus=78") {
		t.Error("the OpenClaw unit restarts for ever on a configuration error")
	}

	// Everything else still restarts on anything: a Node app that exits 78 is
	// exiting for its own reasons, not sysexits' ones.
	site.Type = "nodejs"

	if strings.Contains(RenderUnit(site), "RestartPreventExitStatus") {
		t.Error("a plain Node app was given OpenClaw's exit-code rule")
	}
}

// Every proxied type needs something to run, and every managed type needs the
// deployment to know what to install. A new entry that forgets one of those
// fails here rather than halfway through a deployment.
func TestSiteTypeTableIsSelfConsistent(t *testing.T) {
	for _, kind := range config.SiteTypes {
		if kind.Proxied && kind.Start == "" {
			t.Errorf("%s is proxied but has no start command", kind.Key)
		}

		if kind.Description == "" {
			t.Errorf("%s has no description for the create form", kind.Key)
		}

		if kind.Runtime != "node" && kind.NodeMinimum != 0 {
			t.Errorf("%s is not a Node.js type but names a minimum Node version", kind.Key)
		}
	}
}

// The managed applications are the ones the panel installs itself, so a
// repository would be deployed over them.
func TestManagedTypesAreTheOnesThePanelInstalls(t *testing.T) {
	managed := map[string]bool{}

	for _, kind := range config.SiteTypes {
		if kind.Managed {
			managed[kind.Key] = true
		}
	}

	for _, key := range []string{"wordpress", "nextcloud", "n8n", "openclaw"} {
		if !managed[key] {
			t.Errorf("%s should be a managed application", key)
		}
	}

	for _, key := range []string{"php", "static", "nodejs", "nextjs", "laravel"} {
		if managed[key] {
			t.Errorf("%s is deployed from a repository and should not be managed", key)
		}
	}
}

// The gateway has no login of its own and its dashboard asks for a token, so
// nginx authenticates instead and tells the gateway who it let through. The
// header is set rather than forwarded: a visitor must not be able to name
// themselves.
func TestOpenClawIsAuthenticatedByNginx(t *testing.T) {
	site := &store.Site{
		ID: 8, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3011, SSL: true,
	}

	vhost := RenderVhost(site, true, true)

	for _, needed := range []string{
		"auth_basic \"agent.example.com\";",
		"auth_basic_user_file /etc/nginx/panel-auth/agent.example.com;",
		"proxy_set_header X-Auth-Request-Email $remote_user;",
	} {
		if !strings.Contains(vhost, needed) {
			t.Errorf("the OpenClaw vhost is missing %q", needed)
		}
	}

	// The challenge has to stay reachable or the certificate can never be
	// renewed.
	if strings.Contains(vhost, "auth_basic") &&
		!strings.Contains(vhost, "location ^~ /.well-known/acme-challenge/") {
		t.Error("the ACME challenge is behind the login")
	}

	// Nothing else gets a password prompt it was never told about.
	site.Type = "n8n"

	if strings.Contains(RenderVhost(site, true, true), "auth_basic") {
		t.Error("an n8n site was put behind HTTP basic authentication")
	}
}
