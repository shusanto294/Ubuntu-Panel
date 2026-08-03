<?php

namespace App\Jobs;

use App\Models\Site;
use App\Services\Sites\SiteManager;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

class DeploySiteUpdate implements ShouldQueue
{
    use Queueable;

    public int $timeout = 900;

    public int $tries = 1;

    public function __construct(public Site $site) {}

    public function handle(SiteManager $manager): void
    {
        $manager->deployLatest($this->site);
    }
}
