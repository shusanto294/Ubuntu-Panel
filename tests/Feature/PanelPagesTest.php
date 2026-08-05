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
}
