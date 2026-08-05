<?php

namespace App\Http\Controllers;

use App\Models\DnsAccount;
use App\Services\Dns\DnsProviderRegistry;
use App\Services\Dns\DnsRecord;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Throwable;

/**
 * Credentials for the DNS hosts the panel writes records to.
 */
class DnsAccountController extends Controller
{
    /** What the record form offers. Anything else is a job for the provider. */
    public const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'TXT', 'MX', 'NS', 'SRV', 'CAA'];

    public function index(Request $request)
    {
        return Inertia::render('System/Dns', [
            'accounts' => $request->user()->dnsAccounts()
                ->withCount('sites')
                ->latest()
                ->get()
                ->map(fn (DnsAccount $account) => $account->toPanelArray()),
            'providers' => DnsProviderRegistry::options(),
            'recordTypes' => self::RECORD_TYPES,
        ]);
    }

    public function create()
    {
        return Inertia::render('System/DnsCreate', [
            'providers' => DnsProviderRegistry::options(),
        ]);
    }

    public function store(Request $request)
    {
        $data = $request->validate($this->rules());

        $account = new DnsAccount($data + ['user_id' => $request->user()->id]);

        if ($error = $this->reject($account)) {
            return back()->withErrors(['api_token' => $error]);
        }

        $account->verified_at = now();
        $account->save();

        return redirect()->route('dns.index')
            ->with('success', DnsProviderRegistry::label($account->provider).' connected.');
    }

    public function update(Request $request, DnsAccount $account)
    {
        $this->authorize('update', $account);

        $data = $request->validate($this->rules(creating: false));

        // Left blank means "keep the credential I already gave you"; the
        // browser never receives it back, so it cannot resubmit it.
        foreach (['api_token', 'api_secret'] as $secret) {
            if (blank($data[$secret] ?? null)) {
                unset($data[$secret]);
            }
        }

        $account->fill($data);

        if ($account->isDirty(['provider', 'api_token', 'api_secret'])) {
            if ($error = $this->reject($account)) {
                return back()->withErrors(['api_token' => $error]);
            }

            $account->verified_at = now();
        }

        $account->save();

        return back()->with('success', 'DNS credential updated.');
    }

    public function destroy(Request $request, DnsAccount $account)
    {
        $this->authorize('delete', $account);

        $account->delete();

        return back()->with('success', 'DNS credential removed.');
    }

    /** Re-check a stored credential against the provider. */
    public function verify(Request $request, DnsAccount $account)
    {
        $this->authorize('update', $account);

        try {
            $detail = $account->driver()->verify();
            $account->update(['verified_at' => now()]);

            return back()->with('success', $account->providerLabel().': '.$detail);
        } catch (Throwable $e) {
            $account->update(['verified_at' => null]);

            return back()->with('error', $account->providerLabel().' rejected the credential: '.$e->getMessage());
        }
    }

    /**
     * Every record in one zone.
     *
     * Fetched on demand rather than stored: the provider is the record of what
     * exists, and a copy of it in the panel's database would be wrong the first
     * time anybody used the provider's own dashboard.
     */
    public function records(Request $request, DnsAccount $account)
    {
        $this->authorize('view', $account);

        $data = $request->validate([
            'zone_id' => ['required', 'string', 'max:255'],
            'zone_name' => ['required', 'string', 'max:255'],
        ]);

        try {
            $records = $account->driver()->records($data['zone_id'], rtrim($data['zone_name'], '.'));

            // Grouped the way a zone file reads: apex first, then by name, with
            // the record types in a stable order inside each.
            usort($records, function (array $a, array $b) use ($data) {
                $zone = rtrim($data['zone_name'], '.');

                return [$a['name'] === $zone ? 0 : 1, $a['name'], $a['type']]
                    <=> [$b['name'] === $zone ? 0 : 1, $b['name'], $b['type']];
            });

            return response()->json(['records' => $records]);
        } catch (Throwable $e) {
            return response()->json(['records' => [], 'error' => $e->getMessage()], 422);
        }
    }

    public function storeRecord(Request $request, DnsAccount $account)
    {
        $this->authorize('update', $account);

        $data = $request->validate([
            'zone_id' => ['required', 'string', 'max:255'],
            'zone_name' => ['required', 'string', 'max:255'],
            'type' => ['required', 'string', Rule::in(self::RECORD_TYPES)],
            // Empty means the apex, which is what people type when they mean
            // "the domain itself".
            'name' => ['nullable', 'string', 'max:255'],
            'content' => ['required', 'string', 'max:2048'],
            'priority' => ['nullable', 'integer', 'min:0', 'max:65535'],
            'ttl' => ['nullable', 'integer', 'min:0', 'max:604800'],
            'proxied' => ['boolean'],
        ]);

        $zone = rtrim($data['zone_name'], '.');
        $name = trim((string) ($data['name'] ?? ''));

        $record = DnsRecord::fromArray([
            'type' => $data['type'],
            'name' => $this->qualify($name, $zone),
            'content' => $data['content'],
            'priority' => $data['type'] === 'MX' ? ($data['priority'] ?? 10) : ($data['priority'] ?? null),
            'ttl' => $data['ttl'] ?? 0,
            'proxied' => ($data['proxied'] ?? false) && $account->driver()->supportsProxy(),
        ]);

        try {
            $driver = $account->driver();

            // Same type and name already there: overwrite rather than add a
            // second one, which is what every provider's own UI does and what
            // "add an A record for www" means when there is one already.
            $existing = $driver->findRecordId($data['zone_id'], $zone, $record);

            $existing
                ? $driver->update($data['zone_id'], $zone, $existing, $record)
                : $driver->create($data['zone_id'], $zone, $record);
        } catch (Throwable $e) {
            return back()->with('error', 'Could not write the record: '.$e->getMessage());
        }

        return back()->with(
            'success',
            $record->type.' record for '.$record->name.($existing ? ' updated.' : ' created.')
        );
    }

    public function destroyRecord(Request $request, DnsAccount $account)
    {
        $this->authorize('update', $account);

        $data = $request->validate([
            'zone_id' => ['required', 'string', 'max:255'],
            'zone_name' => ['required', 'string', 'max:255'],
            'record_id' => ['required', 'string', 'max:255'],
        ]);

        try {
            $account->driver()->delete($data['zone_id'], rtrim($data['zone_name'], '.'), $data['record_id']);
        } catch (Throwable $e) {
            return back()->with('error', 'Could not delete the record: '.$e->getMessage());
        }

        return back()->with('success', 'Record deleted.');
    }

    /** `www` in `example.com` is `www.example.com`; nothing is the apex. */
    protected function qualify(string $name, string $zone): string
    {
        $name = rtrim($name, '.');

        if ($name === '' || $name === '@' || $name === $zone) {
            return $zone;
        }

        return str_ends_with($name, '.'.$zone) ? $name : $name.'.'.$zone;
    }

    /** JSON list of zones, used by the site creation form and the DNS page. */
    public function zones(Request $request, DnsAccount $account)
    {
        $this->authorize('view', $account);

        try {
            return response()->json(['zones' => $account->driver()->zones()]);
        } catch (Throwable $e) {
            return response()->json(['zones' => [], 'error' => $e->getMessage()], 422);
        }
    }

    /**
     * @return array<string, mixed>
     */
    protected function rules(bool $creating = true): array
    {
        return [
            'provider' => ['required', Rule::in(DnsProviderRegistry::keys())],
            'label' => ['required', 'string', 'max:120'],
            'api_token' => [$creating ? 'required' : 'nullable', 'string', 'max:500'],
            // Only Porkbun and its kind need one; the form hides it otherwise.
            'api_secret' => ['nullable', 'string', 'max:500'],
            'email' => ['nullable', 'email', 'max:255'],
        ];
    }

    /**
     * Try the credential before storing it. A token that does not work is worse
     * than no token: DNS then fails halfway through creating a site, when the
     * vhost already exists and the certificate is being issued.
     */
    protected function reject(DnsAccount $account): ?string
    {
        if (DnsProviderRegistry::needsSecret($account->provider) && blank($account->api_secret)) {
            return DnsProviderRegistry::label($account->provider).' needs both a key and a secret.';
        }

        try {
            $account->driver()->verify();

            return null;
        } catch (Throwable $e) {
            return DnsProviderRegistry::label($account->provider).' rejected this credential: '.$e->getMessage();
        }
    }
}
