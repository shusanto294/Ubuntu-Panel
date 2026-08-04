<?php

namespace App\Services\System;

use App\Models\ActivityLog;
use App\Models\Service;
use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use App\Services\Tasks\TaskRunner;
use App\Support\Settings;
use Throwable;

class ServiceInstaller
{
    public function __construct(
        protected ServiceCatalog $catalog,
        protected Settings $settings,
    ) {}

    /**
     * Make sure every known service has a row, without disturbing existing ones.
     *
     * This runs on every page view, so it is one read and — only
     * when the catalogue actually grew — one insert, rather than a query per
     * service.
     */
    public function syncRows(): void
    {
        $existing = Service::query()->pluck('key')->all();
        $missing = array_diff(ServiceCatalog::keys(), $existing);

        if ($missing !== []) {
            Service::query()->insert(array_map(fn ($key) => [
                'key' => $key,
                'status' => Service::NOT_INSTALLED,
                'sort_order' => ServiceCatalog::sortOrder($key),
                'created_at' => now(),
                'updated_at' => now(),
            ], array_values($missing)));
        }
    }

    /**
     * Mark services as queued, in dependency order. Already-installed ones are
     * left alone unless $force is set.
     *
     * @param  array<int, string>  $keys
     * @return array<int, string> the keys actually queued
     */
    public function queue(array $keys, bool $force = false): array
    {
        $this->syncRows();

        $queued = [];

        foreach (ServiceCatalog::withDependencies($keys) as $key) {
            $service = Service::where('key', $key)->first();

            if (! $service || $service->isPending()) {
                continue;
            }

            if ($service->isInstalled() && ! $force) {
                continue;
            }

            $service->update([
                'status' => Service::QUEUED,
                'last_error' => null,
                'queued_at' => now(),
            ]);

            $queued[] = $key;
        }

        return $queued;
    }

    /** The next service waiting to be installed, in install order. */
    public function nextQueued(): ?Service
    {
        return Service::query()
            ->where('status', Service::QUEUED)
            ->orderBy('sort_order')
            ->first();
    }

    /**
     * @return array<int, Service>
     */
    public function allQueued(): array
    {
        return Service::query()
            ->where('status', Service::QUEUED)
            ->orderBy('sort_order')
            ->get()
            ->all();
    }

    /**
     * Install everything that is queued in one pass: one apt transaction for
     * every package involved, then each service's own configuration. Anything
     * already present on the machine is skipped.
     *
     * dpkg takes an exclusive lock, so this is the fastest shape available —
     * running apt concurrently would only serialise behind that lock.
     */
    public function installQueued(bool $force = false): bool
    {
        $queued = $this->allQueued();

        if ($queued === []) {
            return true;
        }

        $keys = array_map(fn (Service $service) => $service->key, $queued);

        $log = ActivityLog::record([
            'user_id' => auth()->id(),
            'type' => 'provision',
            'action' => 'software.install',
            'status' => 'running',
            'message' => 'Installing '.count($keys).' item(s): '.implode(', ', array_map(
                fn ($key) => ServiceCatalog::label($key),
                $keys
            )),
            'started_at' => now(),
        ]);

        Service::query()->whereIn('key', $keys)->update([
            'status' => Service::INSTALLING,
            'task_id' => $log->id,
            'last_error' => null,
        ]);

        $connection = app(LocalConnection::class)->timeout(3600);

        try {
            $runner = TaskRunner::for($log, $connection);
            $steps = $this->planFor($keys, $connection, $runner, $force, $installed);

            $ok = $runner->run($steps);

            $this->settle($keys, $installed, $ok, $log, $runner->failures());

            return $ok;
        } catch (Throwable $e) {
            $log->forceFill([
                'status' => 'failed',
                'message' => $e->getMessage(),
                'finished_at' => now(),
            ])->save();

            Service::query()->whereIn('key', $keys)->where('status', Service::INSTALLING)->update([
                'status' => Service::FAILED,
                'last_error' => $e->getMessage(),
            ]);

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    /**
     * Build the ordered step list for a batch.
     *
     * @param  array<int, string>  $keys
     * @param  array<int, string>|null  $installed  filled with keys that finished
     * @return array<int, Step>
     */
    protected function planFor(
                array $keys,
        LocalConnection $connection,
        TaskRunner $runner,
        bool $force,
        ?array &$installed
    ): array {
        $installed = [];

        // Anything already on the box is skipped rather than reinstalled.
        $skipped = [];

        if (! $force) {
            foreach ($keys as $key) {
                if ($this->isPresent($connection, $key)) {
                    $skipped[] = $key;
                }
            }
        }

        $todo = array_values(array_diff($keys, $skipped));

        $steps = [];

        if ($skipped !== []) {
            $steps[] = Step::call(
                'Skip what is already installed',
                function () use ($skipped, $connection, &$installed) {
                    foreach ($skipped as $key) {
                        $this->markInstalled($key, $connection);
                        $installed[] = $key;
                    }

                    return 'Already present, left alone: '.implode(', ', array_map(
                        fn ($key) => ServiceCatalog::label($key),
                        $skipped
                    ));
                }
            );
        }

        if ($todo === []) {
            return $steps;
        }

        $packages = $this->packagesFor($todo);

        $steps[] = $this->catalog->waitForAptStep();

        // Repositories and debconf answers first — they change what apt can see.
        //
        // Tagged with the service they belong to, so that a third-party
        // repository which has nothing to offer this release — MongoDB's, most
        // often — costs you MongoDB and not the entire install.
        foreach ($todo as $key) {
            foreach ($this->catalog->preSteps($key) as $step) {
                $steps[] = $step->for($key);
            }
        }

        if ($packages !== []) {
            // One unreachable third-party repository must not stop the install:
            // apt still has everything the working repositories offer.
            $steps[] = Step::make('Update package lists', [
                'sudo DEBIAN_FRONTEND=noninteractive apt-get update -y || '.
                'echo "apt-get update reported errors; continuing with the repositories that did work"',
            ]);

            // One transaction for every package: one dependency resolution, one
            // download pass, one dpkg run. The list is rebuilt here rather than
            // above, because a pre-step may have changed it (the PHP version can
            // be renegotiated against what the distro actually offers).
            $steps[] = Step::call(
                'Install '.count($packages).' packages in one transaction',
                function (LocalConnection $ssh, TaskRunner $runner) use ($todo) {
                    // Anything whose repository step already gave out is left
                    // out of the transaction. Asking apt for a package it
                    // cannot see would fail the batch for everyone and send
                    // the whole thing down the one-at-a-time retry path.
                    $live = array_values(array_filter(
                        $todo,
                        fn (string $key) => ! $runner->isSkipped($key)
                    ));

                    return $this->installPackages($ssh, $runner, $live, $this->packagesFor($live));
                }
            );
        }

        foreach ($todo as $key) {
            foreach ($this->catalog->installSteps($key) as $step) {
                $steps[] = $step->for($key);
            }

            foreach ($this->catalog->configureSteps($key) as $step) {
                $steps[] = $step->for($key);
            }

            // Each service flips to installed as soon as its own part is done,
            // so the software list fills in progressively.
            $steps[] = Step::call(
                'Verify '.ServiceCatalog::label($key),
                function (LocalConnection $ssh) use ($key, &$installed) {
                    $version = $this->markInstalled($key, $ssh);
                    $installed[] = $key;

                    return ServiceCatalog::label($key).' ready'.($version ? ' — '.$version : '');
                }
            )->for($key);
        }

        return $steps;
    }

    /**
     * @param  array<int, string>  $keys
     * @return array<int, string>
     */
    protected function packagesFor(array $keys): array
    {
        $packages = [];

        foreach ($keys as $key) {
            $packages = array_merge($packages, $this->catalog->packages($key));
        }

        return array_values(array_unique($packages));
    }

    /**
     * One apt call for the batch. If it fails, retry package sets one service at
     * a time so a single broken package does not take the rest down with it.
     *
     * @param  array<int, string>  $keys
     * @param  array<int, string>  $packages
     */
    protected function installPackages(LocalConnection $ssh, TaskRunner $runner, array $keys, array $packages): string
    {
        $command = 'sudo DEBIAN_FRONTEND=noninteractive apt-get install -y '.implode(' ', $packages);

        [$output, $code] = $ssh->run($command);

        if ($code === 0) {
            return $output;
        }

        $log = $output."\n\nThe combined install failed; retrying one service at a time.\n";
        $failures = [];

        foreach ($keys as $key) {
            $own = $this->catalog->packages($key);

            if ($own === []) {
                continue;
            }

            [$retryOutput, $retryCode] = $ssh->run(
                'sudo DEBIAN_FRONTEND=noninteractive apt-get install -y '.implode(' ', $own)
            );

            $log .= "\n$ ".ServiceCatalog::label($key)."\n".$retryOutput."\n";

            if ($retryCode !== 0) {
                $failures[] = $key;

                Service::query()->where('key', $key)->update([
                    'status' => Service::FAILED,
                    'last_error' => 'apt could not install '.implode(', ', $own),
                ]);

                // Configuring something that never installed would only fail
                // and take the rest of the batch down with it.
                $runner->skipGroup($key);
            }
        }

        if ($failures !== []) {
            $log .= "\nFailed: ".implode(', ', array_map(fn ($k) => ServiceCatalog::label($k), $failures));
        }

        return $log;
    }

    /** Is the service already on the box? */
    protected function isPresent(LocalConnection $connection, string $key): bool
    {
        $detect = ServiceCatalog::meta($key)['detect'] ?? null;

        if (! $detect) {
            return false;
        }

        try {
            [, $code] = $connection->run($detect.' >/dev/null 2>&1');

            return $code === 0;
        } catch (Throwable $e) {
            return false;
        }
    }

    protected function markInstalled(string $key, LocalConnection $connection): ?string
    {
        $version = $this->detectVersion($connection, $key);

        Service::query()->where('key', $key)->update([
            'status' => Service::INSTALLED,
            'version' => $version,
            'last_error' => null,
            'installed_at' => now(),
        ]);

        if ($key === 'mail') {
            $this->settings->set('mail_configured', '1');
        }

        return $version;
    }

    /**
     * Anything still marked installing when the run ends did not make it.
     *
     * @param  array<int, string>  $keys
     * @param  array<int, string>  $installed
     */
    protected function settle(array $keys, array $installed, bool $ok, ActivityLog $log, array $failures = []): void
    {
        $stuck = array_values(array_diff($keys, $installed));

        if ($stuck === []) {
            return;
        }

        // Each service that failed on its own step gets its own reason. The
        // rest — cut short by a shared step giving out — get the run's.
        $fallback = $log->fresh()->message;

        foreach ($stuck as $key) {
            Service::query()
                ->where('key', $key)
                ->where('status', Service::INSTALLING)
                ->update([
                    'status' => Service::FAILED,
                    'last_error' => $failures[$key] ?? $fallback,
                ]);
        }
    }

    /**
     * Install a single service on its own. Used by the per-item Install button.
     */
    public function install(Service $service, bool $force = false): bool
    {
        $log = ActivityLog::record([
            'user_id' => auth()->id(),
            'type' => 'provision',
            'action' => 'service.install',
            'status' => 'running',
            'message' => 'Installing '.$service->label(),
            'started_at' => now(),
        ]);

        $service->update([
            'status' => Service::INSTALLING,
            'task_id' => $log->id,
            'last_error' => null,
        ]);

        $connection = app(LocalConnection::class)->timeout(1800);

        try {
            if (! $force && $this->isPresent($connection, $service->key)) {
                $version = $this->markInstalled($service->key, $connection);

                $log->forceFill([
                    'status' => 'success',
                    'message' => $service->label().' was already installed'.($version ? ' ('.$version.')' : '').'.',
                    'progress' => 100,
                    'finished_at' => now(),
                ])->save();

                return true;
            }

            $ok = TaskRunner::for($log, $connection)->run(
                $this->catalog->steps($service->key)
            );

            if ($ok) {
                $this->markInstalled($service->key, $connection);
            } else {
                $service->update([
                    'status' => Service::FAILED,
                    'last_error' => $log->fresh()->message,
                ]);
            }

            return $ok;
        } catch (Throwable $e) {
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            $service->update(['status' => Service::FAILED, 'last_error' => $e->getMessage()]);

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    /**
     * Re-read just these services, right now.
     *
     * The service rows are a record of what installs *did*, which is not the
     * same thing as what is on the box: a batch that failed halfway leaves rows
     * saying `failed` for software that is sitting there installed. A page that
     * is about to tell the user "you have not got this" asks the machine first,
     * and asking about three things costs three `command -v` calls — cheap
     * enough to do on the request rather than on a timer that is usually
     * checking things nobody is looking at.
     *
     * Never throws: a page has to render even if the probe cannot run.
     *
     * @param  array<int, string>  $keys
     */
    public function refresh(array $keys): void
    {
        try {
            $this->detect(only: $keys);
        } catch (Throwable $e) {
            // Best effort.
        }
    }

    /**
     * Ask the machine what is already installed and record versions.
     * Never overwrites a service that is mid-install.
     *
     * @param  array<int, string>|null  $only  limit the probe to these services
     */
    public function detect(?LocalConnection $connection = null, ?array $only = null): void
    {
        $this->syncRows();

        $ssh = $connection ?: app(LocalConnection::class)->timeout(120);
        $owned = $connection === null;

        $services = $only === null
            ? Service::all()
            : Service::whereIn('key', $only)->get();

        try {
            foreach ($services as $service) {
                if ($service->isPending()) {
                    continue;
                }

                if ($this->isPresent($ssh, $service->key)) {
                    $service->update([
                        'status' => Service::INSTALLED,
                        'version' => $this->detectVersion($ssh, $service->key),
                        'last_error' => null,
                        'installed_at' => $service->installed_at ?? now(),
                    ]);

                    continue;
                }

                // Keep a recorded failure visible instead of silently resetting it.
                if ($service->status !== Service::FAILED) {
                    $service->update([
                        'status' => Service::NOT_INSTALLED,
                        'version' => null,
                        'installed_at' => null,
                    ]);
                }
            }
        } finally {
            if ($owned) {
                $ssh->disconnect();
            }
        }
    }

    protected function detectVersion(LocalConnection $ssh, string $key): ?string
    {
        $command = ServiceCatalog::meta($key)['version'] ?? null;

        if (! $command) {
            return null;
        }

        [$output, $code] = $ssh->run($command.' 2>/dev/null');

        if ($code !== 0) {
            return null;
        }

        $line = trim(strtok($output, "\n") ?: '');

        return $line !== '' ? mb_substr($line, 0, 120) : null;
    }
}
