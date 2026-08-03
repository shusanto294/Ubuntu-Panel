<?php

namespace App\Console\Commands;

use App\Jobs\InstallServices;
use App\Models\Service;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Cache;

/**
 * Safety net for the install queue: if a worker died mid-install, or a job was
 * lost, this picks it back up on the next tick.
 */
class ProcessServiceQueue extends Command
{
    protected $signature = 'panel:process-service-queue';

    protected $description = 'Dispatch the next queued service install, if one is waiting';

    public function handle(): int
    {
        if (! Service::where('status', Service::QUEUED)->exists()) {
            $this->info('Nothing queued.');

            return self::SUCCESS;
        }

        $lock = Cache::lock('panel-service-install', 1);

        // A held lock means an install is already in flight; leave it alone.
        if (! $lock->get()) {
            $this->line('An install is already running.');

            return self::SUCCESS;
        }

        $lock->release();

        $this->line('Dispatching the next install.');

        InstallServices::dispatch();

        return self::SUCCESS;
    }
}
