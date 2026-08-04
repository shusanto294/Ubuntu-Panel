<?php

namespace App\Console\Commands;

use App\Services\Shell\LocalConnection;
use App\Support\Settings;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;
use Throwable;

/**
 * Moves the cache, sessions and the queue onto Redis.
 *
 * The installer sets all three to the database because that is the one thing
 * guaranteed to exist while the panel is being installed. It is also the most
 * expensive place to put them: every page view becomes session reads and writes
 * against MariaDB, and the queue worker runs a locking SELECT against it once a
 * second, for ever, on a machine where nothing is happening.
 *
 * Redis is installed by default and, until this runs, does nothing at all.
 *
 * Refuses rather than guesses. A panel that comes back up pointing at a Redis
 * it cannot reach is worse than one that is merely using more CPU than it needs
 * to, so every reason it might not work is checked before `.env` is touched.
 */
class UseRedis extends Command
{
    protected $signature = 'panel:use-redis
                            {--revert : Put the cache, sessions and queue back on the database}
                            {--force : Switch even with jobs still queued}';

    protected $description = 'Move the cache, sessions and queue onto Redis';

    protected const DRIVERS = [
        'CACHE_STORE' => 'redis',
        'SESSION_DRIVER' => 'redis',
        'QUEUE_CONNECTION' => 'redis',
    ];

    public function handle(Settings $settings, LocalConnection $shell): int
    {
        if ($this->option('revert')) {
            return $this->revert($shell);
        }

        if ($this->alreadyOnRedis()) {
            $this->components->info('Already using Redis.');

            return self::SUCCESS;
        }

        $password = (string) $settings->get('redis_password', '');

        $reasons = $this->blockers($password);

        if ($reasons !== []) {
            $this->components->warn('Staying on the database:');

            foreach ($reasons as $reason) {
                $this->line('    · '.$reason);
            }

            return self::SUCCESS;
        }

        $this->write([
            'REDIS_HOST' => '127.0.0.1',
            'REDIS_PORT' => '6379',
            'REDIS_PASSWORD' => $password,
        ] + self::DRIVERS);

        $this->components->info('Cache, sessions and the queue are on Redis.');
        $this->components->warn('Everyone signed in has been signed out — the sessions were in the old store.');

        $this->restart($shell);

        return self::SUCCESS;
    }

    /**
     * Every reason this would not work — all of them, not the first.
     *
     * Reporting one at a time turns a five-second answer into a conversation:
     * fix that, run again, discover the next. They are all cheap to check.
     *
     * @return array<int, string>
     */
    protected function blockers(string $password): array
    {
        $reasons = [];

        if (! extension_loaded('redis')) {
            $reasons[] = 'the phpredis extension is not loaded. Install the PHP service, then try again.';
        }

        if ($password === '') {
            $reasons[] = 'no Redis password is recorded, so Redis has not been set up by this panel yet.';
        } elseif (extension_loaded('redis')) {
            try {
                config([
                    'database.redis.default.password' => $password,
                    'database.redis.cache.password' => $password,
                ]);

                if (app('redis')->connection()->ping() === false) {
                    $reasons[] = 'Redis did not answer a ping.';
                }
            } catch (Throwable $e) {
                $reasons[] = 'Redis is not reachable — '.$e->getMessage();
            }
        }

        // Jobs live in whichever store they were dispatched to. Switching with
        // work still queued would strand it in a table nothing reads any more.
        if (! $this->option('force') && ($pending = $this->pendingJobs()) > 0) {
            $reasons[] = "{$pending} job(s) are still queued. Let them finish, or pass --force to abandon them.";
        }

        return $reasons;
    }

    protected function pendingJobs(): int
    {
        try {
            return DB::table('jobs')->count();
        } catch (Throwable $e) {
            return 0;
        }
    }

    protected function revert(LocalConnection $shell): int
    {
        $this->write([
            'CACHE_STORE' => 'database',
            'SESSION_DRIVER' => 'database',
            'QUEUE_CONNECTION' => 'database',
        ]);

        $this->components->info('Back on the database.');

        $this->restart($shell);

        return self::SUCCESS;
    }

    protected function alreadyOnRedis(): bool
    {
        return config('queue.default') === 'redis'
            && config('session.driver') === 'redis'
            && config('cache.default') === 'redis';
    }

    /**
     * @param  array<string, string>  $values
     */
    protected function write(array $values): void
    {
        $path = base_path('.env');

        if (! is_writable($path)) {
            $this->components->error(".env is not writable, so nothing was changed.");

            return;
        }

        $contents = (string) file_get_contents($path);

        foreach ($values as $key => $value) {
            $line = $key.'='.$this->quote($value);

            $contents = preg_match('/^'.preg_quote($key, '/').'=.*$/m', $contents)
                ? preg_replace('/^'.preg_quote($key, '/').'=.*$/m', $line, $contents)
                : rtrim($contents, "\n")."\n".$line."\n";
        }

        file_put_contents($path, $contents);

        // The cached config is what the web process actually reads.
        $this->callSilently('config:cache');
    }

    /** Passwords have characters `.env` parses if they are left bare. */
    protected function quote(string $value): string
    {
        return preg_match('/[\s"\'#=]/', $value) ? '"'.addcslashes($value, '"\\').'"' : $value;
    }

    /**
     * The workers hold their connections open, and PHP-FPM holds the compiled
     * config, so neither notices a driver change until it is restarted.
     */
    protected function restart(LocalConnection $shell): void
    {
        $fpm = sprintf('php%d.%d-fpm', PHP_MAJOR_VERSION, PHP_MINOR_VERSION);

        $shell->run(sprintf(
            'sudo systemd-run --on-active=3s --unit=ubuntu-panel-drivers --collect bash -c %s',
            escapeshellarg(
                'systemctl restart ubuntu-panel-queue.service ubuntu-panel-terminal.service; '.
                "systemctl restart {$fpm} 2>/dev/null || true"
            )
        ));

        $this->components->info('Services restart in a moment.');
    }
}
