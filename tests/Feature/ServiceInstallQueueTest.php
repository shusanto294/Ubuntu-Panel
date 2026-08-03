<?php

namespace Tests\Feature;

use App\Jobs\InstallServices;
use App\Models\Service;
use App\Services\Shell\LocalConnection;
use App\Models\User;
use App\Services\System\ServiceInstaller;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

class ServiceInstallQueueTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    /**
     * A bare machine: every "is it installed?" probe fails, everything else
     * works.
     *
     * @param  array<string, array{0: string, 1: int}>  $responses  matched before the defaults
     */
    protected function bareMachine(array $responses = []): FakeLocalConnection
    {
        $connection = new FakeLocalConnection($responses + [
            'command -v' => ['', 1],
            // Only the mail service's probe; a blanket "test -f" would also
            // catch the default certificate's existence check.
            'test -f /etc/dovecot' => ['', 1],
        ]);

        $this->app->instance(LocalConnection::class, $connection);

        return $connection;
    }

    protected function aptInstalls(FakeLocalConnection $connection): array
    {
        return array_values(array_filter(
            $connection->ran,
            fn ($command) => str_contains($command, 'apt-get install -y')
        ));
    }

    public function test_everything_queued_goes_into_a_single_apt_transaction(): void
    {
        $connection = $this->bareMachine();

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['nginx', 'redis', 'postgres']);

        $this->assertTrue($installer->installQueued($server->fresh()));

        $installs = $this->aptInstalls($connection);

        $this->assertCount(1, $installs, 'one apt call for the whole batch');
        $this->assertStringContainsString('nginx', $installs[0]);
        $this->assertStringContainsString('redis-server', $installs[0]);
        $this->assertStringContainsString('postgresql', $installs[0]);
        // base is a dependency and rides along in the same transaction.
        $this->assertStringContainsString('software-properties-common', $installs[0]);

        $this->assertEqualsCanonicalizing(
            ['base', 'nginx', 'postgres', 'redis'],
            services()->where('status', Service::INSTALLED)->pluck('key')->all()
        );
    }

    public function test_software_that_is_already_present_is_skipped(): void
    {

        // Redis answers its probe, so it must not be reinstalled.
        $connection = $this->bareMachine([
            'command -v redis-server' => ['/usr/bin/redis-server', 0],
            'redis-server --version' => ['Redis server v=7.0.15', 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['redis', 'nginx']);
        $installer->installQueued($server->fresh());

        $installs = $this->aptInstalls($connection);

        $this->assertStringNotContainsString('redis-server', $installs[0] ?? '');
        $this->assertStringContainsString('nginx', $installs[0] ?? '');

        $redis = services->firstWhere('key', 'redis');
        $this->assertSame(Service::INSTALLED, $redis->status);
        $this->assertSame('Redis server v=7.0.15', $redis->version);
    }

    public function test_a_batch_where_everything_is_present_installs_nothing(): void
    {

        // Every probe succeeds: the box already has the lot.
        $connection = new FakeLocalConnection($server, []);
        $this->app->instance(LocalConnectionFactory::class, new FakeConnectionFactory($connection));

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['nginx', 'redis']);
        $installer->installQueued($server->fresh());

        $this->assertSame([], $this->aptInstalls($connection));
        $this->assertFalse(hasPendingServices());
    }

    public function test_a_failed_batch_retries_one_service_at_a_time(): void
    {

        // The combined install fails; so does nginx on its own. Redis is fine.
        $connection = $this->bareMachine([
            'apt-get install -y nginx' => ['E: Unable to locate package nginx', 100],
            'apt-get install -y software-properties-common' => ['E: broken', 100],
            'command -v' => ['', 1],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['nginx', 'redis']);
        $installer->installQueued($server->fresh());

        $services = services->keyBy('key');

        $this->assertSame(Service::FAILED, $services['nginx']->status);
        $this->assertNotEmpty($services['nginx']->last_error);
        // Redis was not dragged down with it.
        $this->assertSame(Service::INSTALLED, $services['redis']->status);
    }

    public function test_versions_are_recorded_as_each_service_finishes(): void
    {
        $this->bareMachine([
            'redis-server --version' => ['Redis server v=7.0.15', 0],
            'nginx -v' => ['nginx version: nginx/1.24.0', 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['redis', 'nginx']);
        $installer->installQueued($server->fresh());

        $services = services->keyBy('key');

        $this->assertSame('Redis server v=7.0.15', $services['redis']->version);
        $this->assertSame('nginx version: nginx/1.24.0', $services['nginx']->version);
        $this->assertNotNull($services['redis']->installed_at);
        $this->assertNotNull($services['redis']->task_id);
    }

    public function test_one_job_drains_the_whole_queue(): void
    {
        $this->bareMachine();

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['redis', 'postgres']);

        $this->assertSame(3, services()->where('status', Service::QUEUED)->count());

        (new InstallServices($server))->handle($installer);

        $server = $server->fresh();

        $this->assertEqualsCanonicalizing(
            ['base', 'postgres', 'redis'],
            Service::query()->where('status', Service::INSTALLED)->pluck('key')->all()
        );
        $this->assertFalse($server->hasPendingServices());
    }

    public function test_installing_a_single_service_still_works(): void
    {
        $connection = $this->bareMachine();

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->syncRows($server);

        $service = $server->services->firstWhere('key', 'redis');

        $this->assertTrue($installer->install($service));
        $this->assertSame(Service::INSTALLED, $service->fresh()->status);
        $this->assertStringContainsString('redis-server', $this->aptInstalls($connection)[0]);
    }

    public function test_installing_a_service_that_is_already_there_does_no_work(): void
    {

        $connection = new FakeLocalConnection($server, []); // every probe succeeds
        $this->app->instance(LocalConnectionFactory::class, new FakeConnectionFactory($connection));

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->syncRows($server);

        $service = $server->services->firstWhere('key', 'redis');

        $this->assertTrue($installer->install($service));
        $this->assertSame(Service::INSTALLED, $service->fresh()->status);
        $this->assertSame([], $this->aptInstalls($connection));
    }

    public function test_detection_records_what_is_already_installed(): void
    {

        // Only nginx and MariaDB answer their detect probes.
        $this->bareMachine([
            'command -v nginx' => ['/usr/sbin/nginx', 0],
            'nginx -v' => ['nginx version: nginx/1.24.0', 0],
            'command -v mariadb' => ['/usr/bin/mariadb', 0],
            'mysql --version' => ['mysql  Ver 15.1 Distrib 10.11.8-MariaDB', 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->detect($server);

        $services = services->keyBy('key');

        $this->assertSame(Service::INSTALLED, $services['nginx']->status);
        $this->assertSame('nginx version: nginx/1.24.0', $services['nginx']->version);
        $this->assertSame(Service::INSTALLED, $services['mysql']->status);
        $this->assertSame(Service::NOT_INSTALLED, $services['mongodb']->status);
        $this->assertSame(Service::NOT_INSTALLED, $services['node']->status);
    }

    public function test_detection_does_not_disturb_a_service_that_is_mid_install(): void
    {
        $server = $this->markInstalled(null, []);
        Service::query()->where('key', 'redis')->update(['status' => Service::INSTALLING]);

        $this->bareMachine();

        $this->app->make(ServiceInstaller::class)->detect($server);

        $this->assertSame(
            Service::INSTALLING,
            services->firstWhere('key', 'redis')->status
        );
    }

    public function test_the_distro_php_is_used_when_it_already_has_the_version(): void
    {
        $server = null; // wants PHP 8.3
        $connection = $this->bareMachine([
            'lsb_release -cs' => ['noble', 0],
            "apt-cache policy 'php8.3-fpm'" => ["php8.3-fpm:\n  Candidate: 8.3.6", 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['php']);
        $installer->installQueued($server->fresh());

        // No PPA needed, and the requested version stands.
        $this->assertSame('8.3', php_version);
        $this->assertEmpty(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'add-apt-repository')
        ));
    }

    public function test_the_ppa_is_added_when_it_publishes_for_this_release(): void
    {
        $connection = $this->bareMachine([
            'lsb_release -cs' => ['jammy', 0],
            // Not in the distro, but the PPA has a Release file for jammy.
            "apt-cache policy 'php8.3-fpm'" => ['php8.3-fpm:'."\n".'  Candidate: (none)', 0],
            'ppa.launchpadcontent.net/ondrej/php/ubuntu/dists' => ['', 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['php']);
        $installer->installQueued($server->fresh());

        $this->assertNotEmpty(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'add-apt-repository -y ppa:ondrej/php')
        ));
    }

    /**
     * Ubuntu 26.04 "resolute": the PPA has no Release file, so adding it makes
     * every later `apt-get update` fail. Use what the distro ships instead.
     */
    public function test_a_release_without_a_ppa_falls_back_to_the_distro_php(): void
    {
        $connection = $this->bareMachine([
            'lsb_release -cs' => ['resolute', 0],
            "apt-cache policy 'php8.3-fpm'" => ['php8.3-fpm:'."\n".'  Candidate: (none)', 0],
            // curl -fsI against the PPA 404s.
            'ppa.launchpadcontent.net/ondrej/php/ubuntu/dists' => ['', 22],
            "sed 's/^php//" => ['8.4', 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['php']);
        $installer->installQueued($server->fresh());

        // The PPA was never added, its broken sources were cleaned up, and the
        // version was renegotiated to what the box can actually install.
        $this->assertEmpty(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'add-apt-repository')
        ));
        $this->assertNotEmpty(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'rm -f /etc/apt/sources.list.d/ondrej-ubuntu-php-')
        ));
        $this->assertSame('8.4', php_version);

        // And the apt transaction asks for the version that exists.
        $install = $this->aptInstalls($connection)[0] ?? '';
        $this->assertStringContainsString('php8.4-fpm', $install);
        $this->assertStringNotContainsString('php8.3-fpm', $install);
    }

    public function test_no_php_at_all_is_reported_rather_than_installed_blindly(): void
    {
        $this->bareMachine([
            'lsb_release -cs' => ['resolute', 0],
            'apt-cache policy' => ['php8.3-fpm:'."\n".'  Candidate: (none)', 0],
            'ppa.launchpadcontent.net' => ['', 22],
            "sed 's/^php//" => ['', 0],
        ]);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['php']);
        $installer->installQueued($server->fresh());

        $php = services->firstWhere('key', 'php');

        $this->assertSame(Service::FAILED, $php->status);
        $this->assertStringContainsString('No PHP-FPM package is available', $php->last_error);
    }

    public function test_a_broken_repository_does_not_stop_the_batch(): void
    {
        $connection = $this->bareMachine();

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['nginx']);
        $installer->installQueued($server->fresh());

        $update = array_values(array_filter(
            $connection->ran,
            fn ($command) => str_contains($command, 'apt-get update -y')
        ));

        // One unreachable third-party source 404s during update; apt still has
        // everything the working sources offer, so the run must continue.
        $this->assertNotEmpty($update);
        $this->assertStringContainsString('|| echo', $update[0]);
    }

    public function test_the_page_receives_the_real_service_rows(): void
    {
        $user = User::factory()->create();

        $this->markInstalled(['base', 'nginx']);
        Service::query()->where('key', 'php')->update([
            'status' => Service::FAILED,
            'last_error' => 'apt exited 100',
        ]);

        $services = collect(
            $this->actingAs($user)
                ->get(route('dashboard'))
                ->viewData('page')['props']['services']
        )->keyBy('key');

        // The catalogue used by the create form shares the name `services`;
        // it must not overwrite the rows, or every row renders with no status.
        $this->assertSame(Service::INSTALLED, $services['nginx']['status']);
        $this->assertSame(Service::FAILED, $services['php']['status']);
        $this->assertSame('apt exited 100', $services['php']['last_error']);
        $this->assertSame(Service::NOT_INSTALLED, $services['mongodb']['status']);
        $this->assertArrayHasKey('version', $services['nginx']);
    }

    public function test_asking_to_install_something_already_queued_starts_it(): void
    {
        \Illuminate\Support\Facades\Queue::fake();

        $user = User::factory()->create();

        $this->markInstalled([]);
        Service::query()->where('key', 'redis')->update(['status' => Service::QUEUED]);

        $this->actingAs($user)
            ->post(route('services.install', 'redis'))
            ->assertRedirect()
            ->assertSessionHas('success');

        \Illuminate\Support\Facades\Queue::assertPushed(InstallServices::class);
    }

    public function test_installing_the_mail_service_flips_the_mail_flag(): void
    {
        $server = $this->markInstalled(null, ['base', 'mysql']);
        $this->bareMachine();

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue($server, ['mail']);
        $installer->installQueued($server->fresh());

        $this->assertTrue(mail_configured);
        $this->assertTrue(hasService('mail'));
    }
}
