<?php

namespace Tests\Feature;

use App\Models\Database;
use App\Models\Service;
use App\Models\User;
use App\Services\System\PhpMyAdmin;
use App\Services\System\ServiceCatalog;
use App\Services\Shell\LocalConnection;
use App\Support\Settings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

class PhpMyAdminTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    protected function database(User $user, string $engine = 'mysql'): Database
    {
        return Database::create([
            'user_id' => $user->id,
            'engine' => $engine,
            'name' => 'my_app',
            'username' => 'my_app_u',
            'password' => 'the-password',
            'status' => 'ready',
        ]);
    }

    public function test_the_engine_list_puts_mariadb_first(): void
    {
        $this->markInstalled(['mongodb', 'postgres', 'mysql']);

        $this->assertSame(['mysql', 'postgres', 'mongodb'], Service::availableEngines());
    }

    /** Whatever order the rows happen to be in. */
    public function test_the_order_does_not_depend_on_the_rows(): void
    {
        $this->markInstalled(['mongodb']);
        $this->assertSame(['mongodb'], Service::availableEngines());

        $this->markInstalled(['postgres', 'mysql']);
        $this->assertSame(['mysql', 'postgres'], Service::availableEngines());
    }

    /**
     * The credentials go into a session phpMyAdmin reads, never into the URL —
     * which would put a database password in browser history, server logs and
     * the Referer header of everything phpMyAdmin loads.
     */
    public function test_signing_on_puts_the_credentials_in_a_session_not_the_url(): void
    {
        $url = app(PhpMyAdmin::class)->signOnAsAdmin();
        $password = app(PhpMyAdmin::class)->adminPassword();

        $this->assertStringStartsWith(PhpMyAdmin::PATH, $url);
        $this->assertStringNotContainsString($password, $url);
        $this->assertStringNotContainsString(PhpMyAdmin::ADMIN_USER, $url);
        // The signon server, not the login form.
        $this->assertStringContainsString('server='.PhpMyAdmin::SIGNON_SERVER, $url);

        $this->assertSame(PhpMyAdmin::ADMIN_USER, $_SESSION['PMA_single_signon_user'] ?? null);
        $this->assertSame($password, $_SESSION['PMA_single_signon_password'] ?? null);
    }

    public function test_the_button_is_offered_only_when_it_is_installed(): void
    {
        $this->markInstalled(['mysql']);

        $installed = $this->actingAs(User::factory()->create())
            ->get(route('databases.index'))
            ->viewData('page')['props']['phpMyAdmin'];

        // Not on this machine, so the page must not offer to open it.
        $this->assertFalse($installed);
    }

    public function test_it_says_so_rather_than_signing_you_in_to_nothing(): void
    {
        $this->markInstalled(['mysql']);

        $this->actingAs(User::factory()->create())
            ->get(route('databases.phpmyadmin'))
            ->assertRedirect();

        $this->assertNotNull(session('error'));
    }

    public function test_guests_cannot_reach_the_sign_on(): void
    {
        $this->get(route('databases.phpmyadmin'))->assertRedirect(route('login'));
    }

    /**
     * Two ways in, and only one of them is a door.
     *
     * The login form is the default server, so anyone arriving at /phpmyadmin
     * without having come through the panel is asked who they are — and a
     * database's own credentials reach that database and nothing else, because
     * that is all its MariaDB user is granted. The signon server needs a
     * session the panel wrote, so it is not a second front door.
     */
    public function test_the_configuration_offers_a_login_form_and_a_signon_route(): void
    {
        $config = app(PhpMyAdmin::class)->config();

        $this->assertStringContainsString("\$cfg['Servers'][\$i]['auth_type'] = 'cookie';", $config);
        $this->assertStringContainsString("\$cfg['Servers'][\$i]['auth_type'] = 'signon';", $config);
        $this->assertStringContainsString(PhpMyAdmin::SIGNON_SESSION, $config);
        $this->assertStringContainsString("\$cfg['ServerDefault'] = 1;", $config);
        $this->assertStringContainsString("'AllowNoPassword'] = false", $config);

        // The privileged account's password is a live MariaDB credential and
        // has no business in a file the web server serves the directory of.
        $this->assertStringNotContainsString(app(PhpMyAdmin::class)->adminPassword(), $config);
    }

    /** Root authenticates over a socket and has no password to hand over. */
    public function test_the_privileged_account_is_the_panels_own_not_root(): void
    {
        $connection = new FakeLocalConnection([]);

        app(PhpMyAdmin::class)->ensureAdminUser($connection);

        $ran = implode("\n", $connection->ran);

        $this->assertStringContainsString("CREATE USER IF NOT EXISTS 'ubuntu_panel_admin'@'localhost'", $ran);
        $this->assertStringContainsString('GRANT ALL PRIVILEGES ON *.*', $ran);
        $this->assertStringNotContainsString("'root'@", $ran);
    }

    /** Upstream tarball, not apt: Debian's package brings its own web server. */
    public function test_it_is_installed_from_upstream_and_configured_for_signon(): void
    {
        $this->markInstalled(['base', 'nginx', 'php', 'mysql']);

        $connection = new FakeLocalConnection([]);
        $this->app->instance(LocalConnection::class, $connection);

        $installer = $this->app->make(\App\Services\System\ServiceInstaller::class);
        $installer->queue(['phpmyadmin'], force: true);
        $installer->installQueued(force: true);

        $ran = implode("\n", $connection->ran);

        $this->assertStringContainsString('files.phpmyadmin.net', $ran);
        $this->assertStringContainsString(ServiceCatalog::PHPMYADMIN_VERSION, $ran);

        $config = $connection->files[PhpMyAdmin::ROOT.'/config.inc.php'] ?? '';

        $this->assertStringContainsString("'auth_type'] = 'signon'", $config);
        $this->assertStringContainsString("'auth_type'] = 'cookie'", $config);
        $this->assertStringContainsString(PhpMyAdmin::SIGNON_SESSION, $config);
        $this->assertStringContainsString("'AllowNoPassword'] = false", $config);
    }

    /** Changing it logs everyone out, so it is generated once and kept. */
    public function test_the_blowfish_secret_survives_a_reinstall(): void
    {
        $first = app(PhpMyAdmin::class)->config();

        app(Settings::class)->flush();

        $this->assertSame($first, app(PhpMyAdmin::class)->config());
    }
}
