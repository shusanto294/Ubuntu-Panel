<?php

namespace App\Services\Sites;

use App\Models\ActivityLog;
use App\Models\Site;
use App\Services\Cloudflare\CloudflareDnsManager;
use App\Services\Databases\DatabaseManager;
use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use App\Services\Tasks\TaskRunner;
use Throwable;

class SiteManager
{
    public function __construct(
        protected CloudflareDnsManager $dns,
        protected DatabaseManager $databases,
    ) {}

    /**
     * Deploy the site: directory, application, vhost, DNS and certificate.
     */
    public function create(Site $site): bool
    {
        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'site',
            'action' => 'site.deploy',
            'status' => 'running',
            'message' => $site->domain,
        ]);

        $site->update(['status' => 'deploying']);
        $connection = app(LocalConnection::class)->timeout(1800);

        try {
            $steps = (new SiteRecipe($site, $this->databases))->steps();
            $ok = TaskRunner::for($log, $connection)->run($steps);

            $site->update([
                'status' => $ok ? 'active' : 'failed',
                'last_error' => $ok ? null : $log->fresh()->message,
            ]);

            return $ok;
        } catch (Throwable $e) {
            $site->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    /**
     * Remove vhost, service, files, database and DNS records, then delete the record.
     */
    public function delete(Site $site, bool $deleteFiles = true): bool
    {
        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'site',
            'action' => 'site.delete',
            'status' => 'running',
            'message' => $site->domain,
        ]);

        $site->update(['status' => 'deleting']);
        $connection = app(LocalConnection::class)->timeout(600);

        try {
            $steps = [];

            // DNS first — once the record is gone the site is unreachable anyway.
            if ($site->manage_dns) {
                $steps[] = Step::call('Remove DNS records', fn () => $this->dns->deleteForSite($site), optional: true);
            }

            if ($site->isProxied()) {
                $steps[] = Step::make('Stop the application service', [
                    'sudo systemctl disable --now '.$site->serviceName().' || true',
                    'sudo rm -f '.escapeshellarg(NginxVhost::unitPath($site)),
                    'sudo rm -f '.escapeshellarg('/var/log/'.$site->serviceName().'.log'),
                    'sudo systemctl daemon-reload',
                ], optional: true);
            }

            $steps[] = Step::make('Remove the nginx vhost', [
                'sudo rm -f '.escapeshellarg(NginxVhost::enabledPath($site)),
                'sudo rm -f '.escapeshellarg(NginxVhost::availablePath($site)),
                'sudo nginx -t',
                'sudo systemctl reload nginx',
            ]);

            // Only databases the panel created for this site are dropped.
            if ($site->database && $site->database->managed_by_site) {
                $steps[] = Step::call('Drop the site database', function () use ($site) {
                    $this->databases->delete($site->database);

                    return 'Dropped database '.$site->database->name;
                }, optional: true);
            }

            if ($deleteFiles) {
                $steps[] = Step::make('Delete site files', [
                    'sudo rm -rf '.escapeshellarg(rtrim($site->root_path, '/')),
                ]);
            }

            $ok = TaskRunner::for($log, $connection)->run($steps);

            if ($ok) {
                $site->delete();
            } else {
                $site->update(['status' => 'failed', 'last_error' => $log->fresh()->message]);
            }

            return $ok;
        } catch (Throwable $e) {
            $site->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    /** Restart a node-family app. */
    public function restart(Site $site): bool
    {
        if (! $site->isProxied()) {
            return false;
        }

        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'site',
            'action' => 'site.restart',
            'status' => 'running',
            'message' => $site->domain,
        ]);

        $connection = app(LocalConnection::class)->timeout(180);

        try {
            return TaskRunner::for($log, $connection)->run([
                Step::make('Restart '.$site->serviceName(), [
                    'sudo systemctl restart '.$site->serviceName(),
                    'sleep 2',
                    'sudo systemctl status '.$site->serviceName().' --no-pager --lines=20',
                ]),
            ]);
        } finally {
            $connection->disconnect();
        }
    }

    /**
     * Pull the latest commit and restart/rebuild. Only for git-backed sites.
     */
    public function deployLatest(Site $site): bool
    {
        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'site',
            'action' => 'site.pull',
            'status' => 'running',
            'message' => $site->domain,
        ]);

        $root = rtrim($site->root_path, '/');
        $connection = app(LocalConnection::class)->timeout(900);

        try {
            $steps = [
                Step::make('Pull the latest commit', [
                    sprintf('cd %s && sudo -u www-data git fetch --depth 1 origin %s', escapeshellarg($root), escapeshellarg($site->branch ?: 'main')),
                    sprintf('cd %s && sudo -u www-data git reset --hard origin/%s', escapeshellarg($root), $site->branch ?: 'main'),
                ]),
            ];

            if ($site->type === 'laravel') {
                $steps[] = Step::make('Update dependencies and migrate', [
                    sprintf('cd %s && sudo -u www-data COMPOSER_HOME=/tmp/composer composer install --no-dev --optimize-autoloader --no-interaction', escapeshellarg($root)),
                    sprintf('cd %s && sudo -u www-data php%s artisan migrate --force', escapeshellarg($root), $site->php_version),
                    sprintf('cd %s && sudo -u www-data php%s artisan config:cache && sudo -u www-data php%2$s artisan route:cache', escapeshellarg($root), $site->php_version),
                ]);
            }

            if ($site->isProxied()) {
                $steps[] = Step::make('Reinstall and rebuild', array_values(array_filter([
                    sprintf('cd %s && sudo -u www-data HOME=/tmp npm install --omit=dev --no-audit --no-fund', escapeshellarg($root)),
                    $site->build_command || $site->type === 'nextjs'
                        ? sprintf('cd %s && sudo -u www-data HOME=/tmp %s', escapeshellarg($root), $site->build_command ?: 'npm run build')
                        : null,
                ])));

                $steps[] = Step::make('Restart the service', [
                    'sudo systemctl restart '.$site->serviceName(),
                    'sleep 2',
                    'sudo systemctl is-active '.$site->serviceName(),
                ]);
            }

            return TaskRunner::for($log, $connection)->run($steps);
        } finally {
            $connection->disconnect();
        }
    }
}
