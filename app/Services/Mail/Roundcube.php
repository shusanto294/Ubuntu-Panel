<?php

namespace App\Services\Mail;

use App\Models\EmailDomain;
use App\Services\Shell\LocalConnection;
use App\Services\Sites\NginxVhost;
use App\Services\System\UpdateChecker;
use App\Services\Tasks\Step;
use App\Support\Settings;
use Illuminate\Support\Str;

/**
 * Roundcube webmail, one login page per mail domain.
 *
 * Installed from upstream rather than apt for the same reason phpMyAdmin is:
 * Debian's package drags in a web server and a dbconfig prompt, and this
 * machine already has nginx serving the panel.
 *
 * One copy of the code on disk, served by as many vhosts as there are mail
 * domains — `mail.example.com` for `example.com`, which is the address people
 * already expect webmail to be at and the one the panel publishes an A record
 * for. Roundcube talks to Dovecot and Postfix over loopback, so a mailbox works
 * the moment it exists and nothing about it depends on the mail server having a
 * public certificate.
 */
class Roundcube
{
    public const ROOT = '/usr/share/ubuntu-panel/roundcube';

    /** Pinned: an install that silently moves version is not reproducible. */
    public const VERSION = '1.6.11';

    public const DB_NAME = 'roundcube';

    public const DB_USER = 'roundcube';

    public function __construct(protected Settings $settings) {}

    public function isInstalled(): bool
    {
        return is_file(self::ROOT.'/index.php');
    }

    /** `mail.example.com` — where this domain's webmail answers. */
    public static function hostFor(EmailDomain|string $domain): string
    {
        return 'mail.'.(is_string($domain) ? $domain : $domain->domain);
    }

    /**
     * The address to send someone to.
     *
     * https unconditionally: the vhost starts on port 80 and is switched over
     * as soon as a certificate exists, and a webmail login form is not
     * something to hand out an http:// link to in the meantime.
     */
    public static function urlFor(EmailDomain|string $domain, ?string $address = null): string
    {
        $url = 'https://'.self::hostFor($domain).'/';

        // Roundcube reads `_user` off the query string and fills the username
        // in for you. Nothing is trusted about it — it is a typing shortcut.
        return $address ? $url.'?_user='.rawurlencode($address) : $url;
    }

    public static function vhostPath(EmailDomain|string $domain): string
    {
        return '/etc/nginx/sites-available/'.self::hostFor($domain);
    }

    public static function enabledPath(EmailDomain|string $domain): string
    {
        return '/etc/nginx/sites-enabled/'.self::hostFor($domain);
    }

    public static function certificatePath(EmailDomain|string $domain): string
    {
        return '/etc/letsencrypt/live/'.self::hostFor($domain);
    }

    /**
     * Fetch and unpack. The `complete` tarball carries its own vendor tree, so
     * there is no composer run on the server.
     *
     * @return array<int, Step>
     */
    public function installSteps(): array
    {
        $root = self::ROOT;
        $url = 'https://github.com/roundcube/roundcubemail/releases/download/'.
            self::VERSION.'/roundcubemail-'.self::VERSION.'-complete.tar.gz';

        return [
            Step::make('Install Roundcube', [
                'sudo rm -rf /tmp/panel-roundcube && mkdir -p /tmp/panel-roundcube',
                'curl -fsSL '.escapeshellarg($url).' -o /tmp/panel-roundcube/roundcube.tar.gz',
                'tar -xzf /tmp/panel-roundcube/roundcube.tar.gz -C /tmp/panel-roundcube --strip-components=1',
                // Replaced wholesale rather than merged: a half-upgraded
                // Roundcube is worse than either version of it. The config is
                // rewritten below, so nothing of ours is lost with it.
                'sudo rm -rf '.escapeshellarg($root),
                'sudo mkdir -p '.escapeshellarg(dirname($root)),
                'sudo mv /tmp/panel-roundcube '.escapeshellarg($root),
            ]),
        ];
    }

    /**
     * @return array<int, Step>
     */
    public function configureSteps(): array
    {
        $root = self::ROOT;
        $password = $this->ensureDbPassword();

        return [
            Step::make('Create the Roundcube database', [
                sprintf(
                    'sudo mysql -e "CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"',
                    self::DB_NAME
                ),
                sprintf(
                    'sudo mysql -e "CREATE USER IF NOT EXISTS \'%s\'@\'localhost\' IDENTIFIED BY \'%s\'; '.
                    'ALTER USER \'%1$s\'@\'localhost\' IDENTIFIED BY \'%2$s\'; '.
                    'GRANT ALL PRIVILEGES ON %3$s.* TO \'%1$s\'@\'localhost\'; FLUSH PRIVILEGES;"',
                    self::DB_USER,
                    $password,
                    self::DB_NAME
                ),
                // The initial schema is idempotent only in the sense that it
                // will not run twice: `users` existing is how we know.
                sprintf(
                    'sudo mysql -N -e "SELECT 1 FROM information_schema.tables WHERE table_schema=\'%1$s\' AND table_name=\'users\';" | grep -q 1 || '.
                    'sudo mysql %1$s < %2$s/SQL/mysql.initial.sql',
                    self::DB_NAME,
                    $root
                ),
            ]),

            Step::call('Write the Roundcube configuration', function (LocalConnection $ssh) use ($password, $root) {
                $ssh->putFile($root.'/config/config.inc.php', $this->config($password));

                $user = escapeshellarg($this->webUser());

                // Roundcube writes uploads, logs and its temp cache; the web
                // process owns those three and nothing else.
                $ssh->mustRun('sudo mkdir -p '.escapeshellarg($root.'/temp').' '.escapeshellarg($root.'/logs'));
                $ssh->mustRun('sudo chown -R '.$user.':'.$user.' '.escapeshellarg($root.'/temp').' '.escapeshellarg($root.'/logs'));
                $ssh->mustRun('sudo chmod 640 '.escapeshellarg($root.'/config/config.inc.php'));
                $ssh->mustRun('sudo chown root:'.$user.' '.escapeshellarg($root.'/config/config.inc.php'));

                // The upstream tarball ships an installer that can rewrite the
                // configuration and read the database. It has done its job.
                $ssh->mustRun('sudo rm -rf '.escapeshellarg($root.'/installer'));

                return 'Roundcube configured against localhost IMAP and SMTP.';
            }),

            Step::call('Publish webmail for the existing mail domains', function (LocalConnection $ssh) {
                $domains = EmailDomain::all();

                if ($domains->isEmpty()) {
                    return 'No mail domains yet; webmail vhosts are written as domains are added.';
                }

                foreach ($domains as $domain) {
                    $this->writeVhost($ssh, $domain);
                }

                $ssh->mustRun('sudo nginx -t');
                $ssh->mustRun('sudo systemctl reload nginx');

                return 'Webmail published for: '.$domains->pluck('domain')->implode(', ');
            }, optional: true),
        ];
    }

    /**
     * Write (and enable) the webmail vhost for one domain.
     *
     * Plain HTTP unless the certificate is already on disk — nginx refuses to
     * start when `ssl_certificate` names a file that is not there, so the TLS
     * form can only be written once there is something to point it at.
     */
    public function writeVhost(LocalConnection $ssh, EmailDomain $domain, ?bool $tls = null): string
    {
        $host = self::hostFor($domain);

        if ($tls === null) {
            [, $code] = $ssh->run('sudo test -f '.escapeshellarg(self::certificatePath($domain).'/fullchain.pem'));
            $tls = $code === 0;
        }

        $ssh->putFile(self::vhostPath($domain), $this->vhost($domain, $tls));
        $ssh->mustRun(sprintf(
            'sudo ln -sfn %s %s',
            escapeshellarg(self::vhostPath($domain)),
            escapeshellarg(self::enabledPath($domain))
        ));

        return $host.' vhost written'.($tls ? ' (HTTPS)' : ' (HTTP)').'.';
    }

    public function removeVhost(LocalConnection $ssh, EmailDomain|string $domain): void
    {
        $ssh->run('sudo rm -f '.escapeshellarg(self::enabledPath($domain)).' '.escapeshellarg(self::vhostPath($domain)));
    }

    public function vhost(EmailDomain|string $domain, bool $tls = false): string
    {
        $host = self::hostFor($domain);
        $root = self::ROOT;
        $php = $this->settings->phpVersion();
        $acme = NginxVhost::ACME_WEBROOT;
        $certs = self::certificatePath($domain);

        $body = <<<NGINX
            root {$root};

            index index.php;
            charset utf-8;
            client_max_body_size 64M;

            location / {
                try_files \$uri \$uri/ /index.php\$is_args\$args;
            }

            # Everything Roundcube keeps beside the document root is private.
            location ~ ^/(config|temp|logs|SQL|bin|installer)/ {
                deny all;
            }

            location ~ /\.(?!well-known).* {
                deny all;
            }

            location ~ \.php\$ {
                include snippets/fastcgi-php.conf;
                fastcgi_pass unix:/run/php/php{$php}-fpm.sock;
                fastcgi_read_timeout 300;

                # Overrides last — see NginxVhost notes.
                fastcgi_param SCRIPT_FILENAME \$realpath_root\$fastcgi_script_name;
                fastcgi_param HTTPS \$panel_https;
            }
        NGINX;

        $acmeBlock = <<<NGINX
            location ^~ /.well-known/acme-challenge/ {
                root {$acme};
                default_type "text/plain";
            }
        NGINX;

        if (! $tls) {
            return <<<NGINX
            # Managed by Ubuntu Panel — Roundcube webmail for {$host} (HTTP)
            server {
                listen 80;
                listen [::]:80;
                server_name {$host};

                access_log /var/log/nginx/{$host}-access.log;
                error_log  /var/log/nginx/{$host}-error.log;

            {$acmeBlock}
            {$body}
            }
            NGINX;
        }

        return <<<NGINX
        # Managed by Ubuntu Panel — Roundcube webmail for {$host} (HTTPS)
        server {
            listen 80;
            listen [::]:80;
            server_name {$host};

        {$acmeBlock}
            location / {
                return 301 https://\$host\$request_uri;
            }
        }

        server {
            listen 443 ssl;
            listen [::]:443 ssl;
            server_name {$host};

            ssl_certificate     {$certs}/fullchain.pem;
            ssl_certificate_key {$certs}/privkey.pem;
            ssl_session_timeout 1d;
            ssl_session_cache shared:SSL:10m;
            ssl_protocols TLSv1.2 TLSv1.3;
            ssl_prefer_server_ciphers off;

            access_log /var/log/nginx/{$host}-access.log;
            error_log  /var/log/nginx/{$host}-error.log;

        {$body}
        }
        NGINX;
    }

    /**
     * IMAP and SMTP over loopback, because Dovecot and Postfix are on this
     * machine. That also sidesteps the mail server's own certificate: there is
     * no TLS to verify on a localhost socket, and the browser's connection to
     * Roundcube is the one that has to be encrypted.
     */
    public function config(string $password): string
    {
        $db = self::DB_NAME;
        $user = self::DB_USER;
        $desKey = $this->ensureDesKey();
        $product = 'Ubuntu Panel Webmail';

        return <<<PHP
        <?php
        // Managed by Ubuntu Panel — rewritten whenever Roundcube is configured.

        \$config = [];

        \$config['db_dsnw'] = 'mysql://{$user}:{$password}@localhost/{$db}';

        \$config['imap_host'] = 'localhost:143';
        \$config['smtp_host'] = 'localhost:587';
        \$config['smtp_user'] = '%u';
        \$config['smtp_pass'] = '%p';

        \$config['support_url'] = '';
        \$config['product_name'] = '{$product}';
        \$config['des_key'] = '{$desKey}';

        // No `managesieve`: it needs a Dovecot sieve service listening on 4190,
        // which this mail server does not run, and a plugin that cannot connect
        // is a red error on the settings page rather than a missing feature.
        \$config['plugins'] = ['archive', 'zipdownload'];

        \$config['skin'] = 'elastic';
        \$config['enable_installer'] = false;

        // The login username is the full address, which is what the mail
        // server's virtual_users table is keyed on.
        \$config['username_domain'] = '';
        \$config['login_lc'] = 2;

        \$config['session_lifetime'] = 30;
        \$config['log_driver'] = 'file';
        \$config['temp_dir'] = '{$this->tempDir()}';
        PHP;
    }

    protected function tempDir(): string
    {
        return self::ROOT.'/temp';
    }

    /** Whoever owns the panel's files is who the web process runs as. */
    protected function webUser(): string
    {
        return app(UpdateChecker::class)->panelUser();
    }

    /**
     * Generated once and kept, because it is in a configuration file on the
     * server and in the database rows Roundcube encrypts with it.
     */
    protected function ensureDbPassword(): string
    {
        return $this->remember('roundcube_db_password', fn () => Str::password(28, symbols: false));
    }

    protected function ensureDesKey(): string
    {
        return $this->remember('roundcube_des_key', fn () => Str::random(24));
    }

    protected function remember(string $key, callable $make): string
    {
        $existing = $this->settings->get($key);

        if (filled($existing)) {
            return (string) $existing;
        }

        $value = $make();
        $this->settings->set($key, $value);

        return $value;
    }
}
