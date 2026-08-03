<?php

namespace Tests\Concerns;

use App\Models\Service;
use App\Services\System\ServiceCatalog;

trait InstallsServices
{
    /**
     * Give the machine the service rows a real install would have produced.
     *
     * @param  array<int, string>  $installed
     */
    protected function markInstalled(array $installed): void
    {
        foreach (ServiceCatalog::keys() as $key) {
            $present = in_array($key, $installed, true);

            Service::updateOrCreate(
                ['key' => $key],
                [
                    'status' => $present ? Service::INSTALLED : Service::NOT_INSTALLED,
                    'sort_order' => ServiceCatalog::sortOrder($key),
                    'installed_at' => $present ? now() : null,
                ]
            );
        }
    }
}
