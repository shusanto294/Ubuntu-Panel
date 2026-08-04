<?php

namespace App\Services\System;

use App\Services\Mail\MailServerProvisioner;
use App\Support\Settings;
use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use Illuminate\Support\Str;
use InvalidArgumentException;

/**
 * Describes how to install every service, split into the four things that have
 * to happen in order:
 *
 *   pre        repositories, keys and debconf answers — before any apt install
 *   packages   apt package names, so many services can share one transaction
 *   install    non-apt installs (things fetched with curl)
 *   configure  everything after the bytes are on disk
 *
 * Splitting it this way is what lets the bulk installer put every service's
 * packages into a single `apt-get install`, which is the only real way to make
 * "install everything" fast — dpkg takes an exclusive lock, so genuinely
 * parallel apt runs are not possible.
 */
class ServiceCatalog
{
    public function __construct(
        protected MailServerProvisioner $mail,
        protected Settings $settings,
    ) {}

    /** @return array<int, string> every known service key, in install order */
    public static function keys(): array
    {
        return array_keys(config('panel.services'));
    }

    public static function meta(string $key): array
    {
        $meta = config('panel.services.'.$key);

        if (! $meta) {
            throw new InvalidArgumentException("Unknown service [{$key}].");
        }

        return $meta;
    }

    public static function label(string $key): string
    {
        return self::meta($key)['label'] ?? $key;
    }

    public static function sortOrder(string $key): int
    {
        return (int) array_search($key, self::keys(), true);
    }

    /**
     * Expand a selection to include everything it depends on, in install order.
     *
     * @param  array<int, string>  $keys
     * @return array<int, string>
     */
    public static function withDependencies(array $keys): array
    {
        $resolved = [];

        $walk = function (string $key) use (&$walk, &$resolved) {
            if (in_array($key, $resolved, true) || ! config('panel.services.'.$key)) {
                return;
            }

            foreach (self::meta($key)['requires'] ?? [] as $dependency) {
                $walk($dependency);
            }

            $resolved[] = $key;
        };

        foreach ($keys as $key) {
            $walk($key);
        }

        usort($resolved, fn ($a, $b) => self::sortOrder($a) <=> self::sortOrder($b));

        return $resolved;
    }

    /** Services installed by default when the panel is set up. */
    public static function defaults(): array
    {
        return self::withDependencies(
            collect(config('panel.services'))
                ->filter(fn ($meta) => $meta['default'] ?? false)
                ->keys()
                ->all()
        );
    }

    /**
     * Repositories, keys and debconf answers. Must run before any apt install.
     *
     * @return array<int, Step>
     */
    public function preSteps(string $key): array
    {
        return match ($key) {
            'php' => [
                Step::call('Choose a PHP source', fn (LocalConnection $ssh) => $this->resolvePhpSource($ssh)),
            ],

            'mongodb' => [
                Step::make('Add the MongoDB repository', [
                    'curl -fsSL https://www.mongodb.org/static/pgp/server-8.0.asc | sudo gpg -o /usr/share/keyrings/mongodb-server-8.0.gpg --dearmor --yes',
                    'echo "deb [ arch=amd64,arm64 signed-by=/usr/share/keyrings/mongodb-server-8.0.gpg ] https://repo.mongodb.org/apt/ubuntu $(lsb_release -cs)/mongodb-org/8.0 multiverse" | sudo tee /etc/apt/sources.list.d/mongodb-org-8.0.list',
                ]),
            ],

            'node' => [
                Step::make('Add the Node.js repository', [
                    sprintf(
                        'curl -fsSL https://deb.nodesource.com/setup_%s.x | sudo -E bash -',
                        $this->settings->nodeVersion() ?: '22'
                    ),
                ]),
            ],

            'mail' => [
                Step::make('Pre-answer the Postfix prompts', [
                    'echo "postfix postfix/main_mailer_type select Internet Site" | sudo debconf-set-selections',
                    'echo "postfix postfix/mailname string '.$this->mail->hostname().'" | sudo debconf-set-selections',
                ]),
            ],

            default => [],
        };
    }

    /**
     * The apt packages this service needs.
     *
     * @return array<int, string>
     */
    public function packages(string $key): array
    {
        $php = $this->settings->phpVersion();

        return match ($key) {
            'base' => [
                'software-properties-common', 'curl', 'wget', 'unzip', 'zip', 'git',
                'ufw', 'ca-certificates', 'gnupg', 'lsb-release', 'apt-transport-https', 'rsync',
            ],
            'nginx' => ['nginx'],
            'php' => array_map(
                fn ($suffix) => "php{$php}-{$suffix}",
                ['fpm', 'cli', 'common', 'mysql', 'pgsql', 'curl', 'mbstring', 'xml', 'zip', 'gd', 'bcmath', 'intl', 'soap', 'imagick', 'redis']
            ),
            'certbot' => ['certbot', 'python3-certbot-nginx'],
            'mysql' => ['mariadb-server', 'mariadb-client'],
            'postgres' => ['postgresql', 'postgresql-contrib'],
            'mongodb' => ['mongodb-org'],
            'redis' => ['redis-server'],
            'node' => ['nodejs'],
            'mail' => [
                'postfix', 'postfix-mysql', 'dovecot-core', 'dovecot-imapd',
                'dovecot-pop3d', 'dovecot-lmtpd', 'dovecot-mysql', 'opendkim', 'opendkim-tools',
            ],
            default => [],
        };
    }

    /**
     * Installs that do not go through apt.
     *
     * @return array<int, Step>
     */
    public function installSteps(string $key): array
    {
        return match ($key) {
            'composer' => [
                Step::make('Install Composer', [
                    'command -v composer >/dev/null && composer --version || (curl -sS https://getcomposer.org/installer -o /tmp/composer-setup.php && sudo php /tmp/composer-setup.php --install-dir=/usr/local/bin --filename=composer && rm -f /tmp/composer-setup.php)',
                    'composer --version',
                ]),
            ],

            // npm rather than apt, which is how pm2 is distributed. Installed
            // globally so it is on PATH for every user, and `pm2 startup`
            // registers the boot unit for whoever the panel runs as.
            'pm2' => [
                Step::make('Install PM2', [
                    'sudo npm install -g pm2',
                    'pm2 --version',
                ]),
                Step::make('Let PM2 survive a reboot', [
                    'sudo env PATH=$PATH:/usr/bin pm2 startup systemd -u $(whoami) --hp $HOME',
                ], optional: true),
            ],

            'wpcli' => [
                Step::make('Install WP-CLI', [
                    'curl -fsSL -o /tmp/wp-cli.phar https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar',
                    'sudo mv /tmp/wp-cli.phar /usr/local/bin/wp && sudo chmod +x /usr/local/bin/wp',
                    'wp --info --allow-root',
                ]),
            ],

            default => [],
        };
    }

    /**
     * Everything after the packages are on disk.
     *
     * @return array<int, Step>
     */
    public function configureSteps(string $key): array
    {
        $php = $this->settings->phpVersion();

        return match ($key) {
            'base' => [
                Step::make('Configure the firewall', [
                    'sudo ufw allow OpenSSH',
                    'sudo ufw --force enable',
                    'sudo ufw status',
                ], optional: true),
                Step::make('Prepare the web root', [
                    'sudo mkdir -p /var/www',
                ]),
            ],

            'nginx' => [
                Step::call('Refuse unknown hostnames', function (LocalConnection $ssh) {
                    $ssh->mustRun('sudo mkdir -p '.escapeshellarg(\App\Services\Sites\NginxVhost::ACME_WEBROOT));
                    $ssh->mustRun(\App\Services\Sites\NginxVhost::defaultCertificateCommand());
                    $ssh->putFile(
                        \App\Services\Sites\NginxVhost::defaultServerPath(),
                        \App\Services\Sites\NginxVhost::defaultServerConfig()
                    );
                    $ssh->putFile(
                        \App\Services\Sites\NginxVhost::helperConfigPath(),
                        \App\Services\Sites\NginxVhost::helperConfig()
                    );

                    return 'Default server installed; unknown hostnames get a 404.';
                }),
                Step::make('Start nginx', [
                    'sudo systemctl enable --now nginx',
                    // The default vhost would otherwise answer for unknown hostnames.
                    'sudo rm -f /etc/nginx/sites-enabled/default',
                    'sudo nginx -t && sudo systemctl reload nginx',
                ]),
                Step::make('Open the web ports', [
                    "sudo ufw allow 'Nginx Full'",
                ], optional: true),
            ],

            'php' => [
                Step::call('Tune PHP for web apps', function (LocalConnection $ssh) {
                    $version = $this->settings->phpVersion();

                    return $ssh->mustRun(sprintf(
                        'sudo sed -i "s/^upload_max_filesize = .*/upload_max_filesize = 128M/; s/^post_max_size = .*/post_max_size = 128M/; s/^memory_limit = .*/memory_limit = 512M/; s/^max_execution_time = .*/max_execution_time = 300/" /etc/php/%s/fpm/php.ini',
                        $version
                    ));
                }),
                Step::call('Start PHP-FPM', function (LocalConnection $ssh) {
                    $version = $this->settings->phpVersion();

                    $ssh->mustRun("sudo systemctl enable --now php{$version}-fpm");
                    $ssh->mustRun("sudo systemctl restart php{$version}-fpm");

                    return $ssh->mustRun("php{$version} -v");
                }),
            ],

            'mysql' => [
                Step::make('Start and secure MariaDB', [
                    'sudo systemctl enable --now mariadb',
                    'sudo mysql -e "DELETE FROM mysql.user WHERE User=\'\'; DROP DATABASE IF EXISTS test; DELETE FROM mysql.db WHERE Db=\'test\' OR Db=\'test\\\\_%\'; FLUSH PRIVILEGES;"',
                    'sudo mysql -e "SELECT VERSION();"',
                ]),
            ],

            'postgres' => [
                Step::make('Start PostgreSQL', [
                    'sudo systemctl enable --now postgresql',
                    'sudo -u postgres psql -c "SELECT version();"',
                ]),
            ],

            'mongodb' => $this->mongoConfigureSteps(),

            'redis' => $this->redisConfigureSteps(),

            'node' => [
                Step::make('Check Node.js', [
                    'node -v && npm -v',
                ]),
                Step::make('Install package managers', [
                    'sudo npm install -g pnpm yarn --silent',
                ], optional: true),
            ],

            'mail' => $this->mail->configureSteps(),

            default => [],
        };
    }

    /**
     * The full recipe for one service on its own: repositories, its own apt
     * transaction, then the rest.
     *
     * @return array<int, Step>
     */
    public function steps(string $key): array
    {
        $packages = $this->packages($key);

        $apt = $packages === [] ? [] : [
            Step::make('Update package lists', [
                'sudo DEBIAN_FRONTEND=noninteractive apt-get update -y || '.
                'echo "apt-get update reported errors; continuing with the repositories that did work"',
            ]),
            Step::call('Install '.self::label($key), function (LocalConnection $ssh) use ($key) {
                // Rebuilt here: a pre-step may have renegotiated the version.
                $names = $this->packages($key);

                return $ssh->mustRun(
                    'sudo DEBIAN_FRONTEND=noninteractive apt-get install -y '.implode(' ', $names)
                );
            }),
        ];

        return array_merge(
            [$this->waitForAptStep()],
            $this->preSteps($key),
            $apt,
            $this->installSteps($key),
            $this->configureSteps($key),
        );
    }

    /**
     * Work out where PHP is going to come from on this box.
     *
     * The ondrej PPA does not publish for every Ubuntu release (26.04 "resolute"
     * being the current example), and a source that 404s breaks every later
     * `apt-get update`. So: prefer the distro's own packages, add the PPA only
     * when it actually publishes for this release, and fall back to the newest
     * PHP the distro does offer — recording that choice on the server.
     */
    public function resolvePhpSource(LocalConnection $ssh): string
    {
        $wanted = $this->settings->phpVersion();
        $codename = trim($ssh->run('lsb_release -cs 2>/dev/null || . /etc/os-release && echo "$VERSION_CODENAME"')[0]);
        $log = ["Ubuntu codename: {$codename}", "Requested: PHP {$wanted}"];

        if ($this->packageAvailable($ssh, "php{$wanted}-fpm")) {
            $log[] = "php{$wanted}-fpm is available from the configured repositories; no PPA needed.";

            return implode("\n", $log);
        }

        if ($this->ppaPublishesFor($ssh, $codename)) {
            $log[] = 'Adding ppa:ondrej/php.';
            $ssh->run('sudo add-apt-repository -y ppa:ondrej/php');
            $ssh->run('sudo DEBIAN_FRONTEND=noninteractive apt-get update -y');

            if ($this->packageAvailable($ssh, "php{$wanted}-fpm")) {
                $log[] = "php{$wanted}-fpm is now available from the PPA.";

                return implode("\n", $log);
            }
        } else {
            $log[] = "ppa:ondrej/php does not publish for {$codename}; using the distro's own PHP packages.";
            $this->removeOndrejSources($ssh);
        }

        $available = $this->newestAvailablePhp($ssh);

        if ($available === null) {
            throw new \RuntimeException(
                "No PHP-FPM package is available on this server (looked for php{$wanted}-fpm). ".
                'Check the apt sources on the box.'
            );
        }

        if ($available !== $wanted) {
            $this->settings->set('php_version', $available);
            $log[] = "PHP {$wanted} is not available here; using PHP {$available} instead.";
        }

        return implode("\n", $log);
    }

    protected function packageAvailable(LocalConnection $ssh, string $package): bool
    {
        [$output] = $ssh->run('apt-cache policy '.escapeshellarg($package).' 2>/dev/null');

        return $output !== '' && ! str_contains($output, 'Candidate: (none)');
    }

    /** Does the PPA have a Release file for this Ubuntu release? */
    protected function ppaPublishesFor(LocalConnection $ssh, string $codename): bool
    {
        if ($codename === '') {
            return false;
        }

        [, $code] = $ssh->run(sprintf(
            'curl -fsI --max-time 15 https://ppa.launchpadcontent.net/ondrej/php/ubuntu/dists/%s/Release >/dev/null',
            escapeshellarg($codename)
        ));

        return $code === 0;
    }

    /** A source that 404s poisons every later apt-get update. */
    protected function removeOndrejSources(LocalConnection $ssh): void
    {
        $ssh->run('sudo rm -f /etc/apt/sources.list.d/ondrej-ubuntu-php-*.sources /etc/apt/sources.list.d/ondrej-ubuntu-php-*.list');
    }

    /** The highest phpX.Y-fpm the configured repositories can offer. */
    protected function newestAvailablePhp(LocalConnection $ssh): ?string
    {
        [$output] = $ssh->run(
            "apt-cache search --names-only '^php[0-9]+\\.[0-9]+-fpm$' 2>/dev/null | ".
            "awk '{print \$1}' | sed 's/^php//; s/-fpm$//' | sort -V | tail -1"
        );

        $version = trim($output);

        return preg_match('/^\d+\.\d+$/', $version) ? $version : null;
    }

    /** dpkg allows one writer; wait rather than fail with "could not get lock". */
    public function waitForAptStep(): Step
    {
        return Step::make('Wait for apt locks', [
            'while sudo fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do echo "waiting for another apt process…"; sleep 3; done; echo "apt is free"',
        ]);
    }

    /**
     * @return array<int, Step>
     */
    protected function mongoConfigureSteps(): array
    {
        $password = $this->ensurePassword('mongo_root_password');

        return [
            Step::make('Start MongoDB', [
                'sudo systemctl enable --now mongod',
                'sleep 5',
            ]),
            Step::call('Create the admin user', function (LocalConnection $ssh) use ($password) {
                $script = sprintf(
                    'db.getSiblingDB("admin").getUser("panelAdmin") || db.getSiblingDB("admin").createUser({user:"panelAdmin",pwd:%s,roles:[{role:"root",db:"admin"}]})',
                    json_encode($password)
                );

                [$output] = $ssh->run('mongosh --quiet --eval '.escapeshellarg($script));

                // Appending the auth block twice would break the config file.
                $ssh->run(
                    'grep -q "authorization: enabled" /etc/mongod.conf || '.
                    'printf "\nsecurity:\n  authorization: enabled\n" | sudo tee -a /etc/mongod.conf > /dev/null'
                );
                $ssh->run('sudo systemctl restart mongod');

                return $output."\nAdmin user created and authorization enabled.";
            }),
        ];
    }

    /**
     * @return array<int, Step>
     */
    protected function redisConfigureSteps(): array
    {
        $password = $this->ensurePassword('redis_password');

        return [
            Step::make('Set the Redis password', [
                sprintf(
                    'sudo sed -i "s/^# *requirepass .*/requirepass %1$s/; s/^requirepass .*/requirepass %1$s/" /etc/redis/redis.conf',
                    $password
                ),
                sprintf(
                    'grep -q "^requirepass" /etc/redis/redis.conf || echo "requirepass %s" | sudo tee -a /etc/redis/redis.conf > /dev/null',
                    $password
                ),
                'sudo systemctl enable --now redis-server',
                'sudo systemctl restart redis-server',
            ]),
        ];
    }

    /**
     * A password the panel generated once during an install and has to keep
     * using: MongoDB and Redis are configured with it, so regenerating it would
     * lock the panel out of its own services.
     */
    protected function ensurePassword(string $key): string
    {
        return $this->settings->rememberSecret(
            $key,
            fn () => Str::password(28, symbols: false)
        );
    }
}
