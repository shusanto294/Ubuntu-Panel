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
        $user = User::factory()->create();
        $database = $this->database($user);

        $url = app(PhpMyAdmin::class)->signOn($database);

        $this->assertStringStartsWith(PhpMyAdmin::PATH, $url);
        $this->assertStringNotContainsString('the-password', $url);
        $this->assertStringNotContainsString('my_app_u', $url);
        // It lands on the database rather than the server overview.
        $this->assertStringContainsString('db=my_app', $url);

        $this->assertSame('my_app_u', $_SESSION['PMA_single_signon_user'] ?? null);
        $this->assertSame('the-password', $_SESSION['PMA_single_signon_password'] ?? null);
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

    public function test_it_refuses_engines_it_cannot_manage(): void
    {
        $user = User::factory()->create();
        $database = $this->database($user, 'postgres');

        $this->actingAs($user)
            ->get(route('databases.phpmyadmin', $database))
            ->assertRedirect();

        $this->assertNotNull(session('error'));
    }

    public function test_another_user_cannot_sign_in_to_your_database(): void
    {
        $owner = User::factory()->create();
        $intruder = User::factory()->create();

        $this->actingAs($intruder)
            ->get(route('databases.phpmyadmin', $this->database($owner)))
            ->assertForbidden();
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
