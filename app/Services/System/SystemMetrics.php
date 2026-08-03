<?php

namespace App\Services\System;

/**
 * CPU, memory and disk for the machine the panel runs on.
 *
 * Reading /proc locally costs microseconds, so there is no sampling daemon, no
 * queue job and no stored history: the page asks, the kernel answers. CPU usage
 * is a delta between two readings, so the first call in a process returns null
 * for it and the browser simply polls again a second later.
 */
class SystemMetrics
{
    /** Where the previous CPU counters live between requests. */
    protected const CACHE_KEY = 'system-metrics:cpu';

    public function read(): array
    {
        $cpu = $this->cpu();

        return [
            'cpu' => $cpu,
            'memory' => $this->memory(),
            'swap' => $this->swap(),
            'disk' => $this->disk(),
            'load' => $this->load(),
            'uptime_seconds' => $this->uptime(),
            'sampled_at' => now()->toIso8601String(),
        ];
    }

    /**
     * Busy time since the previous reading.
     *
     * The counters are cumulative, so a percentage only means something against
     * an earlier sample; the last one is kept in the cache, which is shared
     * across requests and workers.
     */
    protected function cpu(): array
    {
        $cores = $this->cores();
        $line = $this->firstLine('/proc/stat', 'cpu ');

        if ($line === null) {
            return ['usage' => null, 'cores' => $cores];
        }

        $fields = array_values(array_filter(explode(' ', trim($line)), fn ($v) => $v !== '' && is_numeric($v)));

        if ($fields === []) {
            return ['usage' => null, 'cores' => $cores];
        }

        $total = array_sum(array_map('floatval', $fields));
        // idle + iowait
        $idle = (float) ($fields[3] ?? 0) + (float) ($fields[4] ?? 0);

        $previous = cache()->get(self::CACHE_KEY);
        cache()->put(self::CACHE_KEY, ['total' => $total, 'idle' => $idle], now()->addMinutes(5));

        if (! $previous) {
            return ['usage' => null, 'cores' => $cores];
        }

        $totalDelta = $total - (float) $previous['total'];
        $idleDelta = $idle - (float) $previous['idle'];

        // No time has passed, or the counters were reset by a reboot.
        if ($totalDelta <= 0 || $idleDelta < 0) {
            return ['usage' => null, 'cores' => $cores];
        }

        return [
            'usage' => round(max(0, min(100, (1 - $idleDelta / $totalDelta) * 100)), 1),
            'cores' => $cores,
        ];
    }

    protected function memory(): array
    {
        $values = $this->meminfo();

        $total = $values['MemTotal'] ?? 0;
        $available = $values['MemAvailable'] ?? 0;
        $used = max(0, $total - $available);

        return [
            'total' => $total,
            'used' => $used,
            'available' => $available,
            'percent' => $this->percent($used, $total),
        ];
    }

    protected function swap(): array
    {
        $values = $this->meminfo();

        $total = $values['SwapTotal'] ?? 0;
        $used = max(0, $total - ($values['SwapFree'] ?? 0));

        return [
            'total' => $total,
            'used' => $used,
            'percent' => $this->percent($used, $total),
        ];
    }

    protected function disk(): array
    {
        $total = @disk_total_space('/') ?: 0;
        $free = @disk_free_space('/') ?: 0;
        $used = max(0, $total - $free);

        return [
            'total' => (float) $total,
            'used' => (float) $used,
            'free' => (float) $free,
            'percent' => $this->percent($used, (float) $total),
        ];
    }

    /**
     * @return array<int, float>
     */
    protected function load(): array
    {
        $load = function_exists('sys_getloadavg') ? sys_getloadavg() : [0, 0, 0];

        return array_map(fn ($value) => round((float) $value, 2), array_values($load ?: [0, 0, 0]));
    }

    protected function uptime(): int
    {
        $contents = $this->contents('/proc/uptime');

        return $contents === null ? 0 : (int) (float) strtok(trim($contents), ' ');
    }

    public function cores(): int
    {
        $contents = $this->contents('/proc/cpuinfo');

        if ($contents !== null) {
            $count = substr_count($contents, "\nprocessor") + (str_starts_with($contents, 'processor') ? 1 : 0);

            if ($count > 0) {
                return $count;
            }
        }

        // Not Linux (a developer's laptop): fall back to whatever nproc says.
        $nproc = @shell_exec('getconf _NPROCESSORS_ONLN 2>/dev/null');

        return max(1, (int) trim((string) $nproc));
    }

    /**
     * Memory figures in bytes, keyed by their /proc/meminfo label.
     *
     * @return array<string, float>
     */
    protected function meminfo(): array
    {
        static $cached = null;

        if ($cached !== null) {
            return $cached;
        }

        $contents = $this->contents('/proc/meminfo');

        if ($contents === null) {
            return $cached = [];
        }

        $values = [];

        foreach (preg_split('/\r?\n/', $contents) as $line) {
            if (preg_match('/^(\w+):\s+(\d+)\s*kB/', $line, $matches)) {
                // /proc/meminfo is in kibibytes; everything else here is bytes.
                $values[$matches[1]] = (float) $matches[2] * 1024;
            }
        }

        return $cached = $values;
    }

    protected function firstLine(string $path, string $prefix): ?string
    {
        $contents = $this->contents($path);

        if ($contents === null) {
            return null;
        }

        foreach (preg_split('/\r?\n/', $contents) as $line) {
            if (str_starts_with($line, $prefix)) {
                return substr($line, strlen($prefix));
            }
        }

        return null;
    }

    protected function contents(string $path): ?string
    {
        return is_readable($path) ? (file_get_contents($path) ?: null) : null;
    }

    protected function percent(float $used, float $total): float
    {
        return $total > 0 ? round(max(0, min(100, $used / $total * 100)), 1) : 0.0;
    }
}
