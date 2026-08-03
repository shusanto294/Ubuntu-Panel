<?php

use Illuminate\Support\Facades\Schedule;

// Picks up any install queue that stalled (worker restart, lost job, failed step retry).
Schedule::command('panel:process-service-queue')
    ->everyMinute()
    ->withoutOverlapping();

// Keeps the server list's CPU/memory/disk figures recent. One queued job per
// server, so sampling runs in parallel across the workers and a slow or dead
// server never holds up the others.
Schedule::command('panel:collect-metrics')
    ->everyMinute()
    ->withoutOverlapping();
