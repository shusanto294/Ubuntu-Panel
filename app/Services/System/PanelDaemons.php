<?php

namespace App\Services\System;

use App\Services\Shell\LocalConnection;

/**
 * The systemd units the panel needs, and turning them off and on again.
 *
 * A control panel that cannot restart its own workers is asking you to open an
 * SSH session to fix the thing whose whole purpose is not making you open an
 * SSH session — and when the broken worker is the terminal daemon, the browser
 * shell is not available to do it from either.
 *
 * PHP-FPM is deliberately not in here. The panel is being served by it, so
 * restarting it kills the request asking for the restart: the browser sees a
 * dropped connection and cannot tell that from a panel that has fallen over.
 * `panel:update` restarts FPM out of band, which is where that belongs.
 */
class PanelDaemons
{
    /** @var array<string, array{label: string, what: string}> */
    public const UNITS = [
        'ubuntu-panel-queue.service' => [
            'label' => 'Queue worker',
            'what' => 'Installs, deployments, database and mail jobs. Without it tasks sit at 0% for ever.',
        ],
        'ubuntu-panel-terminal.service' => [
            'label' => 'Terminal server',
            'what' => 'The browser shell and the websocket that streams live task output.',
        ],
        'ubuntu-panel-scheduler.timer' => [
            'label' => 'Scheduler',
            'what' => 'Metric sampling and certificate renewal.',
        ],
    ];

    public function __construct(protected LocalConnection $shell) {}

    /**
     * What each unit is doing right now.
     *
     * @return array<int, array{unit: string, label: string, what: string, state: string, active: bool, since: ?string}>
     */
    public function status(): array
    {
        if (! $this->hasSystemd()) {
            return [];
        }

        $rows = [];

        foreach (self::UNITS as $unit => $meta) {
            [$state] = $this->shell->run('systemctl is-active '.escapeshellarg($unit).' 2>&1');
            $state = trim($state) ?: 'unknown';

            [$since] = $this->shell->run(
                'systemctl show '.escapeshellarg($unit).' --property=ActiveEnterTimestamp --value 2>/dev/null'
            );

            $rows[] = [
                'unit' => $unit,
                'label' => $meta['label'],
                'what' => $meta['what'],
                'state' => $state,
                'active' => $state === 'active',
                'since' => trim($since) ?: null,
            ];
        }

        return $rows;
    }

    /**
     * Restart one unit, or all of them.
     *
     * Synchronous, and that is the point: none of these is serving this
     * request, so the answer can be the unit's state afterwards rather than a
     * promise that something was scheduled. `enable --now` so a unit that ended
     * up disabled comes back rather than reporting success and staying down.
     *
     * @return array{ok: bool, message: string}
     */
    public function restart(?string $unit = null): array
    {
        if (! $this->hasSystemd()) {
            return ['ok' => false, 'message' => 'This machine has no systemd, so the panel is being run some other way.'];
        }

        $units = $unit === null
            ? array_keys(self::UNITS)
            : [$unit];

        foreach ($units as $name) {
            if (! isset(self::UNITS[$name])) {
                return ['ok' => false, 'message' => 'Unknown service.'];
            }
        }

        // The unit files come from install.sh, which a re-run can change under
        // a running system; restarting without this starts the old definition.
        $this->shell->run('sudo systemctl daemon-reload');

        $failed = [];

        foreach ($units as $name) {
            $this->shell->run('sudo systemctl enable '.escapeshellarg($name).' 2>/dev/null');
            $this->shell->run('sudo systemctl restart '.escapeshellarg($name));

            [$state] = $this->shell->run('systemctl is-active '.escapeshellarg($name).' 2>&1');

            if (trim($state) !== 'active') {
                $failed[$name] = trim($state) ?: 'unknown';
            }
        }

        if ($failed !== []) {
            return [
                'ok' => false,
                'message' => 'Did not come back: '.collect($failed)
                    ->map(fn ($state, $name) => self::UNITS[$name]['label'].' ('.$state.')')
                    ->implode(', ').'. Run `sudo systemctl status '.array_key_first($failed).' -n 30` to see why.',
            ];
        }

        return [
            'ok' => true,
            'message' => count($units) === 1
                ? self::UNITS[$units[0]]['label'].' restarted.'
                : 'All panel services restarted.',
        ];
    }

    protected function hasSystemd(): bool
    {
        [, $code] = $this->shell->run('command -v systemctl >/dev/null 2>&1');

        return $code === 0;
    }
}
