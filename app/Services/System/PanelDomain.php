<?php

namespace App\Services\System;

use App\Services\Shell\LocalConnection;
use App\Support\Settings;
use Illuminate\Support\Facades\Cache;
use RuntimeException;

/**
 * Serves the panel itself on a hostname you own, with a real certificate.
 *
 * A fresh install answers on https://<ip>:8443 with a self-signed certificate,
 * which works but warns. Point a name at the machine and this swaps the vhost
 * over to it, gets a Let's Encrypt certificate, and tells the application its
 * new address.
 */
class PanelDomain
{
    public const VHOST = '/etc/nginx/sites-available/ubuntu-panel.conf';

    public const LINK = '/etc/nginx/sites-enabled/ubuntu-panel.conf';

    public function __construct(
        protected LocalConnection $shell,
        protected Settings $settings,
        protected HostInfo $host,
        protected TerminalProxy $terminal,
    ) {}

    public function current(): ?string
    {
        return $this->settings->get('panel_domain');
    }

    /** What the browser should be pointed at right now. */
    public function url(): string
    {
        $domain = $this->current();

        return $domain
            ? 'https://'.$domain
            : rtrim((string) config('app.url'), '/');
    }

    /**
     * Does this hostname actually resolve to this machine?
     *
     * Let's Encrypt has to reach the name over HTTP, so getting this wrong is
     * the single most common reason issuing fails. It is a warning rather than
     * a refusal — DNS may simply not have propagated yet.
     */
    public function resolvesHere(string $domain): bool
    {
        $resolved = gethostbyname($domain);
        $ip = $this->host->publicIp();

        return $ip !== null && $resolved === $ip;
    }

    /**
     * Point the panel at a hostname and secure it.
     *
     * @return array<int, string> log lines
     */
    public function apply(string $domain, ?string $email = null): array
    {
        $domain = strtolower(trim($domain));

        if (! preg_match('/^(?!-)[a-z0-9-]{1,63}(?<!-)(\.(?!-)[a-z0-9-]{1,63}(?<!-))+$/', $domain)) {
            throw new RuntimeException("'{$domain}' is not a valid hostname.");
        }

        $lines = [];

        if (! $this->resolvesHere($domain)) {
            $lines[] = "Warning: {$domain} does not resolve to ".($this->host->publicIp() ?? 'this machine').
                ' yet. Issuing a certificate will fail until DNS points here.';
        }

        // The vhost includes this; nginx will not start if it is missing.
        $this->terminal->writeSnippet();

        // Serve the name over plain HTTP first: certbot needs to answer a
        // challenge on port 80 before there is any certificate to serve.
        $this->shell->putFile(self::VHOST, $this->plainVhost($domain));
        $this->shell->mustRun('sudo ln -sf '.self::VHOST.' '.self::LINK);
        $this->shell->mustRun('sudo nginx -t');
        $this->shell->mustRun('sudo systemctl reload nginx');
        $lines[] = "nginx is serving {$domain} on port 80.";

        $this->shell->mustRun('sudo apt-get install -y -qq certbot python3-certbot-nginx');

        $certbot = sprintf(
            'sudo certbot --nginx --non-interactive --agree-tos --redirect -d %s %s',
            escapeshellarg($domain),
            $email ? '-m '.escapeshellarg($email) : '--register-unsafely-without-email'
        );

        [$output, $code] = $this->shell->run($certbot);

        if ($code !== 0) {
            throw new RuntimeException(
                "Could not issue a certificate for {$domain}.\n".$output.
                "\nThe panel is still reachable on its IP address."
            );
        }

        $lines[] = "Certificate issued for {$domain}.";

        $this->settings->set('panel_domain', $domain);
        $this->setAppUrl('https://'.$domain);

        $this->shell->run('sudo nginx -t && sudo systemctl reload nginx');

        Cache::forget('panel:public-ip');

        $lines[] = "The panel now answers on https://{$domain}.";

        return $lines;
    }

    /** Stop serving the hostname and go back to the IP address. */
    public function clear(): void
    {
        $this->settings->forget('panel_domain');
    }

    /**
     * Port 80 only. certbot rewrites this file itself once it has a
     * certificate, adding the 443 server block and the redirect.
     */
    protected function plainVhost(string $domain): string
    {
        $root = base_path('public');
        $php = $this->phpVersion();

        return <<<NGINX
        # Managed by Ubuntu Panel
        server {
            listen 80;
            listen [::]:80;
            server_name {$domain};

            include snippets/ubuntu-panel-terminal.conf;

            root {$root};
            index index.php;

            client_max_body_size 128M;

            location / {
                try_files \$uri \$uri/ /index.php?\$query_string;
            }

            location ~ \.php\$ {
                include snippets/fastcgi-php.conf;
                fastcgi_pass unix:/run/php/php{$php}-fpm.sock;
                fastcgi_read_timeout 300;
            }

            location ~ /\.(?!well-known).* {
                deny all;
            }
        }
        NGINX;
    }

    /** The version actually running, not one assumed at install time. */
    protected function phpVersion(): string
    {
        return PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;
    }

    /** Laravel builds absolute URLs from this; links break if it is stale. */
    protected function setAppUrl(string $url): void
    {
        $path = base_path('.env');

        if (! is_writable($path)) {
            return;
        }

        $contents = (string) file_get_contents($path);

        $contents = preg_match('/^APP_URL=.*$/m', $contents)
            ? preg_replace('/^APP_URL=.*$/m', 'APP_URL='.$url, $contents)
            : $contents."\nAPP_URL=".$url."\n";

        file_put_contents($path, $contents);

        $this->shell->run('cd '.escapeshellarg(base_path()).' && php artisan config:cache');
    }
}
