<?php

namespace App\Services\System;

/**
 * A running series of readings, for a process that stays alive.
 *
 * The dashboard used to ask over HTTP once a second, which meant booting the
 * framework, opening a session and running the middleware stack sixty times a
 * minute to read three files in /proc. The reading costs microseconds;
 * everything around it did not, and it showed up as a CPU spike every second on
 * an otherwise idle machine.
 *
 * The websocket daemon is already running, so it samples instead and pushes the
 * result. One process, no boot, no session, no request — and the numbers come
 * out steadier for it, because nothing on the way to them is competing for the
 * CPU being measured.
 */
class MetricsStream
{
    /** @var array{total: float, idle: float}|null */
    protected ?array $previousCpu = null;

    public function __construct(protected SystemMetrics $metrics) {}

    /**
     * The next reading.
     *
     * CPU is a delta, so the first one has nothing to compare against and
     * reports null for it — the browser simply has no CPU figure for one
     * interval, which is the same thing the HTTP endpoint has always done.
     */
    public function sample(): array
    {
        $reading = $this->metrics->readWith($this->previousCpu, $counters);

        if ($counters !== null) {
            $this->previousCpu = $counters;
        }

        return $reading;
    }

    /** How often to sample, in seconds. */
    public static function interval(): float
    {
        return max(0.5, (float) config('panel.metrics.stream_interval', 1));
    }
}
