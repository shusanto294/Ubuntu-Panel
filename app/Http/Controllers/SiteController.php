<?php

namespace App\Http\Controllers;

use App\Models\ActivityLog;
use App\Models\DnsAccount;
use App\Jobs\CreateSite;
use App\Jobs\DeleteSite;
use App\Jobs\DeploySiteUpdate;
use App\Models\Service;
use App\Models\Site;
use App\Services\System\HostInfo;
use App\Services\System\ServiceInstaller;
use App\Support\Settings;
use App\Services\Dns\DnsManager;
use App\Services\Sites\SiteManager;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Throwable;

class SiteController extends Controller
{
    public function __construct(
        protected Settings $settings,
        protected HostInfo $host,
        protected ServiceInstaller $installer,
    ) {}

    public function index(Request $request)
    {
        $sites = Site::query()
            ->latest()
            ->get()
            ->map(fn (Site $site) => $this->summary($site));

        return Inertia::render('Sites/Index', [
            'sites' => $sites,
            // Whatever the queue is working on right now, so the page can show
            // the output instead of a badge that never changes.
            'activeTask' => ActivityLog::whereIn('type', ['site', 'database', 'dns'])
                ->where('status', 'running')
                ->latest('id')
                ->first()?->toConsolePayload(),
        ]);
    }

    public function create(Request $request)
    {
        // This form refuses site types whose software is missing, so a row
        // that is wrong refuses a site the machine could host perfectly well.
        // Only what the form actually gates on gets probed.
        $this->installer->refresh(Site::REQUIRED_SERVICES);

        return Inertia::render('Sites/Create', [
            // What this machine can actually host right now.
            'installedServices' => Service::installedKeys(),
            'pendingServices' => Service::whereIn('status', [Service::QUEUED, Service::INSTALLING])->pluck('key'),
            'phpVersion' => $this->settings->phpVersion(),
            'nodeVersion' => $this->settings->nodeVersion(),
            'publicIp' => $this->host->publicIp(),
            'dnsAccounts' => $request->user()->dnsAccounts()->get()->map(fn (DnsAccount $account) => $account->toPanelArray()),
            'phpVersions' => config('panel.php_versions'),
            // Which of those are actually listening. A site pointed at a
            // version this machine does not have gets an nginx vhost naming a
            // PHP-FPM socket that is not there, and every request to it is a
            // 502 — so the choice is worth making an informed one.
            'installedPhpVersions' => $this->installedPhpVersions(),
            'dnsTypes' => config('panel.dns_types'),
            'sitesRoot' => config('panel.sites_root'),
            'siteTypes' => collect(config('panel.site_types'))
                ->map(fn ($type, $key) => $type + ['key' => $key])
                ->values(),
        ]);
    }

    public function store(Request $request)
    {
        $data = $request->validate([
            'type' => ['required', Rule::in(Site::TYPES)],
            'domain' => ['required', 'string', 'max:255', 'regex:/^[a-z0-9.-]+\.[a-z]{2,}$/i'],
            'aliases' => ['nullable', 'array'],
            'aliases.*' => ['string', 'max:255'],
            'php_version' => ['required', Rule::in(config('panel.php_versions'))],
            'web_directory' => ['nullable', 'string', 'max:120'],
            'ssl' => ['boolean'],

            // Git deployment
            'repository' => ['nullable', 'string', 'max:255'],
            'branch' => ['nullable', 'string', 'max:120'],

            // Node-family apps
            'start_command' => ['nullable', 'string', 'max:255'],
            'build_command' => ['nullable', 'string', 'max:255'],

            // WordPress bootstrap
            'wp_title' => ['nullable', 'string', 'max:120'],
            'wp_admin_user' => ['nullable', 'string', 'max:60'],
            'wp_admin_email' => ['nullable', 'email', 'max:255'],
            'wp_admin_password' => ['nullable', 'string', 'min:10', 'max:100'],

            // DNS
            'manage_dns' => ['boolean'],
            'dns_account_id' => [
                'nullable', 'integer',
                Rule::exists('dns_accounts', 'id')->where('user_id', $request->user()->id),
                Rule::requiredIf(fn () => $request->boolean('manage_dns')),
            ],
            'dns_type' => ['required', Rule::in(config('panel.dns_types'))],
            'dns_content' => ['nullable', 'string', 'max:255'],
            'dns_proxied' => ['boolean'],
        ]);

        // Optional fields are absent from the validated array when not submitted.
        $data += [
            'aliases' => [], 'web_directory' => null, 'ssl' => true,
            'repository' => null, 'branch' => null,
            'start_command' => null, 'build_command' => null,
            'wp_title' => null, 'wp_admin_user' => null,
            'wp_admin_email' => null, 'wp_admin_password' => null,
            'manage_dns' => false, 'dns_account_id' => null,
            'dns_content' => null, 'dns_proxied' => true,
        ];

        $domain = strtolower($data['domain']);
        $type = $data['type'];
        $typeConfig = config("panel.site_types.{$type}");

        if (Site::where('domain', $domain)->exists()) {
            return back()->withErrors(['domain' => 'A site for this domain already exists.']);
        }

        $this->installer->refresh(Site::REQUIRED_SERVICES);

        if ($error = $this->missingRequirement($type)) {
            return back()->withErrors(['type' => $error]);
        }

        $proxied = in_array($type, Site::PROXIED_TYPES, true);

        $site = Site::create([
            'user_id' => $request->user()->id,
            'dns_account_id' => $data['manage_dns'] ? $data['dns_account_id'] : null,
            'domain' => $domain,
            'type' => $type,
            'aliases' => array_values(array_filter(array_map('strtolower', $data['aliases'] ?? []))),
            'root_path' => rtrim(config('panel.sites_root'), '/').'/'.$domain,
            'web_directory' => $this->webDirectory($data['web_directory'], $typeConfig),
            'php_version' => $data['php_version'],
            'node_version' => $this->settings->nodeVersion(),
            'status' => 'pending',
            'ssl' => $data['ssl'] ?? false,

            'app_port' => $proxied ? $this->allocatePort() : null,
            'start_command' => $proxied ? ($data['start_command'] ?: ($typeConfig['start'] ?? 'npm run start')) : null,
            'build_command' => $proxied ? ($data['build_command'] ?: ($typeConfig['build'] ?? null)) : null,

            'repository' => $data['repository'] ?: null,
            'branch' => $data['branch'] ?: 'main',

            'wp_title' => $type === 'wordpress' ? ($data['wp_title'] ?: $domain) : null,
            'wp_admin_user' => $type === 'wordpress' ? ($data['wp_admin_user'] ?: 'admin') : null,
            'wp_admin_email' => $type === 'wordpress' ? ($data['wp_admin_email'] ?: $request->user()->email) : null,
            'wp_admin_password' => $type === 'wordpress' ? ($data['wp_admin_password'] ?: null) : null,

            'manage_dns' => $data['manage_dns'] ?? false,
            'dns_type' => $data['dns_type'],
            'dns_content' => ($data['dns_content'] ?? null) ?: $this->host->publicIp(),
            'dns_proxied' => $data['dns_proxied'] ?? true,
        ]);

        CreateSite::dispatch($site);

        return redirect()->route('sites.show', $site)
            ->with('success', 'Site queued. Watch the deployment progress below.');
    }

    public function show(Request $request, Site $site)
    {
        $this->authorize('view', $site);

        $site->load('dnsAccount', 'database');

        return Inertia::render('Sites/Show', [
            'site' => $this->summary($site, detailed: true),
            'logs' => $site->activityLogs()->limit(25)->get()->map(fn ($log) => [
                'id' => $log->id,
                'type' => $log->type,
                'action' => $log->action,
                'status' => $log->status,
                'message' => $log->message,
                'output' => $log->output,
                'created_at' => $log->created_at->toDateTimeString(),
            ]),
            'activeTask' => $site->activityLogs()->where('status', 'running')->first()?->toConsolePayload(),
            'latestTask' => $site->activityLogs()->first()?->toConsolePayload(),
        ]);
    }

    public function destroy(Request $request, Site $site)
    {
        $this->authorize('delete', $site);

        DeleteSite::dispatch($site, $request->boolean('delete_files', true));

        return redirect()->route('sites.index')
            ->with('success', 'Site deletion queued. Its DNS records and database will be removed too.');
    }

    /** Re-run the Cloudflare DNS sync for a site. */
    public function syncDns(Request $request, Site $site, DnsManager $dns)
    {
        $this->authorize('update', $site);

        if (! $site->manage_dns) {
            return back()->with('error', 'DNS management is not enabled for this site.');
        }

        try {
            $dns->syncForSite($site);

            return back()->with('success', 'Cloudflare DNS records synced.');
        } catch (Throwable $e) {
            return back()->with('error', 'DNS sync failed: '.$e->getMessage());
        }
    }

    /** Full redeploy: re-runs every deployment step. */
    public function redeploy(Request $request, Site $site)
    {
        $this->authorize('update', $site);

        CreateSite::dispatch($site);

        return back()->with('success', 'Redeploy queued.');
    }

    /** Git pull + rebuild for repository-backed sites. */
    public function pull(Request $request, Site $site)
    {
        $this->authorize('update', $site);

        if (! $site->repository) {
            return back()->with('error', 'This site is not backed by a git repository.');
        }

        DeploySiteUpdate::dispatch($site);

        return back()->with('success', 'Pulling the latest commit.');
    }

    public function restart(Request $request, Site $site, SiteManager $manager)
    {
        $this->authorize('update', $site);

        if (! $site->isProxied()) {
            return back()->with('error', 'Only Node.js and Next.js sites run a restartable service.');
        }

        $manager->restart($site);

        return back()->with('success', 'Service restarted.');
    }

    /** Is this machine missing something the site type needs? */
    protected function missingRequirement(string $type): ?string
    {
        return match (true) {
            in_array($type, Site::PROXIED_TYPES, true) && ! Service::installed('node')
                => 'Node.js is not installed. Install it from Settings → Services first.',
            $type === 'wordpress' && ! Service::installed('mysql')
                => 'WordPress needs MariaDB. Install it from Settings → Services first.',
            $type === 'wordpress' && ! Service::installed('wpcli')
                => 'WP-CLI is not installed. Install it from Settings → Services first.',
            $type === 'laravel' && ! Service::installed('mysql')
                => 'Laravel sites need MariaDB for their database. Install it from Settings → Services first.',
            default => null,
        };
    }

    protected function webDirectory(?string $input, ?array $typeConfig): string
    {
        $value = $input !== null && trim($input) !== ''
            ? $input
            : ($typeConfig['web_directory'] ?? '');

        $value = trim($value, '/');

        return $value === '' ? '' : '/'.$value;
    }

    /** Next free port in the configured range. */
    protected function allocatePort(): int
    {
        [$start, $end] = config('panel.app_port_range');

        $used = Site::whereNotNull('app_port')->pluck('app_port')->all();

        for ($port = $start; $port <= $end; $port++) {
            if (! in_array($port, $used, true)) {
                return $port;
            }
        }

        return $start;
    }

    /**
     * PHP versions with a running FPM socket, newest first.
     *
     * @return array<int, string>
     */
    protected function installedPhpVersions(): array
    {
        try {
            [$output] = app(\App\Services\Shell\LocalConnection::class)
                ->run('ls /run/php/*-fpm.sock 2>/dev/null');
        } catch (\Throwable) {
            return [];
        }

        preg_match_all('/php(\d+\.\d+)-fpm\.sock/', $output, $matches);

        return array_values(array_intersect(config('panel.php_versions'), array_unique($matches[1])));
    }

    protected function summary(Site $site, bool $detailed = false): array
    {
        $payload = [
            'id' => $site->id,
            'domain' => $site->domain,
            'type' => $site->type,
            'type_label' => $site->typeLabel(),
            'aliases' => $site->aliases ?? [],
            'status' => $site->status,
            'ssl' => $site->ssl,
            'php_version' => $site->php_version,
            'app_port' => $site->app_port,
            'root_path' => $site->root_path,
            'document_root' => $site->documentRoot(),
            'manage_dns' => $site->manage_dns,
            'repository' => $site->repository,
            'branch' => $site->branch,
            'is_proxied' => $site->isProxied(),
            'last_error' => $site->last_error,
            'created_at' => $site->created_at->toDateTimeString(),
        ];

        if (! $detailed) {
            return $payload;
        }

        return $payload + [
            'dns_type' => $site->dns_type,
            'dns_content' => $site->dns_content,
            'dns_proxied' => $site->dns_proxied,
            'dns_zone_id' => $site->dns_zone_id,
            'dns_account' => $site->dnsAccount?->label,
            'dns_provider' => $site->dnsAccount?->providerLabel(),
            'start_command' => $site->start_command,
            'build_command' => $site->build_command,
            'service_name' => $site->isProxied() ? $site->serviceName() : null,
            'database' => $site->database ? [
                'id' => $site->database->id,
                'name' => $site->database->name,
                'username' => $site->database->username,
                'engine_label' => $site->database->engineLabel(),
                'status' => $site->database->status,
            ] : null,
            'wordpress' => $site->type === 'wordpress' ? [
                'admin_url' => 'http'.($site->ssl ? 's' : '').'://'.$site->domain.'/wp-admin',
                'admin_user' => $site->wp_admin_user,
                'admin_password' => $site->wp_admin_password,
            ] : null,
        ];
    }
}
