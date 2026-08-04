<?php

namespace App\Console\Commands;

use App\Services\System\MetricHistory;
use App\Services\System\SystemMetrics;
use Illuminate\Console\Command;

/**
 * Records one reading of this machine, once a minute, for the graphs.
 *
 * The dashboard's live figures never come from here — those are read straight
 * from /proc on request. This exists only so a graph can show yesterday.
 */
class CollectMetrics extends Command
{
    protected $signature = 'panel:collect-metrics';

    protected $description = 'Record a CPU, memory and disk sample for the dashboard graphs';

    public function handle(SystemMetrics $metrics, MetricHistory $history): int
    {
        // CPU is the delta between two readings and this process has taken
        // none, so the first read only primes the counters. A second one a
        // moment later is what actually has a percentage in it.
        $metrics->read();
        usleep(500_000);

        $sample = $history->record($metrics->read());

        $pruned = $history->prune();

        $this->components->info(sprintf(
            'CPU %s · memory %.1f%% · disk %.1f%%%s',
            $sample->cpu_percent === null ? '—' : number_format($sample->cpu_percent, 1).'%',
            $sample->memory_percent,
            $sample->disk_percent,
            $pruned > 0 ? sprintf(' · pruned %d old %s', $pruned, str('sample')->plural($pruned)) : '',
        ));

        return self::SUCCESS;
    }
}
