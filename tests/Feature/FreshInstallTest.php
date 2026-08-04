<?php

namespace Tests\Feature;

use App\Models\Service;
use App\Models\User;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceCatalog;
use App\Support\Settings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Cache;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

/**
 * What `install.sh` does, in the order it does it.
 *
 * The promise is that a fresh server needs nothing done to it afterwards, and
 * the ways that breaks are rarely loud: a second PHP installed beside the one
 * the panel runs on, an extension landing on the wrong version, two apt runs
 * racing each other. None of those fail the install — they just leave you with
 * work to do.
 */
class FreshInstallTest extends TestCase
{
    use RefreshDatabase;

    protected function bareMachine(array $responses = []): FakeLocalConnection
    {
        $probes = [];

        foreach (ServiceCatalog::keys() as $key) {
            $detect = ServiceCatalog::meta($key)['detect'] ?? null;

            if ($detect) {
                $probes[$detect.' >/dev/null 2>&1'] = ['', 1];
            }
        }

        $connection = new FakeLocalConnection($responses + $probes);

        $this->app->instance(LocalConnection::class, $connection);

        return $connection;
    }

    /**
     * The installer sets up one PHP. If the panel then defaults new sites to a
     * different one, the software step installs a second stack beside it — two
     * to keep patched, and extensions on the version the panel is not running.
     * That is how the panel ends up unable to use the Redis it just installed.
     */
    public function test_new_sites_default_to_the_php_the_installer_set_up(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install --php-version=8.3 --no-detect --email=a@b.com --password=a-long-password')
            ->assertSuccessful();

        $this->assertSame('8.3', app(Settings::class)->phpVersion());
    }

    /** Told nothing, it follows the version it is itself running on. */
    public function test_without_being_told_it_follows_the_running_version(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install --no-detect --email=a@b.com --password=a-long-password')
            ->assertSuccessful();

        $running = PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;
        $expected = in_array($running, config('panel.php_versions'), true)
            ? $running
            : config('panel.php_versions')[0];

        $this->assertSame($expected, app(Settings::class)->phpVersion());
    }

    /** And the software step then installs that version, not another one. */
    public function test_the_software_step_installs_that_same_php(): void
    {
        app(Settings::class)->set('php_version', '8.3');

        $connection = $this->bareMachine();

        $this->artisan('panel:install-services --services=php')->assertSuccessful();

        $installs = implode("\n", array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'apt-get install -y')
        ));

        $this->assertStringContainsString('php8.3-fpm', $installs);
        $this->assertStringNotContainsString('php8.5-fpm', $installs);
        // The extension the panel needs to use Redis, on the right version.
        $this->assertStringContainsString('php8.3-redis', $installs);
    }

    /**
     * Two apt runs on one machine end in a dpkg lock, not two installs. The
     * scheduler's sweep respects this lock; so must the foreground command.
     */
    public function test_it_refuses_to_start_alongside_another_install(): void
    {
        $this->bareMachine();

        $lock = Cache::lock('panel-service-install', 60);
        $lock->get();

        $this->artisan('panel:install-services --services=redis')
            ->expectsOutputToContain('An install is already running')
            ->assertFailed();

        $lock->release();
    }

    public function test_a_finished_install_leaves_nothing_queued(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install-services')->assertSuccessful();

        $this->assertSame(0, Service::where('status', Service::QUEUED)->count());
        $this->assertSame(0, Service::where('status', Service::INSTALLING)->count());
    }

    /** Everything in the catalogue, so there is nothing left to click. */
    public function test_a_fresh_install_installs_the_whole_catalogue(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install-services --services=all')->assertSuccessful();

        $this->assertSame(
            ServiceCatalog::keys(),
            Service::query()->where('status', Service::INSTALLED)->orderBy('sort_order')->pluck('key')->all()
        );
    }
}
