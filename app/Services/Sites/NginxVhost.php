<?php

namespace App\Services\Sites;

use App\Models\Site;

class NginxVhost
{
    /** Shared webroot for HTTP-01 challenges, served by every site on port 80. */
    public const ACME_WEBROOT = '/var/www/letsencrypt';

    /**
     * @param  bool  $tls  render the HTTPS form — only once the certificate exists,
     *                     since nginx refuses to start if ssl_certificate is missing
     */
    public static function render(Site $site, bool $tls = false): string
    {
        return $tls ? self::tlsVhost($site) : self::plainVhost($site);
    }

    /**
     * Tells PHP-FPM the request was secure, whether TLS ended at nginx or at a
     * proxy in front of it (Cloudflare). WordPress needs this or it builds http
     * URLs and redirect-loops behind an https proxy.
     */
    public static function helperConfig(): string
    {
        return <<<'NGINX'
        # Managed by Ubuntu Panel
        map "$scheme$http_x_forwarded_proto" $panel_https {
            default   off;
            "~*https" on;
        }
        NGINX;
    }

    public static function helperConfigPath(): string
    {
        return '/etc/nginx/conf.d/panel-proxy.conf';
    }

    /**
     * A catch-all so a hostname with no vhost of its own can never be served by
     * whichever site happens to be first.
     *
     * nginx picks the first server block on a port as the default; without this,
     * a site whose certificate has not been issued yet has no 443 block, and its
     * HTTPS traffic silently lands on another site — which then redirects the
     * visitor to *its* login page.
     */
    public static function defaultServerConfig(): string
    {
        $acme = self::ACME_WEBROOT;
        $cert = self::DEFAULT_CERT;

        return <<<NGINX
        # Managed by Ubuntu Panel — refuses unknown hostnames
        server {
            listen 80 default_server;
            listen [::]:80 default_server;
            server_name _;

            location ^~ /.well-known/acme-challenge/ {
                root {$acme};
                default_type "text/plain";
            }

            location / {
                return 404 "No site is configured for this hostname.\n";
            }
        }

        server {
            listen 443 ssl default_server;
            listen [::]:443 ssl default_server;
            server_name _;

            ssl_certificate     {$cert}.crt;
            ssl_certificate_key {$cert}.key;

            return 404 "No site is configured for this hostname.\n";
        }
        NGINX;
    }

    public static function defaultServerPath(): string
    {
        return '/etc/nginx/conf.d/000-panel-default.conf';
    }

    /** Self-signed, only ever presented to requests for unknown hostnames. */
    public const DEFAULT_CERT = '/etc/nginx/panel-default';

    public static function defaultCertificateCommand(): string
    {
        $cert = self::DEFAULT_CERT;

        return sprintf(
            'test -f %s.crt || sudo openssl req -x509 -nodes -newkey rsa:2048 -days 3650 '.
            '-keyout %s.key -out %s.crt -subj "/CN=unconfigured.invalid" 2>/dev/null',
            $cert,
            $cert,
            $cert
        );
    }

    public static function certificatePath(Site $site): string
    {
        return '/etc/letsencrypt/live/'.$site->domain;
    }

    /** Port 80 only. Used before a certificate exists. */
    protected static function plainVhost(Site $site): string
    {
        $names = implode(' ', $site->hostnames());
        $domain = $site->domain;

        return <<<NGINX
        # Managed by Ubuntu Panel — {$site->typeLabel()} site (HTTP)
        {$site->upstreamBlock()}
        server {
            listen 80;
            listen [::]:80;
            server_name {$names};

            access_log /var/log/nginx/{$domain}-access.log;
            error_log  /var/log/nginx/{$domain}-error.log;

        {$site->acmeBlock()}
        {$site->bodyBlock()}
        }
        NGINX;
    }

    /** Port 80 redirects to 443; the site itself is served over TLS. */
    protected static function tlsVhost(Site $site): string
    {
        $names = implode(' ', $site->hostnames());
        $domain = $site->domain;
        $certs = self::certificatePath($site);

        return <<<NGINX
        # Managed by Ubuntu Panel — {$site->typeLabel()} site (HTTPS, HTTP redirected)
        {$site->upstreamBlock()}
        server {
            listen 80;
            listen [::]:80;
            server_name {$names};

        {$site->acmeBlock()}
            location / {
                return 301 https://\$host\$request_uri;
            }
        }

        server {
            listen 443 ssl;
            listen [::]:443 ssl;
            http2 on;
            server_name {$names};

            ssl_certificate     {$certs}/fullchain.pem;
            ssl_certificate_key {$certs}/privkey.pem;
            ssl_session_timeout 1d;
            ssl_session_cache shared:SSL:10m;
            ssl_protocols TLSv1.2 TLSv1.3;
            ssl_prefer_server_ciphers off;
            add_header Strict-Transport-Security "max-age=31536000" always;

            access_log /var/log/nginx/{$domain}-access.log;
            error_log  /var/log/nginx/{$domain}-error.log;

        {$site->bodyBlock()}
        }
        NGINX;
    }

    /** systemd unit for node-family apps. */
    public static function systemdUnit(Site $site): string
    {
        $root = rtrim($site->root_path, '/');
        $start = $site->start_command ?: 'npm run start';
        $port = $site->app_port;

        return <<<UNIT
        # Managed by Ubuntu Panel
        [Unit]
        Description={$site->domain} ({$site->typeLabel()})
        After=network.target

        [Service]
        Type=simple
        User=www-data
        Group=www-data
        WorkingDirectory={$root}
        Environment=NODE_ENV=production
        Environment=PORT={$port}
        Environment=HOST=127.0.0.1
        EnvironmentFile=-{$root}/.env
        ExecStart=/bin/bash -lc '{$start}'
        Restart=always
        RestartSec=5
        StandardOutput=append:/var/log/{$site->serviceName()}.log
        StandardError=append:/var/log/{$site->serviceName()}.log

        [Install]
        WantedBy=multi-user.target
        UNIT;
    }

    public static function availablePath(Site $site): string
    {
        return "/etc/nginx/sites-available/{$site->domain}";
    }

    public static function enabledPath(Site $site): string
    {
        return "/etc/nginx/sites-enabled/{$site->domain}";
    }

    public static function unitPath(Site $site): string
    {
        return "/etc/systemd/system/{$site->serviceName()}.service";
    }
}
