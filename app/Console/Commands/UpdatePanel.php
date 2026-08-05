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

            // No-ops unless Redis is installed, reachable and idle, so this is
            // safe on a panel that has never had it.
            Step::make('Move the cache and queue to Redis if it is ready', [
                sprintf('cd %s && php artisan panel:use-redis', escapeshellarg($root)),
            ]),
        ]);

        if (! $ok) {
            $this->reportFailure($log);

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
     * Say what went wrong, here, now.
     *
     * "See the activity log" is true and useless: the log is in a panel that
     * is very often the thing that has just stopped working, and this command
     * is being run from a shell that could simply have been told.
     */
    protected function reportFailure(ActivityLog $log): void
    {
        $log = $log->fresh();

        $this->newLine();
        $this->components->error('Update failed. The old code is still running.');

        if ($step = $log?->current_step) {
            $this->line('    Step:   '.$step);
        }

        if ($message = $log?->message) {
            $this->line('    Reason: '.$message);
        }

        // The last few lines of what the command actually printed, which is
        // where the cause usually is.
        $tail = collect(preg_split('/\r?\n/', (string) $log?->output))
            ->filter(fn (string $line) => trim($line) !== '')
            ->take(-12);

        if ($tail->isNotEmpty()) {
            $this->newLine();
            $this->line('    Last output:');

            foreach ($tail as $line) {
                $this->line('      '.$line);
            }
        }

        $this->newLine();
        $this->components->warn('Nothing was left half-applied; run it again once the cause is dealt with.');
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
        $script = $this->restartScript();

        [$output, $code] = $shell->run(sprintf(
            'sudo systemd-run --on-active=5s --unit=ubuntu-panel-restart --collect bash -c %s',
            escapeshellarg($script)
        ));

        if ($code === 0) {
            $this->components->info('Services restart in five seconds.');
            $this->line('    What happened lands in '.self::RESTART_LOG.'; `php artisan panel:doctor` reads it back.');

            return;
        }

        // No systemd (a container, a dev box): fall back to a detached restart.
        $shell->run('(sleep 5; '.$script.') >/dev/null 2>&1 &');

        $this->components->warn('Scheduled a detached restart ('.trim($output).').');
    }

    /** Where the detached restart leaves its account of what happened. */
    public const RESTART_LOG = '/var/log/ubuntu-panel-restart.log';

    /**
     * Restart the workers, and PHP-FPM with them.
     *
     * FPM is easy to forget because the code it serves is re-read on every
     * request — but the *caches* are not. `config:cache` writes a compiled
     * file that a long-lived FPM keeps in opcache, so a setting that changed
     * in this release goes on being served at its old value until something
     * restarts the pool. That is how the browser terminal kept being handed
     * an address that stopped being the default several versions ago.
     *
     * `daemon-reload` first: the unit files are written by `install.sh`, and
     * re-running the installer is a documented way to update. Restarting
     * without reloading starts the old definition of a unit that has since
     * changed — which looks exactly like a restart that did not happen.
     *
     * `enable --now` rather than `restart`: a unit that somehow ended up
     * disabled comes back, and one that is not installed on this machine is a
     * skip rather than a failure.
     *
     * And it says what it did. This runs detached — it has to, because it
     * restarts the process that scheduled it — so without a log the one thing
     * nobody can find out is whether the terminal daemon came back up.
     */
    protected function restartScript(): string
    {
        $fpm = sprintf('php%d.%d-fpm', PHP_MAJOR_VERSION, PHP_MINOR_VERSION);
        $log = self::RESTART_LOG;

        $units = [
            'ubuntu-panel-queue.service',
            'ubuntu-panel-terminal.service',
            'ubuntu-panel-scheduler.timer',
        ];

        $lines = [
            'exec >> '.escapeshellarg($log).' 2>&1;',
            'echo "=== $(date -Is) panel restart ===";',
            'systemctl daemon-reload;',
        ];

        foreach ($units as $unit) {
            $lines[] = sprintf(
                'if systemctl list-unit-files %1$s >/dev/null 2>&1; then '.
                'systemctl enable --now %1$s; systemctl restart %1$s; '.
                'echo "%1$s: $(systemctl is-active %1$s)"; '.
                'else echo "%1$s: not installed"; fi;',
                $unit
            );
        }

        $lines[] = "systemctl restart {$fpm} 2>/dev/null || true;";
        $lines[] = sprintf('echo "%s: $(systemctl is-active %s)";', $fpm, $fpm);

        return implode(' ', $lines);
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
