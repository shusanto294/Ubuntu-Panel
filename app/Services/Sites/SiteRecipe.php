<?php

namespace App\Services\Sites;

use App\Models\Database;
use App\Models\Site;
use App\Services\Databases\DatabaseManager;
use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use Illuminate\Support\Str;

/**
 * Builds the deployment steps for a site, branching on its type.
 */
class SiteRecipe
{
    public function __construct(
        protected Site $site,
        protected DatabaseManager $databases,
    ) {}

    /**
     * @return array<int, Step>
     */
    public function steps(): array
    {
        $site = $this->site;
        $root = rtrim($site->root_path, '/');

        $steps = [
            Step::make('Create the site directory', [
                'sudo mkdir -p '.escapeshellarg($site->documentRoot()),
                'sudo chown -R www-data:www-data '.escapeshellarg($root),
                'sudo chmod -R 755 '.escapeshellarg($root),
            ]),
        ];

        if ($site->needsDatabase()) {
            $steps[] = Step::call('Create the application database', function (LocalConnection $ssh) {
                return $this->databases->createDuringTask($this->ensureDatabase(), $ssh);
            });
        }

        $steps = array_merge($steps, match ($site->type) {
            'laravel' => $this->laravelSteps(),
            'wordpress' => $this->wordpressSteps(),
            'nodejs', 'nextjs' => $this->nodeSteps(),
            'static' => $this->staticSteps(),
            default => $this->phpSteps(),
        });

        $steps[] = Step::call('Write the nginx vhost', function (LocalConnection $ssh) use ($site) {
            // Every server needs the $panel_https map the vhosts reference, plus a
            // shared webroot for HTTP-01 challenges.
            $ssh->putFile(NginxVhost::helperConfigPath(), NginxVhost::helperConfig());
            $ssh->mustRun('sudo mkdir -p '.escapeshellarg(NginxVhost::ACME_WEBROOT.'/.well-known/acme-challenge'));
            $ssh->mustRun('sudo chown -R www-data:www-data '.escapeshellarg(NginxVhost::ACME_WEBROOT));

            // Unknown hostnames must hit a dead end, not the first site on the box.
            $ssh->mustRun(NginxVhost::defaultCertificateCommand());
            $ssh->putFile(NginxVhost::defaultServerPath(), NginxVhost::defaultServerConfig());

            // Plain HTTP for now: nginx will not start with an ssl_certificate
            // that does not exist yet.
            $ssh->putFile(NginxVhost::availablePath($site), NginxVhost::render($site, tls: false));
            $ssh->mustRun(sprintf(
                'sudo ln -sfn %s %s',
                escapeshellarg(NginxVhost::availablePath($site)),
                escapeshellarg(NginxVhost::enabledPath($site))
            ));

            return 'vhost written to '.NginxVhost::availablePath($site);
        });

        $steps[] = Step::make('Reload nginx', [
            'sudo nginx -t',
            'sudo systemctl reload nginx',
        ]);

        if ($site->manage_dns) {
            $steps[] = Step::call('Publish DNS records', fn () => app(\App\Services\Cloudflare\CloudflareDnsManager::class)->syncForSite($site->fresh()));
        }

        if ($site->ssl) {
            $steps[] = Step::call(
                'Request the TLS certificate',
                fn (LocalConnection $ssh) => $this->requestCertificate($ssh),
                optional: true
            );

            $steps[] = Step::call('Serve the site over HTTPS', function (LocalConnection $ssh) use ($site) {
                $certs = NginxVhost::certificatePath($site);
                [, $code] = $ssh->run('sudo test -f '.escapeshellarg($certs.'/fullchain.pem'));

                if ($code !== 0) {
                    $site->forceFill(['ssl' => false])->save();

                    return 'No certificate was issued, so the site stays on HTTP. '.
                        'Check that the domain resolves to this server, then redeploy.';
                }

                $ssh->putFile(NginxVhost::availablePath($site), NginxVhost::render($site->fresh(), tls: true));
                $ssh->mustRun('sudo nginx -t');
                $ssh->mustRun('sudo systemctl reload nginx');

                $site->forceFill(['ssl' => true])->save();

                return 'HTTPS enabled; HTTP now redirects to it.';
            }, optional: true);
        }

        // WordPress stores absolute URLs, so it has to be told which scheme won.
        if ($site->type === 'wordpress') {
            $steps[] = Step::call('Point WordPress at the final URL', function (LocalConnection $ssh) use ($site) {
                $fresh = $site->fresh();
                $url = ($fresh->ssl ? 'https://' : 'http://').$fresh->domain;
                $wp = sprintf('sudo -u www-data wp --path=%s', escapeshellarg($fresh->documentRoot()));

                $ssh->run(sprintf('%s option update home %s', $wp, escapeshellarg($url)));
                $ssh->run(sprintf('%s option update siteurl %s', $wp, escapeshellarg($url)));

                return 'WordPress home and siteurl set to '.$url;
            }, optional: true);
        }

        return $steps;
    }

    /**
     * @return array<int, Step>
     */
    protected function phpSteps(): array
    {
        $site = $this->site;

        if ($site->repository) {
            return array_merge($this->cloneSteps(), [
                Step::make('Fix permissions', [
                    'sudo chown -R www-data:www-data '.escapeshellarg(rtrim($site->root_path, '/')),
                ]),
            ]);
        }

        return [
            Step::call('Write a placeholder page', function (LocalConnection $ssh) use ($site) {
                $ssh->putFile($site->documentRoot().'/index.php', $this->placeholder());
                $ssh->mustRun('sudo chown -R www-data:www-data '.escapeshellarg(rtrim($site->root_path, '/')));

                return 'placeholder index.php created';
            }),
        ];
    }

    /**
     * @return array<int, Step>
     */
    protected function staticSteps(): array
    {
        $site = $this->site;

        if ($site->repository) {
            return $this->cloneSteps();
        }

        return [
            Step::call('Write a placeholder page', function (LocalConnection $ssh) use ($site) {
                $ssh->putFile($site->documentRoot().'/index.html', $this->placeholderHtml());
                $ssh->mustRun('sudo chown -R www-data:www-data '.escapeshellarg(rtrim($site->root_path, '/')));

                return 'placeholder index.html created';
            }),
        ];
    }

    /**
     * @return array<int, Step>
     */
    protected function laravelSteps(): array
    {
        $site = $this->site;
        $root = rtrim($site->root_path, '/');
        $php = $site->php_version;
        $database = $this->ensureDatabase();

        $steps = $site->repository
            ? $this->cloneSteps()
            : [Step::make('Install a fresh Laravel application', [
                sprintf(
                    'sudo -u www-data COMPOSER_HOME=/tmp/composer composer create-project laravel/laravel %s --no-interaction --prefer-dist',
                    escapeshellarg($root)
                ),
            ])];

        if ($site->repository) {
            $steps[] = Step::make('Install Composer dependencies', [
                sprintf(
                    'cd %s && sudo -u www-data COMPOSER_HOME=/tmp/composer composer install --no-dev --optimize-autoloader --no-interaction',
                    escapeshellarg($root)
                ),
            ]);
        }

        $steps[] = Step::call('Write the .env file', function (LocalConnection $ssh) use ($site, $root, $database) {
            $ssh->putFile($root.'/.env', $this->laravelEnv($database));
            $ssh->mustRun('sudo chown www-data:www-data '.escapeshellarg($root.'/.env'));

            return '.env written with database credentials for '.$database->name;
        });

        $steps[] = Step::make('Prepare the application', [
            sprintf('cd %s && sudo -u www-data php%s artisan key:generate --force', escapeshellarg($root), $php),
            sprintf('cd %s && sudo -u www-data php%s artisan storage:link || true', escapeshellarg($root), $php),
            sprintf('cd %s && sudo -u www-data php%s artisan migrate --force || echo "migrations skipped"', escapeshellarg($root), $php),
            sprintf('cd %s && sudo -u www-data php%s artisan config:cache && sudo -u www-data php%2$s artisan route:cache || true', escapeshellarg($root), $php),
        ]);

        $steps[] = Step::make('Set writable permissions', [
            sprintf('sudo chown -R www-data:www-data %s', escapeshellarg($root)),
            sprintf('sudo chmod -R 775 %s/storage %s/bootstrap/cache', escapeshellarg($root), escapeshellarg($root)),
        ]);

        return $steps;
    }

    /**
     * @return array<int, Step>
     */
    protected function wordpressSteps(): array
    {
        $site = $this->site;
        $root = $site->documentRoot();
        $database = $this->ensureDatabase();
        $adminPassword = $site->wp_admin_password ?: Str::password(20, symbols: false);

        if (blank($site->wp_admin_password)) {
            $site->update(['wp_admin_password' => $adminPassword]);
        }

        $wp = sprintf('sudo -u www-data wp --path=%s', escapeshellarg($root));

        return [
            Step::make('Download WordPress core', [
                'command -v wp >/dev/null || (curl -fsSL -o /tmp/wp-cli.phar https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar && sudo mv /tmp/wp-cli.phar /usr/local/bin/wp && sudo chmod +x /usr/local/bin/wp)',
                sprintf('%s core is-installed 2>/dev/null || %1$s core download --force', $wp),
            ]),

            Step::make('Write wp-config.php', [
                sprintf(
                    '%s config create --dbname=%s --dbuser=%s --dbpass=%s --dbhost=127.0.0.1 --skip-check --force',
                    $wp,
                    escapeshellarg($database->name),
                    escapeshellarg((string) $database->username),
                    escapeshellarg((string) $database->password)
                ),
                sprintf('%s config set WP_DEBUG false --raw', $wp),
                sprintf('%s config set DISALLOW_FILE_EDIT true --raw', $wp),
            ]),

            Step::make('Install WordPress', [
                sprintf(
                    '%s core install --url=%s --title=%s --admin_user=%s --admin_password=%s --admin_email=%s --skip-email',
                    $wp,
                    escapeshellarg(($site->ssl ? 'https://' : 'http://').$site->domain),
                    escapeshellarg($site->wp_title ?: $site->domain),
                    escapeshellarg($site->wp_admin_user ?: 'admin'),
                    escapeshellarg($adminPassword),
                    escapeshellarg($site->wp_admin_email ?: ($site->user?->email ?: 'admin@'.$site->domain))
                ),
                sprintf('%s rewrite structure "/%%postname%%/" --hard || true', $wp),
                sprintf('%s plugin delete hello akismet 2>/dev/null || true', $wp),
            ]),

            Step::make('Set WordPress permissions', [
                sprintf('sudo chown -R www-data:www-data %s', escapeshellarg(rtrim($site->root_path, '/'))),
                sprintf('sudo find %s -type d -exec chmod 755 {} \;', escapeshellarg($root)),
                sprintf('sudo find %s -type f -exec chmod 644 {} \;', escapeshellarg($root)),
            ]),
        ];
    }

    /**
     * @return array<int, Step>
     */
    protected function nodeSteps(): array
    {
        $site = $this->site;
        $root = rtrim($site->root_path, '/');
        $build = $site->build_command ?: ($site->type === 'nextjs' ? 'npm run build' : '');

        $steps = $site->repository
            ? $this->cloneSteps()
            : [Step::call('Scaffold a starter app', function (LocalConnection $ssh) use ($site, $root) {
                $ssh->putFile($root.'/package.json', $this->starterPackageJson());
                $ssh->putFile($root.'/server.js', $this->starterServer());
                $ssh->mustRun('sudo chown -R www-data:www-data '.escapeshellarg($root));

                return 'Starter Node app scaffolded. Replace it with your repository when ready.';
            })];

        $steps[] = Step::make('Install dependencies', [
            sprintf(
                'cd %s && sudo -u www-data HOME=/tmp npm %s',
                escapeshellarg($root),
                'install --omit=dev --no-audit --no-fund'
            ),
        ]);

        if ($build !== '') {
            $steps[] = Step::make('Build the application', [
                sprintf('cd %s && sudo -u www-data HOME=/tmp %s', escapeshellarg($root), $build),
            ]);
        }

        $steps[] = Step::call('Create the systemd service', function (LocalConnection $ssh) use ($site) {
            $ssh->putFile(NginxVhost::unitPath($site), NginxVhost::systemdUnit($site));
            $ssh->mustRun('sudo systemctl daemon-reload');
            $ssh->mustRun('sudo systemctl enable '.$site->serviceName());

            return 'systemd unit '.$site->serviceName().' installed';
        });

        $steps[] = Step::make('Start the application', [
            'sudo systemctl restart '.$site->serviceName(),
            'sleep 3',
            'sudo systemctl is-active '.$site->serviceName(),
            sprintf('curl -sS -o /dev/null -w "app responded with HTTP %%{http_code}\n" http://127.0.0.1:%d/ || echo "app not answering yet — check the service log"', $site->app_port),
        ]);

        return $steps;
    }

    /**
     * @return array<int, Step>
     */
    protected function cloneSteps(): array
    {
        $site = $this->site;
        $root = rtrim($site->root_path, '/');

        return [
            Step::make('Clone the repository', [
                sprintf('sudo rm -rf %s && sudo mkdir -p %1$s && sudo chown www-data:www-data %1$s', escapeshellarg($root)),
                sprintf(
                    'sudo -u www-data git clone --depth 1 --branch %s %s %s',
                    escapeshellarg($site->branch ?: 'main'),
                    escapeshellarg($site->repository),
                    escapeshellarg($root)
                ),
            ]),
        ];
    }

    /** Create the Database record for a WordPress/Laravel site if it has none. */
    public function ensureDatabase(): Database
    {
        $site = $this->site;

        if ($site->database) {
            return $site->database;
        }

        $base = Str::of($site->domain)->replaceMatches('/[^a-z0-9]+/i', '_')->lower()->limit(40, '')->toString();
        $suffix = Str::lower(Str::random(4));

        $database = Database::create([
            'user_id' => $site->user_id,
            'engine' => 'mysql',
            'name' => Str::limit($base, 50, '')."_{$suffix}",
            'username' => Str::limit($base, 20, '')."_{$suffix}",
            'password' => Str::password(24, symbols: false),
            'charset' => 'utf8mb4',
            'status' => 'pending',
            'managed_by_site' => true,
        ]);

        $site->update(['database_id' => $database->id]);
        $site->setRelation('database', $database);

        return $database;
    }

    protected function laravelEnv(Database $database): string
    {
        $site = $this->site;
        $scheme = $site->ssl ? 'https' : 'http';

        return <<<ENV
        APP_NAME="{$site->domain}"
        APP_ENV=production
        APP_KEY=
        APP_DEBUG=false
        APP_URL={$scheme}://{$site->domain}

        LOG_CHANNEL=stack
        LOG_LEVEL=error

        DB_CONNECTION=mysql
        DB_HOST=127.0.0.1
        DB_PORT=3306
        DB_DATABASE={$database->name}
        DB_USERNAME={$database->username}
        DB_PASSWORD="{$database->password}"

        SESSION_DRIVER=database
        QUEUE_CONNECTION=database
        CACHE_STORE=database
        ENV;
    }

    protected function starterPackageJson(): string
    {
        return <<<'JSON'
        {
          "name": "ubuntu-panel-app",
          "private": true,
          "version": "1.0.0",
          "scripts": {
            "start": "node server.js"
          }
        }
        JSON;
    }

    protected function starterServer(): string
    {
        $domain = $this->site->domain;

        return <<<JS
        const http = require('http');
        const port = process.env.PORT || 3000;

        http
          .createServer((req, res) => {
            res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
            res.end('<h1>{$domain} is live</h1><p>Deployed by Ubuntu Panel. Point this site at your repository to replace this starter.</p>');
          })
          .listen(port, '127.0.0.1', () => console.log('listening on ' + port));
        JS;
    }

    /**
     * Obtain a certificate without touching our vhost — certbot's --nginx
     * installer would rewrite the managed file and the next deploy would undo it.
     *
     * When the site's DNS is on Cloudflare we validate over DNS-01, which works
     * even with the orange cloud on; an HTTP-01 challenge would be answered by
     * Cloudflare rather than by the origin.
     */
    protected function requestCertificate(LocalConnection $ssh): string
    {
        $site = $this->site->fresh();
        $domains = implode(' ', array_map(fn ($d) => '-d '.escapeshellarg($d), $site->hostnames()));
        $email = escapeshellarg($site->user?->email ?: 'admin@'.$site->domain);

        $common = sprintf(
            'certonly --non-interactive --agree-tos -m %s %s --keep-until-expiring '.
            '--deploy-hook "systemctl reload nginx"',
            $email,
            $domains
        );

        $account = $site->cloudflareAccount;

        if ($site->manage_dns && $account) {
            $ssh->run('sudo DEBIAN_FRONTEND=noninteractive apt-get install -y python3-certbot-dns-cloudflare');

            $ssh->mustRun('sudo mkdir -p /etc/letsencrypt/panel && sudo chmod 700 /etc/letsencrypt/panel');
            $ssh->putFile(
                '/etc/letsencrypt/panel/cloudflare.ini',
                'dns_cloudflare_api_token = '.$account->api_token
            );
            $ssh->mustRun('sudo chmod 600 /etc/letsencrypt/panel/cloudflare.ini');

            [$output, $code] = $ssh->run(
                'sudo certbot '.$common.
                ' --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/panel/cloudflare.ini'.
                ' --dns-cloudflare-propagation-seconds 30'
            );

            return $code === 0
                ? "Certificate issued via Cloudflare DNS validation.\n".$output
                : "DNS validation failed; the site stays on HTTP.\n".$output;
        }

        [$output, $code] = $ssh->run(
            'sudo certbot '.$common.' --webroot -w '.escapeshellarg(NginxVhost::ACME_WEBROOT)
        );

        return $code === 0
            ? "Certificate issued.\n".$output
            : "certbot failed; the site stays on HTTP.\n".$output;
    }

    protected function placeholder(): string
    {
        $domain = $this->site->domain;

        return <<<PHP
        <?php
        // Placeholder created by Ubuntu Panel for {$domain}
        echo '<!doctype html><meta charset="utf-8"><title>{$domain}</title>';
        echo '<h1>{$domain} is live</h1>';
        echo '<p>Deployed by Ubuntu Panel. Replace this file with your application.</p>';
        PHP;
    }

    protected function placeholderHtml(): string
    {
        $domain = $this->site->domain;

        return <<<HTML
        <!doctype html>
        <meta charset="utf-8">
        <title>{$domain}</title>
        <h1>{$domain} is live</h1>
        <p>Deployed by Ubuntu Panel. Upload your static files to replace this page.</p>
        HTML;
    }
}
