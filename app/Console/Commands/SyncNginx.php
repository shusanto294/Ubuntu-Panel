<?php

namespace App\Console\Commands;

use App\Services\System\TerminalProxy;
use Illuminate\Console\Command;
use Throwable;

/**
 * Brings the panel's own nginx configuration back in line with this release.
 *
 * Panels installed before a vhost change do not get it from `panel:update` —
 * that pulls code, not server configuration — so this is the repair path, and
 * `panel:update` calls it on the way through.
 */
class SyncNginx extends Command
{
    protected $signature = 'panel:sync-nginx';

    protected $description = "Refresh the panel's nginx configuration (terminal websocket proxy)";

    public function handle(TerminalProxy $proxy): int
    {
        try {
            foreach ($proxy->sync() as $line) {
                $this->components->info($line);
            }
        } catch (Throwable $e) {
            $this->components->error($e->getMessage());

            return self::FAILURE;
        }

        return self::SUCCESS;
    }
}
