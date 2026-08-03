<?php

namespace Tests\Feature;

use App\Models\CloudflareAccount;
use App\Models\Site;
use App\Models\User;
use App\Services\Cloudflare\CloudflareDnsManager;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class CloudflareIntegrationTest extends TestCase
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

        $this->actingAs($user)->post(route('cloudflare.store'), [
            'label' => 'Personal',
            'api_token' => 'cf-token',
            'email' => 'me@example.com',
        ])->assertRedirect();

        $account = CloudflareAccount::first();

        $this->assertSame('Personal', $account->label);
        $this->assertNotNull($account->verified_at);
        $this->assertSame('cf-token', $account->api_token);
        $this->assertNotSame(
            'cf-token',
            $this->getConnection()->table('cloudflare_accounts')->where('id', $account->id)->first()->api_token
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

        $this->actingAs($user)->post(route('cloudflare.store'), [
            'label' => 'Personal',
            'api_token' => 'bad-token',
        ])->assertSessionHasErrors('api_token');

        $this->assertSame(0, CloudflareAccount::count());
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
        $account = $user->cloudflareAccounts()->create(['label' => 'Personal', 'api_token' => 'cf-token']);

        $site = Site::create([
            'user_id' => $user->id,
            'cloudflare_account_id' => $account->id,
            'domain' => 'app.example.com',
            'aliases' => ['www.app.example.com'],
            'root_path' => '/var/www/app.example.com',
            'manage_dns' => true,
            'dns_type' => 'A',
            'dns_content' => '203.0.113.10',
        ]);

        app(CloudflareDnsManager::class)->syncForSite($site);

        $site->refresh();

        $this->assertSame('zone-1', $site->cloudflare_zone_id);
        $this->assertSame('record-1', $site->cloudflare_record_ids['app.example.com']['record_id']);
        $this->assertSame('record-1', $site->cloudflare_record_ids['www.app.example.com']['record_id']);
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
        $account = $user->cloudflareAccounts()->create(['label' => 'Personal', 'api_token' => 'cf-token']);

        $site = Site::create([
            'user_id' => $user->id,
            'cloudflare_account_id' => $account->id,
            'domain' => 'app.example.com',
            'root_path' => '/var/www/app.example.com',
            'manage_dns' => true,
            'cloudflare_zone_id' => 'zone-1',
            'cloudflare_record_ids' => [
                'app.example.com' => ['zone_id' => 'zone-1', 'record_id' => 'record-1'],
            ],
        ]);

        $message = app(CloudflareDnsManager::class)->deleteForSite($site);

        $this->assertStringContainsString('DNS deleted: app.example.com', $message);
        $this->assertSame([], $site->fresh()->cloudflare_record_ids);

        Http::assertSent(fn ($request) => $request->method() === 'DELETE'
            && str_ends_with($request->url(), '/zones/zone-1/dns_records/record-1'));
    }
}
