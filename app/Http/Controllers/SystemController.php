<?php

namespace App\Http\Controllers;

use App\Jobs\InstallServices;
use App\Models\ActivityLog;
use App\Models\Database;
use App\Models\Service;
use App\Models\Site;
use App\Services\System\ServiceCatalog;
use App\Services\System\ServiceInstaller;
use App\Services\System\SystemMetrics;
use App\Services\System\UpdateChecker;
use App\Support\Settings;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Throwable;

/**
 * The machine the panel is installed on: what is running on it, what is
 * installed, and what it is hosting.
 */
class SystemController extends Controller
{
    public function __construct(
        protected ServiceInstaller $installer,
        protected Settings $settings,
    ) {}

    public function overview(Request $request, SystemMetrics $metrics, UpdateChecker $updates)
    {
        // Servers set up before a service joined the catalogue get their row here.
        $this->installer->syncRows();

        return Inertia::render('System/Overview', [
            'system' => $this->summary(),
            // Cached, so the overview never waits on GitHub.
            'update' => $updates->status(),
            // Rendered immediately; the page then polls for live figures.
            'metrics' => $metrics->read(),
            'counts' => [
                'sites' => Site::count(),
                'databases' => Database::count(),
                'services_installed' => Service::where('status', Service::INSTALLED)->count(),
                'services_total' => Service::count(),
            ],
            'sites' => Site::latest()->limit(5)->get()->map(fn (Site $site) => [
                'id' => $site->id,
                'domain' => $site->domain,
                'type_label' => $site->typeLabel(),
                'status' => $site->status,
            ]),
            'activeTask' => ActivityLog::where('status', 'running')
                ->whereIn('type', ['provision', 'site', 'database', 'mail'])
                ->latest('id')
                ->first()?->toConsolePayload(),
            'recentActivity' => ActivityLog::latest('id')->limit(10)->get()->map(fn ($log) => [
                'id' => $log->id,
                'type' => $log->type,
                'action' => $log->action,
                'status' => $log->status,
                'message' => $log->message,
                'created_at' => $log->created_at->diffForHumans(),
            ]),
        ]);
    }

    /** Everything the panel can install, with what is present right now. */
    public function services(Request $request)
    {
        $this->installer->syncRows();

        return Inertia::render('System/Services', [
            'system' => $this->summary(),
            'services' => Service::orderBy('sort_order')->get()->map->toArray()->values(),
            'activeTask' => ActivityLog::where('status', 'running')
                ->where('type', 'provision')
                ->latest('id')
                ->first()?->toConsolePayload(),
            'latestTask' => ActivityLog::where('type', 'provision')
                ->latest('id')
                ->first()?->toConsolePayload(),
            'phpVersions' => config('panel.php_versions'),
            'nodeVersions' => config('panel.node_versions'),
        ]);
    }

    /**
     * Live figures for this machine.
     *
     * Reading /proc costs microseconds, so the browser can poll this once a
     * second without anything in between — no daemon, no queue, no SSH.
     */
    public function metrics(SystemMetrics $metrics)
    {
        return response()->json(['metrics' => $metrics->read()]);
    }

    /** Queue one service (with its dependencies) for installation. */
    public function installService(Request $request, string $service)
    {
        if (! in_array($service, ServiceCatalog::keys(), true)) {
            return back()->with('error', 'Unknown service.');
        }

        $row = Service::where('key', $service)->first();
        $label = ServiceCatalog::label($service);

        // Already waiting: dispatch rather than refuse, so a stuck batch can be
        // nudged from the UI.
        if ($row && $row->isPending()) {
            InstallServices::dispatch();

            return back()->with('success', $label.' is already queued — starting it now.');
        }

        $queued = $this->installer->queue([$service], force: $request->boolean('force'));

        if ($queued === []) {
            return back()->with('error', $label.' is already installed. Use Reinstall to run it again.');
        }

        InstallServices::dispatch();

        return back()->with('success', 'Queued: '.implode(', ', array_map(
            fn ($key) => ServiceCatalog::label($key),
            $queued
        )).'.');
    }

    /** Queue several at once; they still install in one apt transaction. */
    public function installServices(Request $request)
    {
        $data = $request->validate([
            'services' => ['required', 'array', 'min:1'],
            'services.*' => [Rule::in(ServiceCatalog::keys())],
        ]);

        $queued = $this->installer->queue($data['services']);

        if ($queued === []) {
            return back()->with('error', 'Everything selected is already installed.');
        }

        InstallServices::dispatch();

        return back()->with('success', count($queued).' item(s) queued.');
    }

    public function retryService(Request $request, string $service)
    {
        if (! in_array($service, ServiceCatalog::keys(), true)) {
            return back()->with('error', 'Unknown service.');
        }

        $this->installer->queue([$service], force: true);

        InstallServices::dispatch();

        return back()->with('success', 'Retrying '.ServiceCatalog::label($service).'.');
    }

    /** Re-read what is actually installed on this machine. */
    public function detectServices()
    {
        try {
            $this->installer->detect();

            return back()->with('success', 'Software list refreshed from the system.');
        } catch (Throwable $e) {
            return back()->with('error', 'Could not read the system: '.$e->getMessage());
        }
    }

    /** Defaults applied to new sites. */
    public function updateSettings(Request $request)
    {
        $data = $request->validate([
            'php_version' => ['required', Rule::in(config('panel.php_versions'))],
            'node_version' => ['required', Rule::in(config('panel.node_versions'))],
            'mail_hostname' => ['nullable', 'string', 'max:255'],
        ]);

        foreach ($data as $key => $value) {
            $this->settings->set($key, $value);
        }

        return back()->with('success', 'Settings saved.');
    }

    /** Re-ask GitHub whether a newer version is published. */
    public function checkForUpdates(UpdateChecker $updates)
    {
        return response()->json($updates->status(fresh: true));
    }

    /** Service credentials the panel generated during installation. */
    public function credentials()
    {
        return response()->json(array_filter([
            'mongodb' => $this->settings->secret('mongo_root_password')
                ? ['username' => 'panelAdmin', 'password' => $this->settings->secret('mongo_root_password'), 'auth_db' => 'admin', 'port' => 27017]
                : null,
            'redis' => $this->settings->secret('redis_password')
                ? ['password' => $this->settings->secret('redis_password'), 'port' => 6379]
                : null,
            'mysql' => Service::installed('mysql')
                ? ['note' => 'Root access uses unix socket auth: run `sudo mysql` on this machine.']
                : null,
            'postgres' => Service::installed('postgres')
                ? ['note' => 'Superuser access uses peer auth: run `sudo -u postgres psql`.']
                : null,
        ]));
    }

    /**
     * What this machine is and what state it is in — shown in the header of
     * every system page.
     */
    protected function summary(): array
    {
        return [
            'hostname' => gethostname() ?: 'this server',
            'os' => $this->settings->get('os'),
            'php_version' => $this->settings->phpVersion(),
            'node_version' => $this->settings->nodeVersion(),
            'mail_hostname' => $this->settings->get('mail_hostname'),
            'mail_configured' => $this->settings->boolean('mail_configured'),
            'installed_services' => Service::installedKeys(),
            'available_engines' => Service::availableEngines(),
            'services_installed_count' => Service::where('status', Service::INSTALLED)->count(),
            'services_pending_count' => Service::whereIn('status', [Service::QUEUED, Service::INSTALLING])->count(),
            'services_failed_count' => Service::where('status', Service::FAILED)->count(),
            'preparing' => Service::hasPending(),
        ];
    }
}
