// Package sites deploys sites: the nginx vhost, the per-type recipe, and the
// lifecycle operations the Sites pages trigger.
package sites

import (
	"fmt"
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
)

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
func body(out *strings.Builder, site *store.Site) {
	root := site.DocumentRoot()

	if site.IsProxied() {
		fmt.Fprintf(out, "    charset utf-8;\n    client_max_body_size 128M;\n")

		if site.Type == "nextjs" {
			fmt.Fprintf(out,
				"\n"+
					"    location /_next/static/ {\n"+
					"        alias %s/.next/static/;\n"+
					"        expires 365d;\n"+
					"        access_log off;\n"+
					"    }\n", site.RootPath)
		}

		fmt.Fprintf(out,
			"\n"+
				"    location / {\n"+
				"        proxy_pass http://%s_upstream;\n"+
				"        proxy_http_version 1.1;\n"+
				"        proxy_set_header Upgrade $http_upgrade;\n"+
				"        proxy_set_header Connection \"upgrade\";\n"+
				"        proxy_set_header Host $host;\n"+
				"        proxy_set_header X-Real-IP $remote_addr;\n"+
				"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"+
				"        proxy_set_header X-Forwarded-Proto $scheme;\n"+
				"        proxy_cache_bypass $http_upgrade;\n"+
				"        proxy_read_timeout 300;\n"+
				"    }\n", site.ServiceName())

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
		body(&out, site)
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

		body(&out, site)
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

	body(&out, site)
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
