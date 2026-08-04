<?php

namespace App\Console\Commands;

use App\Models\Service;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceCatalog;
use Illuminate\Console\Command;

/**
 * Why does the panel think that?
 *
 * The panel's picture of the machine comes from two places that can disagree —
 * the rows it wrote when it installed something, and a probe it runs against
 * the box. When a page says software is missing that is plainly there, the
 * question is which of those is wrong, and this prints both side by side.
 */
class Doctor extends Command
{
    protected $signature = 'panel:doctor';

    protected $description = 'Show what the panel believes about this machine, and why';

    public function handle(LocalConnection $shell): int
    {
        $this->newLine();
        $this->components->info('Environment');
        $this->line('    PHP           '.PHP_VERSION.' ('.PHP_SAPI.')');
        $this->line('    PHP binary    '.PHP_BINARY);
        $this->line('    PATH inherited '.($this->orNone((string) getenv('PATH'))));

        [$path] = $shell->run('echo "$PATH"');
        $this->line('    PATH for commands '.$this->orNone($path));

        [$whoami] = $shell->run('whoami');
        $this->line('    Commands run as '.$this->orNone($whoami));

        [$sudo, $sudoCode] = $shell->run('sudo -n true 2>&1 && echo ok');
        $this->line('    Passwordless sudo '.($sudoCode === 0 ? 'yes' : 'NO — '.$this->orNone($sudo)));

        $this->newLine();
        $this->components->info('Stores');
        $this->line('    Cache         '.config('cache.default'));
        $this->line('    Sessions      '.config('session.driver'));
        $this->line('    Queue         '.config('queue.default'));

        if (config('queue.default') === 'database') {
            $this->line('    '.
                'On the database the worker polls it once a second. '.
                'Run `php artisan panel:use-redis` to move them.');
        }

        $this->newLine();
        $this->components->info('Services — what the row says, and what the machine says');

        $rows = Service::orderBy('sort_order')->get()->keyBy('key');
        $disagreements = 0;

        foreach (ServiceCatalog::keys() as $key) {
            $row = $rows[$key] ?? null;
            $detect = ServiceCatalog::meta($key)['detect'] ?? null;

            $recorded = $row?->status ?? 'no row';
            $found = 'not probed';

            if ($detect) {
                [, $code] = $shell->run($detect.' >/dev/null 2>&1');
                $found = $code === 0 ? 'present' : 'absent';
            }

            $agrees = ($recorded === Service::INSTALLED) === ($found === 'present');

            if (! $agrees) {
                $disagreements++;
            }

            $this->line(sprintf(
                '    %-14s row: %-14s machine: %-10s %s',
                $key,
                $recorded,
                $found,
                $agrees ? '' : '  <-- disagree'
            ));
        }

        $this->newLine();
        $this->components->info('Database engines the panel will offer');

        $engines = Service::availableEngines();

        $this->line('    '.($engines === [] ? 'none' : implode(', ', $engines)));

        if ($disagreements > 0) {
            $this->newLine();
            $this->components->warn(
                $disagreements.' service(s) disagree. If the machine column is right, run:'
            );
            $this->line('    php artisan panel:detect-services');
            $this->newLine();
            $this->components->warn(
                'If the machine column is wrong — it says absent for something you can see — '.
                'the probe cannot reach it. Compare "PATH for commands" above with the PATH in '.
                'a normal shell; this command run from the web process and from a terminal '.
                'should agree.'
            );
        }

        return self::SUCCESS;
    }

    protected function orNone(string $value): string
    {
        $value = trim($value);

        return $value === '' ? '(empty)' : $value;
    }
}
