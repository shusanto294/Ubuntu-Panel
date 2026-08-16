package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
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

// n8n's editor talks to itself over a websocket, and a proxied application
// streams. A proxy without the upgrade headers is an editor that says
// "connection lost" on a site that is running perfectly.
func TestManagedNodeAppsAreProxiedWithUpgradeHeaders(t *testing.T) {
	for _, kind := range []string{"n8n", "nextjs"} {
		site := &store.Site{
			ID: 3, Domain: kind + ".example.com", Type: kind,
			RootPath: "/var/www/" + kind + ".example.com", AppPort: 3001,
		}

		vhost := RenderVhost(site, false, true)

		for _, needed := range []string{
			"upstream " + site.ServiceName() + "_upstream {",
			"server 127.0.0.1:3001;",
			"proxy_set_header Upgrade $http_upgrade;",
			// The visitor's scheme, not this nginx's: behind a proxy that ends
			// TLS in front of the machine, $scheme is http and an application
			// told that builds http links into an https page.
			"proxy_set_header X-Forwarded-Proto $panel_proto;",
		} {
			if !strings.Contains(vhost, needed) {
				t.Errorf("the %s vhost is missing %q", kind, needed)
			}
		}
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

	for _, key := range []string{"wordpress", "nextcloud", "n8n"} {
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
