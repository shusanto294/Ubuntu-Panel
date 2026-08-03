<?php

namespace App\Jobs;

use App\Models\Database;
use App\Services\Databases\DatabaseManager;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

class CreateDatabase implements ShouldQueue
{
    use Queueable;

    public int $timeout = 300;

    public int $tries = 1;

    public function __construct(public Database $database) {}

    public function handle(DatabaseManager $manager): void
    {
        $manager->create($this->database);
    }
}
