<?php

namespace App\Http\Controllers;

use App\Models\CloudflareAccount;
use App\Services\Cloudflare\CloudflareClient;
use Illuminate\Http\Request;
use Inertia\Inertia;
use Throwable;

class CloudflareAccountController extends Controller
{
    public function index(Request $request)
    {
        $accounts = $request->user()->cloudflareAccounts()
            ->withCount('sites')
            ->latest()
            ->get()
            ->map(fn (CloudflareAccount $account) => [
                'id' => $account->id,
                'label' => $account->label,
                'email' => $account->email,
                'sites_count' => $account->sites_count,
                'verified_at' => $account->verified_at?->toDateTimeString(),
                'created_at' => $account->created_at->toDateTimeString(),
            ]);

        return Inertia::render('Cloudflare/Index', ['accounts' => $accounts]);
    }

    public function store(Request $request)
    {
        $data = $request->validate([
            'label' => ['required', 'string', 'max:120'],
            'api_token' => ['required', 'string', 'max:500'],
            'email' => ['nullable', 'email', 'max:255'],
        ]);

        try {
            (new CloudflareClient($data['api_token']))->verifyToken();
        } catch (Throwable $e) {
            return back()->withErrors(['api_token' => 'Cloudflare rejected this token: '.$e->getMessage()]);
        }

        $request->user()->cloudflareAccounts()->create($data + ['verified_at' => now()]);

        return back()->with('success', 'Cloudflare account connected.');
    }

    public function update(Request $request, CloudflareAccount $account)
    {
        $this->authorize('update', $account);

        $data = $request->validate([
            'label' => ['required', 'string', 'max:120'],
            'api_token' => ['nullable', 'string', 'max:500'],
            'email' => ['nullable', 'email', 'max:255'],
        ]);

        if (filled($data['api_token'] ?? null)) {
            try {
                (new CloudflareClient($data['api_token']))->verifyToken();
            } catch (Throwable $e) {
                return back()->withErrors(['api_token' => 'Cloudflare rejected this token: '.$e->getMessage()]);
            }

            $data['verified_at'] = now();
        } else {
            unset($data['api_token']);
        }

        $account->update($data);

        return back()->with('success', 'Cloudflare account updated.');
    }

    public function destroy(Request $request, CloudflareAccount $account)
    {
        $this->authorize('delete', $account);

        $account->delete();

        return back()->with('success', 'Cloudflare account disconnected.');
    }

    /** Re-check the stored token against the Cloudflare API. */
    public function verify(Request $request, CloudflareAccount $account)
    {
        $this->authorize('update', $account);

        try {
            CloudflareClient::for($account)->verifyToken();
            $account->update(['verified_at' => now()]);

            return back()->with('success', 'Token is valid.');
        } catch (Throwable $e) {
            $account->update(['verified_at' => null]);

            return back()->with('error', 'Token check failed: '.$e->getMessage());
        }
    }

    /** JSON list of zones, used by the site creation form. */
    public function zones(Request $request, CloudflareAccount $account)
    {
        $this->authorize('view', $account);

        try {
            $zones = collect(CloudflareClient::for($account)->zones())
                ->map(fn ($zone) => ['id' => $zone['id'], 'name' => $zone['name'], 'status' => $zone['status'] ?? null])
                ->values();

            return response()->json(['zones' => $zones]);
        } catch (Throwable $e) {
            return response()->json(['zones' => [], 'error' => $e->getMessage()], 422);
        }
    }
}
