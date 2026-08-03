<?php

namespace App\Jobs;

use App\Models\Site;
use App\Services\Sites\SiteManager;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

class DeleteSite implements ShouldQueue
{
    use Queueable;

    public int $timeout = 600;

    public int $tries = 1;

    public function __construct(public Site $site, public bool $deleteFiles = true) {}

    public function handle(SiteManager $manager): void
    {
        $manager->delete($this->site, $this->deleteFiles);
    }
}
