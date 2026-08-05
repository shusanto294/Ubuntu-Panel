<?php

namespace Tests\Feature;

use App\Models\Site;
use App\Models\Database;
use App\Models\EmailDomain;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Inertia\Testing\AssertableInertia;
use Tests\TestCase;

class PanelPagesTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    public function test_every_panel_page_renders(): void
    {
        $user = User::factory()->create();


        $this->markInstalled(['base', 'nginx', 'php', 'composer', 'certbot', 'mysql', 'redis', 'node', 'wpcli', 'mail']);

        Database::create([
            'user_id' => $user->id,
            'engine' => 'mysql',
            'name' => 'app_db',
            'username' => 'app_db_u',
            'password' => 'secret',
            'status' => 'ready',
        ]);

        $mailDomain = EmailDomain::create([
            'user_id' => $user->id,
            'domain' => 'example.com',
            'status' => 'active',
        ]);

        $mailDomain->accounts()->create([
            'user_id' => $user->id,
            'local_part' => 'info',
            'password' => 'secret',
            'status' => 'active',
        ]);

        $site = Site::create([
            'user_id' => $user->id,
            'domain' => 'app.example.com',
            'root_path' => '/var/www/app.example.com',
            'php_version' => '8.3',
        ]);

        $user->dnsAccounts()->create([
            'provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf-token',
        ]);

        $pages = [
            ['dashboard', [], 'System/Overview'],
            ['settings', [], 'System/Settings'],
            ['services.index', [], 'System/Services'],
            ['dns.index', [], 'System/Dns'],
            ['terminal', [], 'System/Terminal'],
            ['profile.edit', [], 'Profile/Edit'],
            ['sites.index', [], 'Sites/Index'],
            ['sites.create', [], 'Sites/Create'],
            ['sites.show', $site, 'Sites/Show'],
            ['databases.index', [], 'Databases/Index'],
            ['databases.create', [], 'Databases/Create'],
            ['email.index', [], 'Email/Index'],
            ['email.domains.create', [], 'Email/CreateDomain'],
            ['email.domains.show', $mailDomain, 'Email/Show'],
            ['email.accounts.create', $mailDomain, 'Email/CreateAccount'],
            ['dns.create', [], 'System/DnsCreate'],
        ];

        foreach ($pages as [$name, $params, $component]) {
            $this->actingAs($user)
                ->get(route($name, $params))
                ->assertOk()
                ->assertInertia(fn (AssertableInertia $page) => $page->component($component));
        }
    }

    public function test_guests_are_redirected_to_login(): void
    {
        $this->get(route('dashboard'))->assertRedirect(route('login'));
        $this->get(route('sites.index'))->assertRedirect(route('login'));
        $this->get(route('databases.index'))->assertRedirect(route('login'));
        $this->get(route('email.index'))->assertRedirect(route('login'));
        $this->get(route('settings'))->assertRedirect(route('login'));
        $this->get(route('services.index'))->assertRedirect(route('login'));
        $this->get(route('dns.index'))->assertRedirect(route('login'));
    }

    /**
     * A control panel that cannot restart its own workers asks you to open an
     * SSH session to fix the thing whose purpose is not making you open one —
     * and when the broken worker is the terminal daemon, the browser shell is
     * not available to do it from either.
     */
    public function test_the_panels_own_services_can_be_restarted_from_settings(): void
    {
        $connection = new \Tests\Support\FakeLocalConnection([
            'is-active' => ["active\n", 0],
        ]);

        $this->app->instance(\App\Services\Shell\LocalConnection::class, $connection);

        $this->actingAs(\App\Models\User::factory()->create())
            ->post(route('system.restart'), ['unit' => 'ubuntu-panel-terminal.service'])
            ->assertRedirect();

        $this->assertTrue($connection->ranCommandContaining('systemctl restart'));
        $this->assertTrue($connection->ranCommandContaining('ubuntu-panel-terminal.service'));
        // A unit file can change under a running system when install.sh is
        // re-run, and restarting without this starts the old definition.
        $this->assertTrue($connection->ranCommandContaining('daemon-reload'));

        // PHP-FPM is serving this request; restarting it would kill the request
        // asking for the restart.
        $this->assertFalse($connection->ranCommandContaining('fpm'));
    }

    public function test_an_unknown_unit_is_refused(): void
    {
        $connection = new \Tests\Support\FakeLocalConnection;
        $this->app->instance(\App\Services\Shell\LocalConnection::class, $connection);

        $this->actingAs(\App\Models\User::factory()->create())
            ->post(route('system.restart'), ['unit' => 'sshd.service'])
            ->assertRedirect();

        $this->assertFalse($connection->ranCommandContaining('sshd'));
        $this->assertSame('Unknown service.', session('error'));
    }

    public function test_guests_cannot_restart_anything(): void
    {
        $this->post(route('system.restart'))->assertRedirect(route('login'));
    }
}
