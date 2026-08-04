<?php

namespace Tests\Feature;

use App\Jobs\CreateSite;
use App\Jobs\DeleteSite;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

class SiteManagementTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();

        // HostInfo caches the machine's public address; seeding the cache keeps
        // it off the suite's own network and makes the expected value fixed.
        Cache::put('panel:public-ip', '203.0.113.10', now()->addHour());
    }

    /** Put the software a site type needs on the machine. */
    protected function withServices(array $services = ['mysql', 'wpcli', 'node', 'redis']): void
    {
        $this->markInstalled($services);
    }

    protected function payload(array $overrides = []): array
    {
        return array_merge([
            'type' => 'php',
            'domain' => 'app.example.com',
            'php_version' => '8.3',
            'dns_type' => 'A',
        ], $overrides);
    }

    public function test_a_php_site_can_be_created_and_is_queued_for_deployment(): void
    {
        Queue::fake();
        $user = User::factory()->create();

        $this->actingAs($user)->post(route('sites.store'), $this->payload([
            'domain' => 'App.Example.com',
            'aliases' => ['www.app.example.com'],
            'web_directory' => 'public',
        ]))->assertRedirect();

        $site = Site::first();

        $this->assertSame('app.example.com', $site->domain);
        $this->assertSame('php', $site->type);
        $this->assertSame('/var/www/app.example.com', $site->root_path);
        $this->assertSame('/var/www/app.example.com/public', $site->documentRoot());
        $this->assertSame('203.0.113.10', $site->dns_content);
        $this->assertSame(['app.example.com', 'www.app.example.com'], $site->hostnames());
        $this->assertNull($site->app_port);
        Queue::assertPushed(CreateSite::class);
    }

    public function test_a_wordpress_site_records_its_admin_bootstrap(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withServices();

        $this->actingAs($user)->post(route('sites.store'), $this->payload([
            'type' => 'wordpress',
            'wp_title' => 'My Blog',
            'wp_admin_user' => 'editor',
        ]))->assertRedirect();

        $site = Site::first();

        $this->assertSame('wordpress', $site->type);
        $this->assertSame('My Blog', $site->wp_title);
        $this->assertSame('editor', $site->wp_admin_user);
        $this->assertSame($user->email, $site->wp_admin_email);
        // WordPress serves from the site root, not a public/ subdirectory.
        $this->assertSame('/var/www/app.example.com', $site->documentRoot());
        $this->assertTrue($site->needsDatabase());
    }

    public function test_a_node_site_gets_a_port_and_a_service_name(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withServices();

        $this->actingAs($user)->post(route('sites.store'), $this->payload([
            'type' => 'nextjs',
            'repository' => 'https://github.com/example/app.git',
        ]))->assertRedirect();

        $site = Site::first();

        $this->assertSame(3000, $site->app_port);
        $this->assertSame('npm run build', $site->build_command);
        $this->assertSame('npm run start', $site->start_command);
        $this->assertSame('ubuntu-panel-app-example-com', $site->serviceName());
        $this->assertTrue($site->isProxied());
    }

    public function test_ports_are_not_reused_on_the_same_server(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withServices();

        $this->actingAs($user)->post(route('sites.store'), $this->payload([
            'type' => 'nodejs', 'domain' => 'one.example.com',
        ]));
        $this->actingAs($user)->post(route('sites.store'), $this->payload([
            'type' => 'nodejs', 'domain' => 'two.example.com',
        ]));

        $this->assertSame([3000, 3001], Site::orderBy('id')->pluck('app_port')->all());
    }

    public function test_a_node_site_is_rejected_when_node_is_not_installed(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user)
            ->post(route('sites.store'), $this->payload(['type' => 'nodejs']))
            ->assertSessionHasErrors('type');

        $this->assertSame(0, Site::count());
    }

    public function test_wordpress_is_rejected_without_wp_cli(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user)
            ->post(route('sites.store'), $this->payload(['type' => 'wordpress']))
            ->assertSessionHasErrors('type');
    }

    public function test_managing_dns_requires_a_cloudflare_account(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user)
            ->post(route('sites.store'), $this->payload(['manage_dns' => true]))
            ->assertSessionHasErrors('dns_account_id');
    }

    public function test_duplicate_domains_on_the_same_server_are_rejected(): void
    {
        Queue::fake();
        $user = User::factory()->create();

        $this->actingAs($user)->post(route('sites.store'), $this->payload())->assertRedirect();
        $this->actingAs($user)->post(route('sites.store'), $this->payload())->assertSessionHasErrors('domain');

        $this->assertSame(1, Site::count());
    }

    public function test_deleting_a_site_queues_the_cleanup_job(): void
    {
        Queue::fake();
        $user = User::factory()->create();

        $site = Site::create([
            'user_id' => $user->id,
            'domain' => 'app.example.com',
            'root_path' => '/var/www/app.example.com',
            'php_version' => '8.3',
        ]);

        $this->actingAs($user)
            ->delete(route('sites.destroy', $site), ['delete_files' => true])
            ->assertRedirect(route('sites.index'));

        Queue::assertPushed(DeleteSite::class);
    }

    public function test_guests_cannot_create_a_site(): void
    {
        $this->post(route('sites.store'), $this->payload())
            ->assertRedirect(route('login'));

        $this->assertSame(0, Site::count());
    }
}
