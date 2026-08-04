<?php

namespace App\Console\Commands;

use App\Models\Service;
use App\Services\System\ServiceCatalog;
use App\Services\System\ServiceInstaller;
use Illuminate\Console\Command;

/**
 * Re-reads what is actually on the machine.
 *
 * The service rows record what installs did, which drifts from what is on the
 * box — a batch that failed halfway leaves rows saying `failed` for software
 * that installed fine, and anything installed outside the panel is invisible
 * until someone asks. Scheduled, so the panel is not relying on the user to
 * press a button before it will admit MariaDB is there.
 */
class DetectServices extends Command
{
    protected $signature = 'panel:detect-services';

    protected $description = 'Re-read which software is installed on this machine';

    public function handle(ServiceInstaller $installer): int
    {
        $installer->detect();

        $installed = Service::query()->where('status', Service::INSTALLED)->pluck('key')->all();

        $this->components->info(
            $installed === []
                ? 'Nothing from the catalogue is installed.'
                : count($installed).' installed: '.implode(', ', array_map(
                    fn ($key) => ServiceCatalog::label($key),
                    $installed
                ))
        );

        return self::SUCCESS;
    }
}
