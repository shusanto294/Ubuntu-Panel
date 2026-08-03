<?php

namespace App\Jobs;

use App\Models\ActivityLog;
use App\Services\System\PanelDomain;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Throwable;

/**
 * Moves the panel onto a hostname from the UI.
 *
 * Queued rather than done in the request: issuing a certificate takes a while
 * and reloads nginx underneath the very request that asked for it.
 */
class ConfigurePanelDomain implements ShouldQueue
{
    use Queueable;

    public int $timeout = 600;

    public int $tries = 1;

    public function __construct(public string $domain, public ?string $email = null) {}

    public function handle(PanelDomain $panel): void
    {
        $log = ActivityLog::record([
            'type' => 'provision',
            'action' => 'panel.domain',
            'status' => 'running',
            'message' => 'Setting the panel up on '.$this->domain,
            'started_at' => now(),
            'progress' => 10,
        ]);

        try {
            $lines = $panel->apply($this->domain, $this->email);

            $log->forceFill([
                'status' => 'success',
                'message' => 'The panel now answers on '.$this->domain,
                'output' => implode("\n", $lines),
                'progress' => 100,
                'finished_at' => now(),
            ])->save();
        } catch (Throwable $e) {
            $log->forceFill([
                'status' => 'failed',
                'message' => $e->getMessage(),
                'progress' => 100,
                'finished_at' => now(),
            ])->save();
        }
    }
}
