<?php

namespace Tests\Feature;

use App\Models\DnsAccount;
use App\Models\Site;
use App\Models\User;
use App\Services\Dns\DnsManager;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class DnsIntegrationTest extends TestCase
{
    use RefreshDatabase;

    public function test_a_token_is_verified_before_the_account_is_stored(): void
    {
        Http::fake([
            'api.cloudflare.com/client/v4/user/tokens/verify' => Http::response([
                'success' => true,
                'result' => ['status' => 'active'],
            ]),
        ]);

        $user = User::factory()->create();

        $this->actingAs($user)->post(route('dns.store'), [
            'provider' => 'cloudflare',
            'label' => 'Personal',
            'api_token' => 'cf-token',
            'email' => 'me@example.com',
        ])->assertRedirect();

        $account = DnsAccount::first();

        $this->assertSame('Personal', $account->label);
        $this->assertNotNull($account->verified_at);
        $this->assertSame('cf-token', $account->api_token);
        $this->assertNotSame(
            'cf-token',
            $this->getConnection()->table('dns_accounts')->where('id', $account->id)->first()->api_token
        );
    }

    public function test_an_invalid_token_is_rejected(): void
    {
        Http::fake([
            'api.cloudflare.com/*' => Http::response([
                'success' => false,
                'errors' => [['code' => 1000, 'message' => 'Invalid API Token']],
            ], 401),
        ]);

        $user = User::factory()->create();

        $this->actingAs($user)->post(route('dns.store'), [
            'provider' => 'cloudflare',
            'label' => 'Personal',
            'api_token' => 'bad-token',
        ])->assertSessionHasErrors('api_token');

        $this->assertSame(0, DnsAccount::count());
    }

    public function test_dns_records_are_created_for_each_hostname(): void
    {
        Http::fake([
            'api.cloudflare.com/client/v4/zones?*' => Http::response([
                'success' => true,
                'result' => [['id' => 'zone-1', 'name' => 'example.com']],
                'result_info' => ['total_pages' => 1],
            ]),
            'api.cloudflare.com/client/v4/zones/zone-1/dns_records?*' => Http::response([
                'success' => true,
                'result' => [],
            ]),
            'api.cloudflare.com/client/v4/zones/zone-1/dns_records' => Http::response([
                'success' => true,
                'result' => ['id' => 'record-1'],
            ]),
        ]);

        $user = User::factory()->create();
        $account = $user->dnsAccounts()->create(['provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf-token']);

        $site = Site::create([
            'user_id' => $user->id,
            'dns_account_id' => $account->id,
            'domain' => 'app.example.com',
            'aliases' => ['www.app.example.com'],
            'root_path' => '/var/www/app.example.com',
            'manage_dns' => true,
            'dns_type' => 'A',
            'dns_content' => '203.0.113.10',
        ]);

        app(DnsManager::class)->syncForSite($site);

        $site->refresh();

        $this->assertSame('zone-1', $site->dns_zone_id);
        $this->assertSame('record-1', $site->dns_record_ids['app.example.com']['record_id']);
        $this->assertSame('record-1', $site->dns_record_ids['www.app.example.com']['record_id']);
    }

    public function test_dns_records_are_deleted_with_the_site(): void
    {
        Http::fake([
            'api.cloudflare.com/client/v4/zones/zone-1/dns_records/record-1' => Http::response([
                'success' => true,
                'result' => [],
            ]),
        ]);

        $user = User::factory()->create();
        $account = $user->dnsAccounts()->create(['provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf-token']);

        $site = Site::create([
            'user_id' => $user->id,
            'dns_account_id' => $account->id,
            'domain' => 'app.example.com',
            'root_path' => '/var/www/app.example.com',
            'manage_dns' => true,
            'dns_zone_id' => 'zone-1',
            'dns_record_ids' => [
                'app.example.com' => ['zone_id' => 'zone-1', 'record_id' => 'record-1'],
            ],
        ]);

        $message = app(DnsManager::class)->deleteForSite($site);

        $this->assertStringContainsString('DNS deleted: app.example.com', $message);
        $this->assertSame([], $site->fresh()->dns_record_ids);

        Http::assertSent(fn ($request) => $request->method() === 'DELETE'
            && str_ends_with($request->url(), '/zones/zone-1/dns_records/record-1'));
    }

    /** Adding a record from the panel, without opening the provider's own UI. */
    public function test_a_record_can_be_written_from_the_panel(): void
    {
        Http::fake([
            // Nothing of that type and name yet.
            'api.cloudflare.com/client/v4/zones/z1/dns_records?*' => Http::response([
                'success' => true, 'result' => [],
            ]),
            'api.cloudflare.com/*' => Http::response(['success' => true, 'result' => ['id' => 'new']]),
        ]);

        $user = User::factory()->create();
        $account = $user->dnsAccounts()->create([
            'provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf',
        ]);

        $this->actingAs($user)
            ->post(route('dns.records.store', $account->id), [
                'zone_id' => 'z1',
                'zone_name' => 'example.com',
                'type' => 'A',
                'name' => 'www',
                'content' => '203.0.113.10',
            ])
            ->assertRedirect();

        $this->assertNull(session('error'));

        Http::assertSent(function ($request) {
            return $request->method() === 'POST'
                && str_contains($request->url(), '/zones/z1/dns_records')
                // The label is qualified against the zone before it goes out.
                && ($request->data()['name'] ?? null) === 'www.example.com';
        });
    }

    /** An empty name is the apex, which is what "the domain itself" means. */
    public function test_an_empty_name_writes_the_apex(): void
    {
        Http::fake([
            'api.cloudflare.com/client/v4/zones/z1/dns_records?*' => Http::response(['success' => true, 'result' => []]),
            'api.cloudflare.com/*' => Http::response(['success' => true, 'result' => ['id' => 'new']]),
        ]);

        $user = User::factory()->create();
        $account = $user->dnsAccounts()->create([
            'provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf',
        ]);

        $this->actingAs($user)->post(route('dns.records.store', $account->id), [
            'zone_id' => 'z1',
            'zone_name' => 'example.com',
            'type' => 'A',
            'name' => '',
            'content' => '203.0.113.10',
        ]);

        Http::assertSent(fn ($request) => $request->method() !== 'POST'
            || ($request->data()['name'] ?? null) === 'example.com');
    }

    public function test_records_belong_to_the_credential_that_owns_them(): void
    {
        $owner = User::factory()->create();
        $intruder = User::factory()->create();

        $account = $owner->dnsAccounts()->create([
            'provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf',
        ]);

        $this->actingAs($intruder)
            ->get(route('dns.records', $account->id).'?zone_id=z1&zone_name=example.com')
            ->assertForbidden();

        $this->actingAs($intruder)
            ->post(route('dns.records.store', $account->id), [
                'zone_id' => 'z1', 'zone_name' => 'example.com',
                'type' => 'A', 'name' => 'www', 'content' => '203.0.113.10',
            ])
            ->assertForbidden();

        $this->actingAs($intruder)
            ->delete(route('dns.records.destroy', $account->id), [
                'zone_id' => 'z1', 'zone_name' => 'example.com', 'record_id' => 'r1',
            ])
            ->assertForbidden();
    }
}
