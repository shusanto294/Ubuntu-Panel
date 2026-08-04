<?php

namespace App\Console\Commands;

use App\Models\ActivityLog;
use App\Services\Shell\LocalConnection;
use App\Services\System\UpdateChecker;
use App\Services\Tasks\Step;
use App\Services\Tasks\TaskRunner;
use Illuminate\Console\Command;

/**
 * Updates the panel in place: pull, install, build, migrate, restart.
 *
 * The restart is deliberately the last thing and is detached, because this
 * command is usually running inside one of the services it restarts — killing
 * itself halfway through an update is how installs end up broken.
 */
class UpdatePanel extends Command
{
    protected $signature = 'panel:update
                            {--force : Update even when already on the latest commit}
                            {--no-restart : Leave the services running the old code}';

    protected $description = 'Update the panel to the latest published version';

    public function handle(UpdateChecker $checker, LocalConnection $shell): int
    {
        $status = $checker->status(fresh: true);

        $this->components->info('Installed: '.$this->describe($status['current']));

        if (($status['latest']['error'] ?? null) !== null) {
            $this->components->warn('Could not reach GitHub: '.$status['latest']['error']);
        } else {
            $this->components->info('Published: '.$this->describe($status['latest']));
        }

        if (! $status['available'] && ! $this->option('force')) {
            $this->components->info('Already up to date. Use --force to reinstall anyway.');

            return self::SUCCESS;
        }

        $log = ActivityLog::record([
            'type' => 'provision',
            'action' => 'panel.update',
            'status' => 'running',
            'message' => 'Updating the panel',
            'started_at' => now(),
        ]);

        $root = base_path();
        $user = $checker->panelUser();

        $ok = TaskRunner::for($log, $shell->timeout(1800))->run([
            Step::make('Fetch the latest code', [
                sprintf('cd %s && sudo -u %s git fetch --all --prune', escapeshellarg($root), escapeshellarg($user)),
                sprintf('cd %s && sudo -u %s git reset --hard origin/%s', escapeshellarg($root), escapeshellarg($user), escapeshellarg(config('panel.update_branch', 'main'))),
            ]),

            Step::make('Install PHP dependencies', [
                sprintf('cd %s && sudo -u %s composer install --no-dev --no-interaction --optimize-autoloader', escapeshellarg($root), escapeshellarg($user)),
            ]),

            Step::make('Build the assets', [
                sprintf('cd %s && sudo -u %s npm ci --no-audit --no-fund', escapeshellarg($root), escapeshellarg($user)),
                sprintf('cd %s && sudo -u %s npm run build', escapeshellarg($root), escapeshellarg($user)),
            ]),

            Step::make('Apply database changes', [
                sprintf('cd %s && sudo -u %s php artisan migrate --force', escapeshellarg($root), escapeshellarg($user)),
            ]),

            Step::make('Refresh caches', [
                sprintf('cd %s && sudo -u %s php artisan config:cache', escapeshellarg($root), escapeshellarg($user)),
                sprintf('cd %s && sudo -u %s php artisan route:cache', escapeshellarg($root), escapeshellarg($user)),
            ]),

            // Server configuration does not arrive with the code, so a panel
            // installed before a vhost change has to be brought up to it here.
            Step::make('Update the nginx configuration', [
                sprintf('cd %s && php artisan panel:sync-nginx', escapeshellarg($root)),
            ]),
        ]);

        if (! $ok) {
            $this->components->error('Update failed — see the activity log. The old code is still running.');

            return self::FAILURE;
        }

        if (! $this->option('no-restart')) {
            $this->restartServices($shell);
        }

        $this->reloadConfig();

        $this->components->info('Updated to '.$this->describe($checker->status(fresh: true)['current']));

        return self::SUCCESS;
    }

    /**
     * Pick up the config the update just wrote.
     *
     * This process read its configuration at boot, before the new code
     * existed — `config:cache` ran in a subprocess and cannot reach back into
     * this one. Without this the closing line reports the version the command
     * started with, so an update that changed the version number appears to
     * have changed nothing.
     */
    protected function reloadConfig(): void
    {
        $path = base_path('config/panel.php');

        if (! is_readable($path)) {
            return;
        }

        $fresh = require $path;

        if (is_array($fresh)) {
            config(['panel' => $fresh]);
        }
    }

    /**
     * Restart out of band, a few seconds from now.
     *
     * systemd-run schedules it as its own transient unit, so the restart is not
     * a child of this process and survives this process being restarted.
     */
    protected function restartServices(LocalConnection $shell): void
    {
        $services = 'ubuntu-panel-queue.service ubuntu-panel-terminal.service';

        [$output, $code] = $shell->run(
            "sudo systemd-run --on-active=5s --unit=ubuntu-panel-restart --collect systemctl restart {$services}"
        );

        if ($code === 0) {
            $this->components->info('Services restart in five seconds.');

            return;
        }

        // No systemd (a container, a dev box): fall back to a detached restart.
        $shell->run("(sleep 5; sudo systemctl restart {$services}) >/dev/null 2>&1 &");

        $this->components->warn('Scheduled a detached restart ('.trim($output).').');
    }

    /**
     * @param  array<string, mixed>  $version
     */
    protected function describe(array $version): string
    {
        return collect([
            $version['version'] ?? null,
            isset($version['commit']) ? 'commit '.$version['commit'] : null,
        ])->filter()->implode(' · ') ?: 'unknown';
    }
}
