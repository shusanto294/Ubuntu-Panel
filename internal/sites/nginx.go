// Package sites deploys sites: the nginx vhost, the per-type recipe, and the
// lifecycle operations the Sites pages trigger.
package sites

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

const (
	// ACMEWebroot is the shared webroot for HTTP-01 challenges, served by every
	// site on port 80.
	ACMEWebroot = "/var/www/letsencrypt"

	// DefaultCert is self-signed, and only ever presented to requests for
	// hostnames nothing is configured for.
	DefaultCert = "/etc/nginx/panel-default"

	// PhpMyAdminParent is what the panel unpacks phpMyAdmin under, and
	// PhpMyAdminPath is where the panel's own origin serves it from.
	//
	// The parent rather than the directory itself, because the location below
	// uses `root` and not `alias`: with the URI prefix and the last path
	// segment spelled the same, nginx maps /phpmyadmin/x to
	// /usr/share/ubuntu-panel/phpmyadmin/x on its own. `alias` would do the
	// same job and take try_files, $document_root and PATH_INFO with it — the
	// standard way this configuration is got wrong.
	PhpMyAdminParent = "/usr/share/ubuntu-panel"
	PhpMyAdminPath   = "/phpmyadmin"
)

// PhpMyAdminLocation serves phpMyAdmin from the panel's own origin.
//
// On the panel's origin rather than a vhost of its own: phpMyAdmin is a login
// form for every database on the machine, and the panel's hostname is the one
// with a certificate on it. Given a vhost of its own on plain HTTP, the first
// thing through it would be a MySQL password in clear text.
//
// Emitted whether or not phpMyAdmin is installed. A location pointing at a
// directory that is not there is a 404, which is what it should be; making the
// block conditional would mean the panel had to rewrite its own vhost the
// moment somebody installed the app from Apps.
//
// Nothing here references $panel_https, deliberately. That map lives in a
// conf.d file the panel writes when it configures nginx, and this block also
// goes into the vhost the installer writes before any of that has happened —
// a vhost naming an undefined variable is one nginx refuses to load, and the
// thing it would refuse to load is the panel itself. fastcgi-php.conf sets
// HTTPS from $https, which is right whenever TLS ends at this nginx.
func PhpMyAdminLocation(php string, gate string, panelPort int) string {
	if php == "" {
		php = "8.3"
	}

	// The guard, and the internal location that answers it.
	//
	// phpMyAdmin is configured with a server that signs itself in as a MariaDB
	// administrator with no password prompt. What keeps that from being every
	// database on the machine one URL away is this: nginx asks the panel who
	// is calling, and serves nothing under /phpmyadmin until the panel says it
	// is somebody signed in.
	//
	// Empty when no gate is wanted, which is how a machine whose nginx has no
	// auth_request module still gets phpMyAdmin — with its own login form, and
	// without the auto-login server, which the panel then does not write.
	guard, gateLocation := "", ""

	if gate != "" {
		guard = fmt.Sprintf("        auth_request %s;\n", gate)

		gateLocation = fmt.Sprintf(
			"    location = %s {\n"+
				"        internal;\n"+
				"        proxy_pass http://127.0.0.1:%d;\n"+
				"        proxy_pass_request_body off;\n"+
				"        proxy_set_header Content-Length \"\";\n"+
				"        proxy_set_header Host $host;\n"+
				"        proxy_set_header X-Forwarded-Proto $scheme;\n"+
				"    }\n\n", gate, panelPort)
	}

	return fmt.Sprintf(
		"%[4]s"+
			"    # phpMyAdmin, when it is installed. See PhpMyAdminLocation in nginx.go.\n"+
			"    location ^~ %[2]s {\n"+
			"%[5]s"+
			"        root %[1]s;\n"+
			"        index index.php;\n"+
			"        try_files $uri $uri/ =404;\n"+
			"\n"+
			"        location ~ ^%[2]s/.+\\.php$ {\n"+
			"            root %[1]s;\n"+
			"            include snippets/fastcgi-php.conf;\n"+
			"            fastcgi_pass unix:/run/php/php%[3]s-fpm.sock;\n"+
			"            fastcgi_read_timeout 300;\n"+
			"            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n"+
			"        }\n"+
			"\n"+
			"        # Nothing under here is meant to be fetched directly.\n"+
			"        location ~ ^%[2]s/(libraries|templates|setup)/ {\n"+
			"            deny all;\n"+
			"        }\n"+
			"    }\n",
		PhpMyAdminParent, PhpMyAdminPath, php, gateLocation, guard)
}

// AvailablePath, EnabledPath, CertificatePath and UnitPath are where a site's
// files live.
func AvailablePath(site *store.Site) string { return "/etc/nginx/sites-available/" + site.Domain }
func EnabledPath(site *store.Site) string   { return "/etc/nginx/sites-enabled/" + site.Domain }
func CertificatePath(site *store.Site) string {
	return "/etc/letsencrypt/live/" + site.Domain
}
func UnitPath(site *store.Site) string {
	return "/etc/systemd/system/" + site.ServiceName() + ".service"
}

// PasswordFilePath is the htpasswd file for a site nginx authenticates on
// behalf of the application behind it.
func PasswordFilePath(site *store.Site) string {
	return "/etc/nginx/panel-auth/" + site.Domain
}

// ------------------------------------------------------------- page cache

// CacheTTLs are the lifetimes the Cache tab offers, longest last.
//
// A menu rather than a box: the value goes into nginx as a time, and `10 min`
// or `1 hr` is a configuration nginx refuses — for every site on the machine,
// not just this one.
var CacheTTLs = []struct {
	Value string
	Label string
}{
	{"1m", "1 minute"},
	{"5m", "5 minutes"},
	{"10m", "10 minutes"},
	{"30m", "30 minutes"},
	{"1h", "1 hour"},
	{"6h", "6 hours"},
	{"12h", "12 hours"},
	{"1d", "1 day"},
}

// DefaultCacheTTL is what a site that has never been configured caches for.
// Ten minutes is long enough to take the repeat visitors off PHP and short
// enough that a published change appears while somebody is still looking.
const DefaultCacheTTL = "12h"

// ValidCacheTTL reports whether a lifetime is one of the offered ones.
func ValidCacheTTL(ttl string) bool {
	for _, entry := range CacheTTLs {
		if entry.Value == ttl {
			return true
		}
	}

	return false
}

// CacheTTLOf is the site's lifetime, with the default for a site whose stored
// value is empty or no longer offered.
func CacheTTLOf(site *store.Site) string {
	if ValidCacheTTL(site.CacheTTL) {
		return site.CacheTTL
	}

	return DefaultCacheTTL
}

// Cacheable reports whether the panel will put a page cache in front of this
// kind of site.
//
// Static sites are already nginx reading a file off disk, so a cache in front
// of that is a second copy of the same file. Nextcloud is every page personal
// to whoever asked for it, and its own manual says not to. The rest — PHP
// applications and proxied ones — are a process rendering a page per request,
// which is the thing worth not doing twice.
func Cacheable(site *store.Site) bool {
	switch site.Type {
	case "static", "nextcloud":
		return false
	}

	return true
}

// CacheActive is whether this site's vhost should carry cache directives at
// all: asked for, and of a kind the panel will cache.
func CacheActive(site *store.Site) bool {
	return site.CacheEnabled && Cacheable(site)
}

// CacheDir is where the cached responses are written, and CacheConfigPath is
// the conf.d file that declares the zone.
//
// The zone has to be declared in nginx's http context, which a vhost is not
// in — so every cached site owns one small file in conf.d beside its vhost.
func CacheDir(site *store.Site) string {
	return "/var/cache/ubuntu-panel/" + site.Domain
}

func CacheConfigPath(site *store.Site) string {
	return "/etc/nginx/conf.d/panel-cache-" + site.Domain + ".conf"
}

// CacheZone is the shared-memory zone's name: one per site, and a name nginx
// will take, which a domain with dots and dashes in it is not.
func CacheZone(site *store.Site) string {
	return "panelcache_" + sanitiseIdentifier(site.Domain)
}

// RenderCacheConfig is that conf.d file.
//
// max_size caps what one site may take on disk and inactive throws away what
// nothing has asked for in an hour, so a cache nobody purges cannot fill the
// disk the sites are served from.
func RenderCacheConfig(site *store.Site) string {
	directive := "fastcgi_cache_path"
	if site.IsProxied() {
		directive = "proxy_cache_path"
	}

	return fmt.Sprintf(
		"# Managed by %s — page cache for %s.\n"+
			"# The zone has to live in the http context; the vhost names it.\n"+
			"%s %s levels=1:2 keys_zone=%s:10m inactive=60m max_size=256m;\n",
		config.Name, site.Domain, directive, CacheDir(site), CacheZone(site))
}

// cacheSkipRules decide, per request, whether this one may be served from the
// cache or stored in it.
//
// The whole risk of a page cache is here. A logged-in visitor must never be
// handed a page built for somebody else, and a form must never be answered
// from a copy — so anything carrying a session, anything posted, and every
// path an application uses for its own back end is left to go through.
func cacheSkipRules(site *store.Site) string {
	var out strings.Builder

	out.WriteString(
		"\n" +
			"    # Page cache. Anything that could be personal skips it — see\n" +
			"    # cacheSkipRules in nginx.go.\n" +
			"    set $skip_cache 0;\n" +
			"\n" +
			"    if ($request_method = POST) { set $skip_cache 1; }\n" +
			"    if ($query_string != \"\") { set $skip_cache 1; }\n" +
			"    if ($http_authorization != \"\") { set $skip_cache 1; }\n")

	if site.Type == "wordpress" {
		out.WriteString(
			"    if ($request_uri ~* \"/wp-admin/|/wp-json/|/xmlrpc\\.php|wp-.*\\.php" +
				"|/feed/|/cart|/checkout|/my-account|sitemap(_index)?\\.xml\") { set $skip_cache 1; }\n" +
				"    if ($http_cookie ~* \"comment_author|wordpress_[a-f0-9]+|wp-postpass" +
				"|wordpress_no_cache|wordpress_logged_in|woocommerce_items_in_cart" +
				"|woocommerce_cart_hash\") { set $skip_cache 1; }\n")

		return out.String()
	}

	// Everything else: a session cookie of any of the usual names is enough to
	// treat the request as somebody's own.
	out.WriteString(
		"    if ($http_cookie ~* \"PHPSESSID|laravel_session|remember_web|session|_token" +
			"|logged_in\") { set $skip_cache 1; }\n")

	return out.String()
}

// cacheDirectives are what goes inside the location that talks to the
// application.
//
// use_stale plus background_update is the part that earns the cache its
// keep: an expired page is served immediately and refreshed behind the
// visitor, and a PHP-FPM that has fallen over serves the last good copy
// instead of a 502. cache_lock stops a hundred simultaneous misses becoming a
// hundred simultaneous renders of the same page.
func cacheDirectives(site *store.Site, tls bool) string {
	prefix := "fastcgi"
	if site.IsProxied() {
		prefix = "proxy"
	}

	// The proxied form carries the WebSocket condition too: it replaces the
	// plain proxy_cache_bypass rather than sitting beside it.
	upgrade := ""
	if site.IsProxied() {
		upgrade = " $http_upgrade"
	}

	var out strings.Builder

	fmt.Fprintf(&out,
		"\n"+
			"        # Page cache, %s. X-Panel-Cache says HIT, MISS or BYPASS.\n"+
			"        %[2]s_cache %[3]s;\n"+
			"        %[2]s_cache_key \"$scheme$request_method$host$request_uri\";\n"+
			"        %[2]s_cache_valid 200 301 302 %[4]s;\n"+
			"        %[2]s_cache_bypass $skip_cache%[5]s;\n"+
			"        %[2]s_no_cache $skip_cache;\n"+
			"        %[2]s_cache_use_stale error timeout updating invalid_header http_500 http_503;\n"+
			"        %[2]s_cache_background_update on;\n"+
			"        %[2]s_cache_lock on;\n"+
			"        add_header X-Panel-Cache $upstream_cache_status always;\n",
		CacheTTLOf(site), prefix, CacheZone(site), CacheTTLOf(site), upgrade)

	// add_header in a location replaces every add_header inherited from the
	// server block, so the one the server block sets has to be repeated here
	// or a cached site quietly stops sending HSTS.
	if tls {
		out.WriteString(
			"        add_header Strict-Transport-Security \"max-age=31536000\" always;\n")
	}

	return out.String()
}

// SupportsHTTP2Directive reports whether this nginx takes `http2 on;` as a
// directive, which only 1.25.1 and later do.
//
// An unreadable version is treated as the older syntax: losing HTTP/2 costs a
// little speed; guessing wrong the other way costs the whole configuration,
// for every site on the box.
func SupportsHTTP2Directive(versionOutput string) bool {
	_, rest, found := strings.Cut(versionOutput, "nginx/")
	if !found {
		return false
	}

	parts := strings.SplitN(strings.Fields(rest)[0], ".", 3)
	if len(parts) < 3 {
		return false
	}

	number := func(text string) int {
		digits := strings.Builder{}

		for _, r := range text {
			if r < '0' || r > '9' {
				break
			}

			digits.WriteRune(r)
		}

		value, _ := strconv.Atoi(digits.String())

		return value
	}

	major, minor, patch := number(parts[0]), number(parts[1]), number(parts[2])

	if major != 1 {
		return major > 1
	}

	if minor != 25 {
		return minor > 25
	}

	return patch >= 1
}

// body is the directives that actually serve the site, shared by the HTTP and
// HTTPS forms so the two can never drift apart.
func body(out *strings.Builder, site *store.Site, tls bool) {
	root := site.DocumentRoot()

	// Off unless the site's Cache tab turned it on, and never on a kind of site
	// the panel will not cache.
	cached := CacheActive(site)

	if site.IsProxied() {
		fmt.Fprintf(out, "    charset utf-8;\n    client_max_body_size 128M;\n")

		if cached {
			out.WriteString(cacheSkipRules(site))
		}

		if site.Type == "nextjs" {
			fmt.Fprintf(out,
				"\n"+
					"    location /_next/static/ {\n"+
					"        alias %s/.next/static/;\n"+
					"        expires 365d;\n"+
					"        access_log off;\n"+
					"    }\n", site.RootPath)
		}

		// OpenClaw's dashboard is a login form for a gateway token, and the
		// gateway behind it will do the pairing dance with a browser before it
		// accepts a Control UI session. Neither is something to hand an
		// operator who asked the panel to install an application.
		//
		// So nginx does the authenticating — one browser login, remembered —
		// and tells the gateway who it let through. The gateway is configured
		// to trust that header from the loopback address it is proxied from,
		// and answers the dashboard without asking for anything.
		//
		// The header is set, never appended: a visitor who sends one of their
		// own must not be able to name themselves.
		authentication := ""
		if site.Type == "openclaw" {
			authentication = fmt.Sprintf(
				"        auth_basic \"%s\";\n"+
					"        auth_basic_user_file %s;\n"+
					"        proxy_set_header X-Auth-Request-Email $remote_user;\n",
				site.Domain, PasswordFilePath(site))
		}

		// A WebSocket upgrade must never be answered from a cache. Without
		// the cache that is all this line says; with it, the two conditions
		// are one directive, because nginx refuses a second one beside it.
		caching, bypass := "", "        proxy_cache_bypass $http_upgrade;\n"

		if cached {
			caching, bypass = cacheDirectives(site, tls), ""
		}

		fmt.Fprintf(out,
			"\n"+
				"    location / {\n"+
				"%s"+
				"        proxy_pass http://%s_upstream;\n"+
				"        proxy_http_version 1.1;\n"+
				"        proxy_set_header Upgrade $http_upgrade;\n"+
				"        proxy_set_header Connection \"upgrade\";\n"+
				"        proxy_set_header Host $host;\n"+
				"        proxy_set_header X-Real-IP $remote_addr;\n"+
				"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"+
				"        proxy_set_header X-Forwarded-Proto $scheme;\n"+
				"%s"+
				"        proxy_read_timeout 300;\n"+
				"%s"+
				"    }\n", authentication, site.ServiceName(), bypass, caching)

		return
	}

	if site.Type == "nextcloud" {
		nextcloudBody(out, site)

		return
	}

	if site.Type == "static" {
		fmt.Fprintf(out,
			"    root %s;\n"+
				"\n"+
				"    index index.html index.htm;\n"+
				"    charset utf-8;\n"+
				"\n"+
				"    location / {\n"+
				"        try_files $uri $uri/ $uri.html /index.html;\n"+
				"    }\n"+
				"\n"+
				"    location ~* \\.(?:css|js|jpg|jpeg|png|gif|ico|svg|woff2?)$ {\n"+
				"        expires 30d;\n"+
				"        access_log off;\n"+
				"    }\n"+
				"\n"+
				"    location ~ /\\.(?!well-known).* {\n"+
				"        deny all;\n"+
				"    }\n", root)

		return
	}

	fmt.Fprintf(out,
		"    root %s;\n"+
			"\n"+
			"    index index.php index.html;\n"+
			"    charset utf-8;\n"+
			"    client_max_body_size 128M;\n"+
			"\n"+
			"    location / {\n"+
			"        try_files $uri $uri/ /index.php?$query_string;\n"+
			"    }\n"+
			"\n"+
			"    location = /favicon.ico { access_log off; log_not_found off; }\n"+
			"    location = /robots.txt  { access_log off; log_not_found off; }\n", root)

	if site.Type == "wordpress" {
		out.WriteString(
			"\n" +
				"    # WordPress hardening\n" +
				"    location = /xmlrpc.php { deny all; access_log off; log_not_found off; }\n" +
				"    location ~* /(?:uploads|files)/.*\\.php$ { deny all; }\n" +
				"    location ~* \\.(?:css|js|jpg|jpeg|png|gif|ico|svg|webp|woff2?)$ {\n" +
				"        expires 30d;\n" +
				"        access_log off;\n" +
				"    }\n")
	}

	php := site.PHPVersion
	if php == "" {
		php = "8.3"
	}

	// The FastCGI overrides go after the snippet, never before it.
	//
	// `snippets/fastcgi-php.conf` pulls in `fastcgi.conf`, which sets the whole
	// standard parameter set — including SCRIPT_FILENAME and
	// `HTTPS $https if_not_empty`. nginx sends parameters in declaration order
	// and PHP keeps the last of any duplicate, so ours only win by coming
	// afterwards.
	if cached {
		out.WriteString(cacheSkipRules(site))
	}

	caching := ""
	if cached {
		caching = cacheDirectives(site, tls)
	}

	fmt.Fprintf(out,
		"\n"+
			"    location ~ \\.php$ {\n"+
			"        include snippets/fastcgi-php.conf;\n"+
			"        fastcgi_pass unix:/run/php/php%s-fpm.sock;\n"+
			"        fastcgi_read_timeout 300;\n"+
			"\n"+
			"        # Overrides last — see the note in nginx.go.\n"+
			"        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;\n"+
			"        fastcgi_param HTTPS $panel_https;\n"+
			phpValueParam(site)+
			caching+
			"    }\n"+
			"\n"+
			"    location ~ /\\.(?!well-known).* {\n"+
			"        deny all;\n"+
			"    }\n", php)
}

// nextcloudBody is Nextcloud's own configuration, which is not a variation on
// the generic PHP one.
//
// Nextcloud routes everything through index.php with PATH_INFO, refuses to
// serve if `/config` or `/data` are reachable, and its desktop and mobile
// clients discover the server through /.well-known redirects that have to
// exist before a login is possible. A generic `try_files $uri /index.php`
// vhost gets a Nextcloud that appears to work and then fails at sync, so this
// follows the configuration in Nextcloud's own administration manual.
func nextcloudBody(out *strings.Builder, site *store.Site) {
	php := site.PHPVersion
	if php == "" {
		php = "8.3"
	}

	fmt.Fprintf(out,
		"    root %s;\n"+
			"\n"+
			"    index index.php index.html /index.php$request_uri;\n"+
			"    charset utf-8;\n"+
			"\n"+
			"    # Big enough for the desktop client's uploads; Nextcloud chunks\n"+
			"    # anything larger than this itself.\n"+
			"    client_max_body_size 512M;\n"+
			"    client_body_timeout 300s;\n"+
			"    fastcgi_buffers 64 4K;\n"+
			"\n"+
			"    add_header Referrer-Policy \"no-referrer\" always;\n"+
			"    add_header X-Content-Type-Options \"nosniff\" always;\n"+
			"    add_header X-Frame-Options \"SAMEORIGIN\" always;\n"+
			"    add_header X-Permitted-Cross-Domain-Policies \"none\" always;\n"+
			"    add_header X-Robots-Tag \"noindex, nofollow\" always;\n"+
			"    fastcgi_hide_header X-Powered-By;\n"+
			"\n"+
			"    location = /robots.txt { allow all; log_not_found off; access_log off; }\n"+
			"\n"+
			"    # How the desktop and mobile clients find the server.\n"+
			"    location ^~ /.well-known {\n"+
			"        location = /.well-known/carddav { return 301 /remote.php/dav/; }\n"+
			"        location = /.well-known/caldav  { return 301 /remote.php/dav/; }\n"+
			"        location /.well-known/acme-challenge    { try_files $uri $uri/ =404; }\n"+
			"        location /.well-known/pki-validation    { try_files $uri $uri/ =404; }\n"+
			"        return 301 /index.php$request_uri;\n"+
			"    }\n"+
			"\n"+
			"    # Nothing below these is ever served directly.\n"+
			"    location ~ ^/(?:build|tests|config|lib|3rdparty|templates|data)(?:$|/) { return 404; }\n"+
			"    location ~ ^/(?:\\.|autotest|occ|issue|indie|db_|console)              { return 404; }\n"+
			"\n"+
			"    location ~ \\.php(?:$|/) {\n"+
			"        rewrite ^/(?!index|remote|public|cron|core\\/ajax\\/update|status|ocs\\/v[12]|"+
			"updater\\/.+|oc[ms]-provider\\/.+|.+\\/richdocumentscode(_arm64)?\\/proxy) "+
			"/index.php$request_uri;\n"+
			"\n"+
			"        fastcgi_split_path_info ^(.+?\\.php)(/.*)$;\n"+
			"        set $path_info $fastcgi_path_info;\n"+
			"        try_files $fastcgi_script_name =404;\n"+
			"\n"+
			"        include fastcgi_params;\n"+
			"        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n"+
			"        fastcgi_param PATH_INFO $path_info;\n"+
			"        fastcgi_param HTTPS $panel_https;\n"+
			"        fastcgi_param modHeadersAvailable true;\n"+
			"        fastcgi_param front_controller_active true;\n"+
			"\n"+
			"        fastcgi_pass unix:/run/php/php%s-fpm.sock;\n"+
			"        fastcgi_intercept_errors on;\n"+
			"        fastcgi_request_buffering off;\n"+
			"        fastcgi_read_timeout 3600;\n"+
			"        fastcgi_max_temp_file_size 0;\n"+
			"    }\n"+
			"\n"+
			"    location ~ \\.(?:css|js|mjs|svg|gif|ico|jpg|jpeg|png|webp|wasm|tflite|map|ogg|flac)$ {\n"+
			"        try_files $uri /index.php$request_uri;\n"+
			"        expires 6M;\n"+
			"        access_log off;\n"+
			"    }\n"+
			"\n"+
			"    location ~ \\.woff2?$ {\n"+
			"        try_files $uri /index.php$request_uri;\n"+
			"        expires 7d;\n"+
			"        access_log off;\n"+
			"    }\n"+
			"\n"+
			"    location /remote { return 301 /remote.php$request_uri; }\n"+
			"\n"+
			"    location / {\n"+
			"        try_files $uri $uri/ /index.php$request_uri;\n"+
			"    }\n",
		site.DocumentRoot(), php)
}

func acme(out *strings.Builder) {
	fmt.Fprintf(out,
		"    location ^~ /.well-known/acme-challenge/ {\n"+
			"        root %s;\n"+
			"        default_type \"text/plain\";\n"+
			"    }\n", ACMEWebroot)
}

// upstream is only needed by the proxied types, and must sit outside server {}.
func upstream(out *strings.Builder, site *store.Site) {
	if !site.IsProxied() {
		return
	}

	fmt.Fprintf(out,
		"upstream %s_upstream {\n"+
			"    server 127.0.0.1:%d;\n"+
			"    keepalive 32;\n"+
			"}\n\n", site.ServiceName(), site.AppPort)
}

// RenderVhost writes the site's nginx configuration.
//
// tls is only true once the certificate exists — nginx refuses to start if
// ssl_certificate names a file that is not there. http2 emits the `http2 on;`
// directive, which only nginx 1.25.1 and later understand: on anything older it
// is not a warning, the configuration fails to load.
func RenderVhost(site *store.Site, tls, http2 bool) string {
	var out strings.Builder

	names := strings.Join(site.Hostnames(), " ")

	scheme := "HTTP"
	if tls {
		scheme = "HTTPS, HTTP redirected"
	}

	fmt.Fprintf(&out, "# Managed by %s — %s site (%s)\n", config.Name, site.TypeLabel(), scheme)

	upstream(&out, site)

	if !tls {
		out.WriteString("server {\n")
		out.WriteString("    listen 80;\n    listen [::]:80;\n")
		fmt.Fprintf(&out, "    server_name %s;\n\n", names)
		fmt.Fprintf(&out, "    access_log /var/log/nginx/%s-access.log;\n", site.Domain)
		fmt.Fprintf(&out, "    error_log  /var/log/nginx/%s-error.log;\n\n", site.Domain)

		acme(&out)
		out.WriteString("\n")
		body(&out, site, tls)
		out.WriteString("}\n")

		// The same site on 443, with the self-signed pair, until a real
		// certificate exists.
		//
		// Without this the hostname has no 443 block at all, so HTTPS lands on
		// the catch-all and answers "No site is configured for this hostname."
		// — a 404 for a site that is up and serving perfectly on port 80. A CDN
		// in front makes it worse: it dials the origin on 443 by default, gets
		// that 404 or nothing, and shows the visitor an error page for a site
		// that works. No redirect from 80, because a warning page is not an
		// upgrade over the site itself; the redirect arrives with the real
		// certificate.
		out.WriteString("\nserver {\n")
		out.WriteString("    listen 443 ssl;\n    listen [::]:443 ssl;\n")

		if http2 {
			out.WriteString("    http2 on;\n")
		}

		fmt.Fprintf(&out, "    server_name %s;\n\n", names)
		fmt.Fprintf(&out,
			"    ssl_certificate     %s.crt;\n"+
				"    ssl_certificate_key %s.key;\n"+
				"    ssl_protocols TLSv1.2 TLSv1.3;\n\n", DefaultCert, DefaultCert)
		fmt.Fprintf(&out, "    access_log /var/log/nginx/%s-access.log;\n", site.Domain)
		fmt.Fprintf(&out, "    error_log  /var/log/nginx/%s-error.log;\n\n", site.Domain)

		body(&out, site, tls)
		out.WriteString("}\n")

		return out.String()
	}

	certs := CertificatePath(site)

	out.WriteString("server {\n")
	out.WriteString("    listen 80;\n    listen [::]:80;\n")
	fmt.Fprintf(&out, "    server_name %s;\n\n", names)

	acme(&out)

	out.WriteString(
		"\n" +
			"    location / {\n" +
			"        return 301 https://$host$request_uri;\n" +
			"    }\n" +
			"}\n\n")

	out.WriteString("server {\n")
	out.WriteString("    listen 443 ssl;\n    listen [::]:443 ssl;\n")

	if http2 {
		out.WriteString("    http2 on;\n")
	}

	fmt.Fprintf(&out, "    server_name %s;\n\n", names)
	fmt.Fprintf(&out,
		"    ssl_certificate     %s/fullchain.pem;\n"+
			"    ssl_certificate_key %s/privkey.pem;\n"+
			"    ssl_session_timeout 1d;\n"+
			"    ssl_session_cache shared:SSL:10m;\n"+
			"    ssl_protocols TLSv1.2 TLSv1.3;\n"+
			"    ssl_prefer_server_ciphers off;\n"+
			"    add_header Strict-Transport-Security \"max-age=31536000\" always;\n\n",
		certs, certs)

	fmt.Fprintf(&out, "    access_log /var/log/nginx/%s-access.log;\n", site.Domain)
	fmt.Fprintf(&out, "    error_log  /var/log/nginx/%s-error.log;\n\n", site.Domain)

	body(&out, site, tls)
	out.WriteString("}\n")

	return out.String()
}

// RenderUnit is the systemd unit a node-family app runs as.
func RenderUnit(site *store.Site) string {
	start := site.StartCommand

	// A managed application has a command of its own, and `npm run start` in a
	// directory with no package.json is a service that restarts for ever.
	if start == "" {
		if kind := config.FindSiteType(site.Type); kind != nil && kind.Start != "" {
			start = kind.Start
		}
	}

	if start == "" {
		start = "npm run start"
	}

	// The port on the command line, not only in the environment.
	//
	// OpenClaw takes it from `--port`, then OPENCLAW_GATEWAY_PORT, then its
	// config, then 18789 — and a gateway that falls through to 18789 while
	// nginx proxies to the port the panel assigned is a 502 on a service that
	// is running perfectly. The flag is the one place in that list the panel
	// can be sure of.
	if site.Type == "openclaw" && site.AppPort > 0 && !strings.Contains(start, "--port") {
		start = fmt.Sprintf("%s --port %d", start, site.AppPort)
	}

	service := site.ServiceName()

	// A configuration the process objects to is not something restarting fixes.
	// OpenClaw exits 78 — the conventional EX_CONFIG — and without this the
	// unit relaunches it every five seconds for ever, burning a CPU core and
	// writing the same complaint into the log a thousand times.
	restart := "Restart=always\nRestartSec=5\n"
	if site.Type == "openclaw" {
		restart += "RestartPreventExitStatus=78\n"
	}

	return fmt.Sprintf(
		"# Managed by %s\n"+
			"[Unit]\n"+
			"Description=%s (%s)\n"+
			"After=network.target\n"+
			"\n"+
			"[Service]\n"+
			"Type=simple\n"+
			"User=www-data\n"+
			"Group=www-data\n"+
			"WorkingDirectory=%s\n"+
			"Environment=NODE_ENV=production\n"+
			"Environment=PORT=%d\n"+
			"Environment=HOST=127.0.0.1\n"+
			"EnvironmentFile=-%s/.env\n"+
			"ExecStart=/bin/bash -lc '%s'\n"+
			"%s"+
			"StandardOutput=append:/var/log/%s.log\n"+
			"StandardError=append:/var/log/%s.log\n"+
			"\n"+
			"[Install]\n"+
			"WantedBy=multi-user.target\n",
		config.Name, site.Domain, site.TypeLabel(),
		site.RootPath, site.AppPort, site.RootPath, start, restart,
		service, service)
}

// helperConfig is the `$panel_https` map every vhost references.
const helperConfig = "# Managed by " + config.Name + "\n" +
	"# Tells PHP-FPM the request was secure, whether TLS ended at nginx or at a\n" +
	"# proxy in front of it. WordPress needs this or it builds http URLs and\n" +
	"# redirect-loops behind an https proxy.\n" +
	"map \"$scheme$http_x_forwarded_proto\" $panel_https {\n" +
	"    default   off;\n" +
	"    \"~*https\" on;\n" +
	"}\n"

// defaultServer is the catch-all that stops an unknown hostname being served
// by whichever site happens to be first.
//
// nginx picks the first server block on a port as the default; without this, a
// site whose certificate has not been issued yet has no 443 block, and its
// HTTPS traffic silently lands on another site — which then redirects the
// visitor to *its* login page.
func defaultServer() string {
	return fmt.Sprintf(
		"# Managed by %s — refuses unknown hostnames\n"+
			"server {\n"+
			"    listen 80 default_server;\n"+
			"    listen [::]:80 default_server;\n"+
			"    server_name _;\n"+
			"\n"+
			"    location ^~ /.well-known/acme-challenge/ {\n"+
			"        root %s;\n"+
			"        default_type \"text/plain\";\n"+
			"    }\n"+
			"\n"+
			"    location / {\n"+
			"        return 404 \"No site is configured for this hostname.\\n\";\n"+
			"    }\n"+
			"}\n"+
			"\n"+
			"server {\n"+
			"    listen 443 ssl default_server;\n"+
			"    listen [::]:443 ssl default_server;\n"+
			"    server_name _;\n"+
			"\n"+
			"    ssl_certificate     %s.crt;\n"+
			"    ssl_certificate_key %s.key;\n"+
			"\n"+
			"    return 404 \"No site is configured for this hostname.\\n\";\n"+
			"}\n",
		config.Name, ACMEWebroot, DefaultCert, DefaultCert)
}

// phpValueParam is the site's own php.ini overrides, handed to PHP-FPM through
// the request rather than through a pool of its own.
//
// PHP_VALUE is read by FPM as a block of ini directives, one per line, and
// applied to that request. A pool per site would be the other way to do it and
// costs a socket, a systemd reload and a vhost that names it — for settings an
// operator changes to make an upload work.
//
// Directives and values are both filtered to what an ini line can hold, so a
// value with a newline in it cannot smuggle a second directive into the block.
func phpValueParam(site *store.Site) string {
	settings := site.PHPOverrides()
	if len(settings) == 0 {
		return ""
	}

	// Sorted, so the file does not churn between renders and a diff of two
	// vhosts is about what changed.
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}

	sort.Strings(names)

	lines := make([]string, 0, len(names))

	for _, name := range names {
		if !safeINIToken(name) || !safeINIToken(settings[name]) {
			continue
		}

		lines = append(lines, name+"="+settings[name])
	}

	if len(lines) == 0 {
		return ""
	}

	return "\n        # Set from this site's PHP settings.\n" +
		"        fastcgi_param PHP_VALUE \"" + strings.Join(lines, "\n") + "\";\n"
}

// safeINIToken is what may appear either side of the `=` in a PHP_VALUE line.
// No quotes, no newlines, no semicolons: each of those ends the directive and
// starts something the operator did not write.
func safeINIToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}

	return !strings.ContainsAny(value, "\"\n\r;$")
}
