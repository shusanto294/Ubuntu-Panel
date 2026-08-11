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
