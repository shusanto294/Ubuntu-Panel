package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
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

// ------------------------------------------------------------- page cache

// A site nobody turned the cache on for must render exactly the vhost it
// rendered before the feature existed: no zone named, nothing to declare in
// conf.d, no behaviour changed.
func TestVhostHasNoCacheUntilItIsAskedFor(t *testing.T) {
	vhost := RenderVhost(wordpress(), true, true)

	for _, directive := range []string{"fastcgi_cache", "$skip_cache", "X-Panel-Cache"} {
		if strings.Contains(vhost, directive) {
			t.Fatalf("a site with the cache off still emits %s", directive)
		}
	}
}

func TestCachedWordPressSkipsAnythingPersonal(t *testing.T) {
	site := wordpress()
	site.CacheEnabled = true
	site.CacheTTL = "30m"

	vhost := RenderVhost(site, true, true)

	if !strings.Contains(vhost, "fastcgi_cache "+CacheZone(site)+";") {
		t.Fatal("the cached vhost does not name its own zone")
	}

	if !strings.Contains(vhost, "fastcgi_cache_valid 200 301 302 30m;") {
		t.Fatal("the cached vhost does not use the lifetime the site was given")
	}

	// The whole risk of a page cache: one visitor's page served to the next.
	for _, guard := range []string{
		"if ($request_method = POST) { set $skip_cache 1; }",
		"wordpress_logged_in",
		"/wp-admin/",
		"fastcgi_cache_bypass $skip_cache;",
		"fastcgi_no_cache $skip_cache;",
	} {
		if !strings.Contains(vhost, guard) {
			t.Fatalf("the cached WordPress vhost is missing %q", guard)
		}
	}

	// add_header in a location drops everything inherited, so HSTS has to be
	// repeated beside the cache header or a cached site stops sending it.
	if strings.Count(vhost, "Strict-Transport-Security") < 2 {
		t.Fatal("the cached HTTPS vhost sends the cache header and loses HSTS")
	}
}

// nginx refuses a second proxy_cache_bypass in the same block, and it refuses
// the whole configuration — every site on the machine — rather than that one
// line. The WebSocket condition and the skip flag have to be one directive.
func TestCachedProxiedSiteKeepsOneBypassDirective(t *testing.T) {
	site := &store.Site{
		ID: 2, Domain: "app.example.com", Type: "nextjs",
		RootPath: "/var/www/app.example.com", AppPort: 3000,
		CacheEnabled: true, CacheTTL: "5m",
	}

	vhost := RenderVhost(site, true, true)

	// The TLS form has one block that serves the site; the other redirects.
	if count := strings.Count(vhost, "proxy_cache_bypass"); count != 1 {
		t.Fatalf("expected one proxy_cache_bypass in the serving block, got %d", count)
	}

	// The plain form serves from both 80 and the self-signed 443 block, so one
	// each and no more.
	if count := strings.Count(RenderVhost(site, false, true), "proxy_cache_bypass"); count != 2 {
		t.Fatalf("expected one proxy_cache_bypass per serving block, got %d", count)
	}

	if !strings.Contains(vhost, "proxy_cache_bypass $skip_cache $http_upgrade;") {
		t.Fatal("a cached proxied site no longer refuses to cache a WebSocket upgrade")
	}
}

// Static is nginx reading a file off disk already, and Nextcloud's own manual
// says not to cache it. Asking anyway must not produce a vhost that does.
func TestKindsThePanelWillNotCache(t *testing.T) {
	for _, kind := range []string{"static", "nextcloud"} {
		site := wordpress()
		site.Type = kind
		site.CacheEnabled = true

		if Cacheable(site) {
			t.Fatalf("%s is offered a page cache", kind)
		}

		if strings.Contains(RenderVhost(site, true, true), "fastcgi_cache ") {
			t.Fatalf("a %s site was given a page cache anyway", kind)
		}
	}
}

// The zone is declared in conf.d and named in the vhost. A name that does not
// match is a configuration nginx refuses outright.
func TestCacheConfigDeclaresTheZoneTheVhostNames(t *testing.T) {
	site := wordpress()
	site.CacheEnabled = true

	declaration := RenderCacheConfig(site)

	if !strings.Contains(declaration, "keys_zone="+CacheZone(site)+":") {
		t.Fatal("the conf.d file does not declare the zone the vhost names")
	}

	if !strings.Contains(declaration, "fastcgi_cache_path "+CacheDir(site)+" ") {
		t.Fatal("the zone is not backed by the site's own cache directory")
	}

	// A domain is not a name nginx takes for a zone.
	if strings.Contains(CacheZone(site), ".") {
		t.Fatalf("the zone name %q is not one nginx will accept", CacheZone(site))
	}
}

func TestCacheDefaultsToTwelveHours(t *testing.T) {
	site := wordpress()

	if CacheTTLOf(site) != "12h" {
		t.Fatalf("a site with no lifetime set caches for %s", CacheTTLOf(site))
	}

	// A value that is no longer offered falls back rather than reaching nginx.
	site.CacheTTL = "3 hours"

	if CacheTTLOf(site) != DefaultCacheTTL {
		t.Fatal("a lifetime nginx would refuse was passed through to the vhost")
	}
}

// Every site on the machine shares one Redis. Without a prefix the second
// WordPress install reads the first one's cached options.
func TestRedisKeysAreNamespacedPerSite(t *testing.T) {
	one, two := wordpress(), wordpress()
	two.Domain = "other.example.com"

	if RedisPrefix(one) == RedisPrefix(two) {
		t.Fatal("two sites share one Redis namespace")
	}

	if strings.Contains(RedisPrefix(one), ".") {
		t.Fatalf("the prefix %q carries the domain's dots into a Redis key", RedisPrefix(one))
	}
}

// The panel wires WordPress into Redis because WordPress has a documented
// drop-in for it. Editing somebody's Laravel .env or Node source to do the
// same is how a site boots into a stack trace.
func TestRedisIsOfferedOnlyWherePanelCanWireIt(t *testing.T) {
	site := wordpress()

	if !RedisSupported(site) {
		t.Fatal("WordPress is not offered the object cache")
	}

	for _, kind := range []string{"laravel", "nextjs", "static", "php"} {
		site.Type = kind

		if RedisSupported(site) {
			t.Fatalf("a %s site is offered an object cache the panel cannot wire", kind)
		}
	}
}

// nginx reads `12h`; Redis and the object-cache plugin take a number of
// seconds. One menu, two forms of the same answer.
func TestCacheLifetimeHasBothForms(t *testing.T) {
	cases := map[string]int{"1m": 60, "1h": 3600, "12h": 43200, "1d": 86400}

	for ttl, want := range cases {
		if got := CacheSeconds(ttl); got != want {
			t.Errorf("CacheSeconds(%q) = %d, want %d", ttl, got, want)
		}
	}

	// Anything unrecognised falls back rather than reaching wp-config.php as a
	// zero, which the plugin reads as "never expire".
	if CacheSeconds("half an hour") != CacheSeconds(DefaultCacheTTL) {
		t.Fatal("an unknown lifetime does not fall back to the default")
	}

	// Forever is the one entry with no seconds: zero is how the object-cache
	// plugin spells "never expire".
	for _, entry := range CacheTTLs {
		if entry.Seconds <= 0 && entry.Value != "forever" {
			t.Fatalf("%s has no seconds against it", entry.Value)
		}
	}

	if CacheSeconds("forever") != 0 {
		t.Fatal("forever would reach wp-config.php as a real expiry")
	}
}

// nginx has no word for never, and `inactive` deletes a cached page nothing
// has asked for in that long whatever its lifetime — so forever meant an hour
// until both of them were dealt with.
func TestForeverOutlastsBothOfNginxsClocks(t *testing.T) {
	site := wordpress()
	site.CacheEnabled = true
	site.CacheTTL = "forever"

	if !strings.Contains(RenderVhost(site, true, true), "fastcgi_cache_valid 200 301 302 365d;") {
		t.Fatal("forever did not become a lifetime nginx understands")
	}

	if !strings.Contains(RenderCacheConfig(site), "inactive=365d") {
		t.Fatal("a page nobody asked for in an hour is still deleted under forever")
	}

	// A day-long lifetime had the same problem in smaller form.
	site.CacheTTL = "1d"

	if !strings.Contains(RenderCacheConfig(site), "inactive=1d") {
		t.Fatal("inactive does not follow the lifetime")
	}

	// Short lifetimes keep the hour floor: there is no point evicting a page
	// five minutes after its own expiry.
	site.CacheTTL = "5m"

	if !strings.Contains(RenderCacheConfig(site), "inactive=60m") {
		t.Fatal("a short lifetime lost the hour floor on inactive")
	}
}

// The map every proxied vhost now leans on, in the file that defines it.
//
// A vhost naming a variable nothing declares is one nginx refuses to load, and
// nginx refuses the whole configuration rather than that one file — so the
// missing half of this pair would take every site on the machine down.
func TestTheSharedConfigDeclaresTheForwardedProtoMap(t *testing.T) {
	if !strings.Contains(helperConfig, "$panel_proto") {
		t.Fatal("the shared config does not define $panel_proto")
	}

	if !strings.Contains(helperConfig, "map $http_x_forwarded_proto $panel_proto {") {
		t.Errorf("the map is not keyed on the forwarded header:\n%s", helperConfig)
	}
}
