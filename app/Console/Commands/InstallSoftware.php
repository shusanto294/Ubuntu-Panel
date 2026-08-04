<?php

namespace App\Console\Commands;

use App\Models\Service;
use App\Services\System\ServiceCatalog;
use App\Services\System\ServiceInstaller;
use Illuminate\Console\Command;
use Throwable;

/**
 * Installs the software catalogue and reports what actually happened.
 *
 * Named for the page it backs rather than for the job it dispatches — the
 * queued equivalent is App\\Jobs\\InstallServices.
 *
 * The Software page does the same work through the queue, which is right for
 * a click but wrong for an installer: a script that returns before the work is
 * done cannot tell you whether it succeeded. This runs the install in the
 * foreground and exits non-zero if anything is left broken, so `install.sh` can
 * say "ready to use" and mean it.
 */
class InstallSoftware extends Command
{
    protected $signature = 'panel:install-services
                            {--services=all : all, default, or a comma-separated list of keys}
                            {--retry : Only attempt what previously failed}
                            {--force : Reinstall even what is already present}';

    protected $description = 'Install the software catalogue on this machine and report the result';

    public function handle(ServiceInstaller $installer): int
    {
        $installer->syncRows();

        $keys = $this->keys();

        if ($keys === []) {
            $this->components->info('Nothing to install.');

            return self::SUCCESS;
        }

        $force = (bool) $this->option('force');

        $queued = $installer->queue($keys, $force);

        if ($queued === []) {
            $this->components->info('Everything requested is already installed.');

            return self::SUCCESS;
        }

        $this->components->info('Installing '.count($queued).' of '.count($keys).': '.implode(', ', $queued));
        $this->newLine();

        try {
            $installer->installQueued($force);
        } catch (Throwable $e) {
            // A throw here is the batch failing as a whole; the per-service
            // rows below still say which ones got through, so report rather
            // than re-throw.
            $this->components->error('The install run stopped early: '.$e->getMessage());
        }

        return $this->report($keys);
    }

    /**
     * @return array<int, string>
     */
    protected function keys(): array
    {
        if ($this->option('retry')) {
            return Service::query()
                ->whereIn('status', [Service::FAILED, Service::NOT_INSTALLED])
                ->orderBy('sort_order')
                ->pluck('key')
                ->all();
        }

        $requested = trim((string) $this->option('services'));

        return match ($requested) {
            '', 'all' => ServiceCatalog::keys(),
            'default' => array_values(array_filter(
                ServiceCatalog::keys(),
                fn (string $key) => (bool) (config("panel.services.{$key}.default") ?? false)
            )),
            default => $this->explicit($requested),
        };
    }

    /**
     * @return array<int, string>
     */
    protected function explicit(string $list): array
    {
        $keys = array_values(array_filter(array_map('trim', explode(',', $list))));

        $unknown = array_diff($keys, ServiceCatalog::keys());

        if ($unknown !== []) {
            $this->components->warn('Not in the catalogue, ignoring: '.implode(', ', $unknown));
        }

        return array_values(array_intersect(ServiceCatalog::keys(), $keys));
    }

    /**
     * Say what is installed and what is not, and fail if anything is not.
     *
     * @param  array<int, string>  $keys
     */
    protected function report(array $keys): int
    {
        $rows = Service::query()->whereIn('key', $keys)->orderBy('sort_order')->get();

        $installed = $rows->where('status', Service::INSTALLED);
        $broken = $rows->where('status', '!=', Service::INSTALLED);

        $this->newLine();

        if ($installed->isNotEmpty()) {
            $this->components->info(
                $installed->count().' installed: '.
                $installed->map(fn (Service $s) => ServiceCatalog::label($s->key))->implode(', ')
            );
        }

        if ($broken->isEmpty()) {
            return self::SUCCESS;
        }

        $this->newLine();

        foreach ($broken as $service) {
            $this->components->error(ServiceCatalog::label($service->key).' — '.$service->status);

            if ($service->last_error) {
                $this->line('    '.$service->last_error);
            }
        }

        $this->newLine();
        $this->components->warn(
            'The panel itself is running. Retry the failures from the Software page, or:'
        );
        $this->line('    cd '.base_path().' && php artisan panel:install-services --retry');

        return self::FAILURE;
    }
}
