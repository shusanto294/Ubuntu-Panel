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

// The service rows are a record of what installs did; this is what is actually
// on the machine. They drift — a half-finished batch leaves `failed` rows for
// software that installed fine — and the panel refuses to do things it can in
// fact do until they agree. Cheap: a `command -v` per service.
Schedule::command('panel:detect-services')
    ->everyFifteenMinutes()
    ->withoutOverlapping();
