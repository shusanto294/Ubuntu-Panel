<?php

namespace App\Http\Controllers;

use App\Jobs\ConfigurePanelDomain;
use App\Jobs\InstallServices;
use App\Models\ActivityLog;
use App\Models\Database;
use App\Models\DnsAccount;
use App\Models\Service;
use App\Models\Site;
use App\Services\Dns\DnsProviderRegistry;
use App\Services\System\HostInfo;
use App\Services\System\MetricHistory;
use App\Services\System\PanelDomain;
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

    public function overview(Request $request, SystemMetrics $metrics, UpdateChecker $updates, MetricHistory $history)
    {
        // Servers set up before a service joined the catalogue get their row here.
        $this->installer->syncRows();

        return Inertia::render('System/Overview', [
            'system' => $this->summary(),
            // Cached, so the overview never waits on GitHub.
            'update' => $updates->status(),
            // Rendered immediately; the page then polls for live figures.
            'metrics' => $metrics->read(),
            // The graphs open on the shortest range, so ship that one with the
            // page and let the browser fetch the others only if it asks.
            'history' => $history->series(MetricHistory::DEFAULT_RANGE),
            'historyRanges' => MetricHistory::options(),
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

    /**
     * Everything the panel can put on this machine, and what state each of it
     * is in. A page rather than a settings tab: this is where you install,
     * watch the output, and retry what failed.
     */
    public function services()
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

    /**
     * Recorded history for the graphs.
     *
     * Averaged into buckets by the range, so a month costs the same handful of
     * points as an hour does.
     */
    public function metricHistory(Request $request, MetricHistory $history)
    {
        $range = (string) $request->query('range', MetricHistory::DEFAULT_RANGE);

        return response()->json([
            'history' => $history->series($range),
        ]);
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

    /**
     * Where the panel answers, what version it is, and what new sites inherit.
     *
     * Software and DNS credentials used to be tabs on this page. They are
     * destinations of their own again: both are lists you come back to and
     * work in — installing, retrying, adding a provider — which is not what a
     * settings tab is for, and neither was reachable without first landing
     * somewhere else.
     */
    public function settings(UpdateChecker $updates, PanelDomain $panel)
    {
        $this->installer->syncRows();

        return Inertia::render('System/Settings', [
            'system' => $this->summary(),
            'update' => $updates->status(),
            'panel' => [
                'domain' => $panel->current(),
                'url' => $panel->url(),
                'public_ip' => app(HostInfo::class)->publicIp(),
            ],
            'defaults' => [
                'php_version' => $this->settings->phpVersion(),
                'node_version' => $this->settings->nodeVersion(),
                'mail_hostname' => $this->settings->get('mail_hostname'),
            ],
            'phpVersions' => config('panel.php_versions'),
            'nodeVersions' => config('panel.node_versions'),
        ]);
    }

    /**
     * Serve the panel on a hostname the operator owns.
     *
     * Queued, because issuing the certificate reloads nginx underneath the
     * request that asked for it.
     */
    public function updateDomain(Request $request)
    {
        $data = $request->validate([
            'domain' => ['required', 'string', 'max:253', 'regex:/^(?!-)[A-Za-z0-9-]{1,63}(?<!-)(\.(?!-)[A-Za-z0-9-]{1,63}(?<!-))+$/'],
            'email' => ['nullable', 'email'],
        ]);

        ConfigurePanelDomain::dispatch(strtolower($data['domain']), $data['email'] ?? null);

        return back()->with(
            'success',
            'Setting the panel up on '.$data['domain'].'. Watch the console — when it finishes, log in at https://'.$data['domain'].'.'
        );
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
