<?php

namespace App\Http\Controllers;

use App\Models\DnsAccount;
use App\Services\Dns\DnsProviderRegistry;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Throwable;

/**
 * Credentials for the DNS hosts the panel writes records to.
 */
class DnsAccountController extends Controller
{
    public function index(Request $request)
    {
        return Inertia::render('System/Dns', [
            'accounts' => $request->user()->dnsAccounts()
                ->withCount('sites')
                ->latest()
                ->get()
                ->map(fn (DnsAccount $account) => $account->toPanelArray()),
            'providers' => DnsProviderRegistry::options(),
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

    /** JSON list of zones, used by the site creation form. */
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
