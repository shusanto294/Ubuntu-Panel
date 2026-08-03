<?php

namespace App\Jobs;

use App\Services\System\ServiceInstaller;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Cache;

/**
 * Installs everything queued in one pass.
 *
 * Not one job per service: dpkg takes an exclusive lock, so parallel apt runs
 * would only queue behind each other (or fail outright). Batching every package
 * into a single transaction is the version of "all at once" that actually goes
 * faster — one dependency resolution, one download pass, one dpkg run.
 */
class InstallServices implements ShouldQueue
{
    use Queueable;

    public int $timeout = 3600;

    public int $tries = 1;

    public function __construct(public bool $force = false) {}

    public function handle(ServiceInstaller $installer): void
    {
        $lock = Cache::lock($this->lockKey(), 3600);

        if (! $lock->get()) {
            // Another worker is already running an install.
            return;
        }

        try {
            $installer->installQueued($this->force);
        } finally {
            $lock->release();
        }

        // Anything queued while that ran gets picked up straight away.
        if ($installer->nextQueued()) {
            self::dispatch();
        }
    }

    public function lockKey(): string
    {
        return 'panel-service-install';
    }
}
