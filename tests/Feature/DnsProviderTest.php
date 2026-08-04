<?php

namespace Tests\Feature;

use App\Models\DnsAccount;
use App\Services\Dns\DnsProviderRegistry;
use App\Services\Dns\DnsRecord;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/**
 * Every provider is asked the same question and disagrees about the answer.
 *
 * These check the shape of what goes out — relative or absolute names, what the
 * value field is called, where MX priority goes — because that is where a driver
 * is wrong in a way nothing else catches until DNS quietly stops updating.
 */
class DnsProviderTest extends TestCase
{
    protected function driver(string $provider, string $token = 'tok', string $secret = 'sec')
    {
        return DnsProviderRegistry::driver(new DnsAccount([
            'provider' => $provider,
            'api_token' => $token,
            'api_secret' => $secret,
        ]));
    }

    protected function record(): DnsRecord
    {
        return new DnsRecord('A', 'www.example.com', '203.0.113.10');
    }

    public function test_the_registry_can_build_every_provider_it_offers(): void
    {
        foreach (DnsProviderRegistry::keys() as $key) {
            $this->assertNotNull($this->driver($key), $key);
        }
    }

    public function test_cloudflare_sends_the_whole_hostname(): void
    {
        Http::fake(['api.cloudflare.com/*' => Http::response(['success' => true, 'result' => ['id' => 'r1']])]);

        $this->driver('cloudflare')->create('zone-1', 'example.com', $this->record());

        Http::assertSent(function ($request) {
            return $request['name'] === 'www.example.com'
                && $request['content'] === '203.0.113.10';
        });
    }

    public function test_digitalocean_sends_a_relative_name_and_calls_the_value_data(): void
    {
        Http::fake(['api.digitalocean.com/*' => Http::response(['domain_record' => ['id' => 7]])]);

        $this->driver('digitalocean')->create('example.com', 'example.com', $this->record());

        Http::assertSent(fn ($request) => $request['name'] === 'www' && $request['data'] === '203.0.113.10');
    }

    public function test_digitalocean_uses_an_at_sign_for_the_apex(): void
    {
        Http::fake(['api.digitalocean.com/*' => Http::response(['domain_record' => ['id' => 7]])]);

        $this->driver('digitalocean')->create(
            'example.com',
            'example.com',
            new DnsRecord('A', 'example.com', '203.0.113.10')
        );

        Http::assertSent(fn ($request) => $request['name'] === '@');
    }

    public function test_linode_calls_the_value_target_and_the_ttl_ttl_sec(): void
    {
        Http::fake(['api.linode.com/*' => Http::response(['id' => 7])]);

        $this->driver('linode')->create('123', 'example.com', $this->record());

        Http::assertSent(fn ($request) => $request['name'] === 'www'
            && $request['target'] === '203.0.113.10'
            && isset($request['ttl_sec']));
    }

    public function test_linode_uses_an_empty_name_for_the_apex(): void
    {
        Http::fake(['api.linode.com/*' => Http::response(['id' => 7])]);

        $this->driver('linode')->create('123', 'example.com', new DnsRecord('A', 'example.com', '203.0.113.10'));

        Http::assertSent(fn ($request) => $request['name'] === '');
    }

    public function test_vultr_updates_with_patch_rather_than_put(): void
    {
        Http::fake(['api.vultr.com/*' => Http::response([])]);

        $this->driver('vultr')->update('example.com', 'example.com', 'r1', $this->record());

        Http::assertSent(fn ($request) => $request->method() === 'PATCH' && $request['data'] === '203.0.113.10');
    }

    /** Hetzner has no priority column; an MX carries it in the value. */
    public function test_hetzner_puts_mx_priority_in_front_of_the_value(): void
    {
        Http::fake(['dns.hetzner.com/*' => Http::response(['record' => ['id' => 'r1']])]);

        $this->driver('hetzner')->create(
            'zone-1',
            'example.com',
            new DnsRecord('MX', 'example.com', 'mail.example.com', priority: 10)
        );

        Http::assertSent(fn ($request) => $request['value'] === '10 mail.example.com'
            && $request['name'] === '@'
            && $request['zone_id'] === 'zone-1');
    }

    public function test_hetzner_authenticates_with_its_own_header(): void
    {
        Http::fake(['dns.hetzner.com/*' => Http::response(['zones' => []])]);

        $this->driver('hetzner', token: 'hetzner-token')->zones();

        Http::assertSent(fn ($request) => $request->hasHeader('Auth-API-Token', 'hetzner-token'));
    }

    /** Porkbun takes both credentials in the body of every call. */
    public function test_porkbun_sends_its_key_pair_in_the_body(): void
    {
        Http::fake(['api.porkbun.com/*' => Http::response(['status' => 'SUCCESS', 'id' => 9])]);

        $this->driver('porkbun', token: 'pk', secret: 'sk')->create('example.com', 'example.com', $this->record());

        Http::assertSent(fn ($request) => $request->method() === 'POST'
            && $request['apikey'] === 'pk'
            && $request['secretapikey'] === 'sk'
            && $request['name'] === 'www');
    }

    public function test_porkbun_reports_a_rejection_it_answered_200_for(): void
    {
        Http::fake(['api.porkbun.com/*' => Http::response(['status' => 'ERROR', 'message' => 'Invalid API key'])]);

        $this->expectExceptionMessage('Invalid API key');

        $this->driver('porkbun')->verify();
    }

    /** The most specific zone wins, so a delegated subdomain beats its parent. */
    public function test_the_longest_matching_zone_is_chosen(): void
    {
        Http::fake([
            'api.digitalocean.com/*' => Http::response([
                'domains' => [
                    ['name' => 'example.com'],
                    ['name' => 'staging.example.com'],
                    ['name' => 'elsewhere.com'],
                ],
            ]),
        ]);

        $zone = $this->driver('digitalocean')->findZone('app.staging.example.com');

        $this->assertSame('staging.example.com', $zone['name']);
    }

    public function test_a_hostname_in_no_zone_finds_nothing(): void
    {
        Http::fake(['api.digitalocean.com/*' => Http::response(['domains' => [['name' => 'example.com']]])]);

        $this->assertNull($this->driver('digitalocean')->findZone('app.somewhere-else.com'));
    }

    /** Only Cloudflare proxies; the rest must not be sent a `proxied` field. */
    public function test_cloudflare_is_the_only_provider_that_proxies(): void
    {
        $this->assertTrue($this->driver('cloudflare')->supportsProxy());

        foreach (['digitalocean', 'linode', 'vultr', 'hetzner', 'porkbun'] as $key) {
            $this->assertFalse($this->driver($key)->supportsProxy(), $key);
        }
    }

    public function test_an_http_failure_names_the_provider_and_its_reason(): void
    {
        Http::fake(['api.linode.com/*' => Http::response(['errors' => [['reason' => 'Invalid Token']]], 401)]);

        $this->expectExceptionMessage('Invalid Token');

        $this->driver('linode')->verify();
    }
}
