<?php

namespace App\Services\System;

use App\Models\MetricSample;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;

/**
 * The recorded past of this machine's CPU, memory and disk.
 *
 * Samples land once a minute. Reading them back a month at a time would be
 * 43,000 rows for a graph 300 pixels wide, so every range is averaged into
 * buckets by the database and comes back as roughly a hundred points — small
 * enough to send on every range change and dense enough that nothing visible
 * is lost.
 */
class MetricHistory
{
    /**
     * The ranges the dashboard offers.
     *
     * `bucket` is chosen so each range lands near a hundred points; anything
     * denser is finer than the graph can draw.
     *
     * @var array<string, array{label: string, window: int, bucket: int}>
     */
    public const RANGES = [
        '1h' => ['label' => '1 hour', 'window' => 3600, 'bucket' => 60],
        '24h' => ['label' => '24 hours', 'window' => 86400, 'bucket' => 900],
        '7d' => ['label' => '7 days', 'window' => 604800, 'bucket' => 3600],
        '30d' => ['label' => '30 days', 'window' => 2592000, 'bucket' => 21600],
    ];

    public const DEFAULT_RANGE = '1h';

    /** Older than this and a sample is dropped; the longest range is 30 days. */
    public const RETAIN_DAYS = 35;

    /** Record one reading. */
    public function record(array $metrics, ?Carbon $at = null): MetricSample
    {
        return MetricSample::create([
            'cpu_percent' => $metrics['cpu']['usage'] ?? null,
            'memory_percent' => $metrics['memory']['percent'] ?? 0,
            'disk_percent' => $metrics['disk']['percent'] ?? 0,
            'swap_percent' => $metrics['swap']['percent'] ?? null,
            'memory_used' => (int) ($metrics['memory']['used'] ?? 0),
            'memory_total' => (int) ($metrics['memory']['total'] ?? 0),
            'disk_used' => (int) ($metrics['disk']['used'] ?? 0),
            'disk_total' => (int) ($metrics['disk']['total'] ?? 0),
            'load_1' => $metrics['load'][0] ?? null,
            'sampled_at' => $at ?? now(),
        ]);
    }

    /** Drop samples past the retention window. Returns how many went. */
    public function prune(): int
    {
        return MetricSample::where('sampled_at', '<', now()->subDays(self::RETAIN_DAYS))->delete();
    }

    public static function isRange(string $range): bool
    {
        return array_key_exists($range, self::RANGES);
    }

    /**
     * The ranges as the browser needs them: key, label, and how often a graph
     * on that range is worth refetching.
     *
     * @return array<int, array{key: string, label: string, refresh_ms: int}>
     */
    public static function options(): array
    {
        $options = [];

        foreach (self::RANGES as $key => $range) {
            $options[] = [
                'key' => $key,
                'label' => $range['label'],
                // No point asking again before the next bucket can have moved.
                'refresh_ms' => min(60, $range['bucket']) * 1000,
            ];
        }

        return $options;
    }

    /**
     * Averaged points for a range, oldest first.
     *
     * @return array{range: string, label: string, bucket_seconds: int, from: string, to: string, points: array<int, array<string, float|int|null>>}
     */
    public function series(string $range = self::DEFAULT_RANGE): array
    {
        $range = self::isRange($range) ? $range : self::DEFAULT_RANGE;
        $spec = self::RANGES[$range];

        $to = now();
        $from = $to->copy()->subSeconds($spec['window']);

        $bucket = (int) $spec['bucket'];

        $rows = MetricSample::query()
            ->where('sampled_at', '>=', $from)
            ->groupBy('bucket')
            ->orderBy('bucket')
            ->get([
                DB::raw($this->bucketExpression($bucket).' AS bucket'),
                DB::raw('AVG(cpu_percent) AS cpu'),
                DB::raw('AVG(memory_percent) AS memory'),
                DB::raw('AVG(disk_percent) AS disk'),
                DB::raw('AVG(memory_used) AS memory_used'),
                DB::raw('AVG(disk_used) AS disk_used'),
                DB::raw('MAX(memory_total) AS memory_total'),
                DB::raw('MAX(disk_total) AS disk_total'),
            ]);

        $points = $rows->map(fn ($row) => [
            't' => (int) $row->bucket * $bucket,
            'cpu' => $this->round($row->cpu),
            'memory' => $this->round($row->memory),
            'disk' => $this->round($row->disk),
            'memory_used' => $row->memory_used === null ? null : (int) $row->memory_used,
            'memory_total' => $row->memory_total === null ? null : (int) $row->memory_total,
            'disk_used' => $row->disk_used === null ? null : (int) $row->disk_used,
            'disk_total' => $row->disk_total === null ? null : (int) $row->disk_total,
        ])->values()->all();

        return [
            'range' => $range,
            'label' => $spec['label'],
            'bucket_seconds' => $bucket,
            'from' => $from->toIso8601String(),
            'to' => $to->toIso8601String(),
            'points' => $points,
        ];
    }

    /** How far back the recorded history actually goes, in seconds. */
    public function depthSeconds(): int
    {
        $oldest = MetricSample::min('sampled_at');

        return $oldest === null ? 0 : max(0, now()->diffInSeconds(Carbon::parse($oldest), absolute: true));
    }

    /**
     * Which bucket a row falls in: its timestamp divided by the bucket width,
     * floored, in this connection's dialect.
     *
     * Bucketing has to happen in the database — the whole point is not to pull
     * a month of rows into PHP — and there is no portable spelling of it.
     * Every branch below truncates rather than rounds, which for timestamps
     * (always positive) is the floor we want; MySQL's `CAST(… AS SIGNED)`
     * rounds, so it is deliberately not used here.
     */
    protected function bucketExpression(int $bucket): string
    {
        return match (DB::connection()->getDriverName()) {
            'sqlite' => "CAST(strftime('%s', sampled_at) AS INTEGER) / {$bucket}",
            'pgsql' => "FLOOR(EXTRACT(EPOCH FROM sampled_at) / {$bucket})::bigint",
            'sqlsrv' => "DATEDIFF(second, '1970-01-01', sampled_at) / {$bucket}",
            default => "UNIX_TIMESTAMP(sampled_at) DIV {$bucket}",
        };
    }

    protected function round(mixed $value): ?float
    {
        return $value === null ? null : round((float) $value, 1);
    }
}
