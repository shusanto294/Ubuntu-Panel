<?php

use Illuminate\Support\Facades\Schedule;

// Picks up any install queue that stalled (worker restart, lost job, failed step retry).
Schedule::command('panel:process-service-queue')
    ->everyMinute()
    ->withoutOverlapping();

// Builds the history the dashboard graphs draw from — one row a minute, older
// rows pruned as it goes. Live figures on the page do not come from here; they
// are read from /proc per request.
Schedule::command('panel:collect-metrics')
    ->everyMinute()
    ->withoutOverlapping();
