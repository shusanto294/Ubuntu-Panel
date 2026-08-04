<?php

namespace Tests\Feature;

use App\Models\Service;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceCatalog;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

class InstallSoftwareTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    /** A machine with nothing on it, where apt works. */
    protected function bareMachine(array $responses = []): FakeLocalConnection
    {
        // "Bare" means every one of the catalogue's own detect probes says
        // no. Taken from the catalogue rather than written out, so a probe
        // that changes cannot quietly stop being answered here — and so the
        // match is the exact probe, not a prefix of some other command that
        // happens to start the same way (Composer's bootstrap begins with
        // `command -v composer`).
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

    public function test_it_installs_the_whole_catalogue_by_default(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install-services')->assertSuccessful();

        $this->assertSame(
            ServiceCatalog::keys(),
            Service::query()->where('status', Service::INSTALLED)->orderBy('sort_order')->pluck('key')->all(),
        );
    }

    public function test_the_default_set_leaves_the_opt_in_software_alone(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install-services --services=default')->assertSuccessful();

        $installed = Service::query()->where('status', Service::INSTALLED)->pluck('key')->all();

        $this->assertContains('nginx', $installed);
        $this->assertContains('mysql', $installed);
        $this->assertNotContains('mongodb', $installed);
        $this->assertNotContains('mail', $installed);
    }

    public function test_an_explicit_list_installs_that_and_its_dependencies(): void
    {
        $this->bareMachine();

        $this->artisan('panel:install-services --services=redis')->assertSuccessful();

        $installed = Service::query()->where('status', Service::INSTALLED)->pluck('key')->all();

        $this->assertContains('redis', $installed);
        // base is a dependency and comes along.
        $this->assertContains('base', $installed);
        $this->assertNotContains('nginx', $installed);
    }

    /**
     * The whole point of running this in the foreground: a caller that gets
     * exit 0 from a run that broke something cannot act on it.
     */
    public function test_it_exits_non_zero_when_something_fails(): void
    {
        $this->bareMachine([
            // The leading space keeps this to apt commands: it matches the
            // combined transaction and MongoDB's own retry, but not the
            // repository line, where the string appears as a path component.
            ' mongodb-org' => ['E: Unable to locate package mongodb-org', 100],
        ]);

        $this->artisan('panel:install-services --services=mongodb')->assertFailed();

        $this->assertSame(Service::FAILED, Service::where('key', 'mongodb')->first()->status);
    }

    public function test_one_failure_does_not_stop_the_others(): void
    {
        $this->bareMachine([
            // The leading space keeps this to apt commands: it matches the
            // combined transaction and MongoDB's own retry, but not the
            // repository line, where the string appears as a path component.
            ' mongodb-org' => ['E: Unable to locate package mongodb-org', 100],
        ]);

        $this->artisan('panel:install-services')->assertFailed();

        $installed = Service::query()->where('status', Service::INSTALLED)->pluck('key')->all();

        $this->assertContains('nginx', $installed);
        $this->assertContains('mysql', $installed);
        $this->assertContains('mail', $installed);
        $this->assertNotContains('mongodb', $installed);
    }

    /**
     * A third-party repository with nothing for this release used to abort the
     * run before apt was even called, so every service in the batch came back
     * failed — including ones that were sitting installed on the machine.
     */
    public function test_a_broken_repository_costs_only_its_own_service(): void
    {
        $this->bareMachine([
            'repo.mongodb.org' => ['E: The repository has no Release file', 100],
        ]);

        $this->artisan('panel:install-services')->assertFailed();

        $rows = Service::query()->get()->keyBy('key');

        $this->assertSame(Service::FAILED, $rows['mongodb']->status);
        $this->assertStringContainsString('Release file', $rows['mongodb']->last_error);

        foreach (['base', 'nginx', 'php', 'mysql', 'redis', 'node', 'mail'] as $key) {
            $this->assertSame(
                Service::INSTALLED,
                $rows[$key]->status,
                "{$key} was dragged down by MongoDB's repository"
            );
        }
    }

    /**
     * And the package it could not fetch stays out of the shared transaction,
     * so the batch does not fail for everyone and fall back to installing one
     * service at a time.
     */
    public function test_a_written_off_service_is_left_out_of_the_transaction(): void
    {
        $connection = $this->bareMachine([
            'repo.mongodb.org' => ['E: The repository has no Release file', 100],
        ]);

        $this->artisan('panel:install-services');

        $installs = array_values(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'apt-get install -y') && ! str_contains($c, '--reinstall')
        ));

        $this->assertCount(1, $installs, 'one transaction, no per-service retry pass');
        $this->assertStringNotContainsString('mongodb-org', $installs[0]);
        $this->assertStringContainsString('nginx', $installs[0]);
    }

    /**
     * What `install.sh` runs. Every catalogue entry has to come out of a fresh
     * install, and the ones that are not apt packages are the ones that go
     * missing quietly — they need a step written for them, and a catalogue
     * entry without one installs nothing and reports success.
     */
    public function test_a_fresh_install_covers_everything_including_the_non_apt_ones(): void
    {
        $connection = $this->bareMachine();

        $this->artisan('panel:install-services --services=all')->assertSuccessful();

        $installed = Service::query()->where('status', Service::INSTALLED)->pluck('key')->all();

        foreach (ServiceCatalog::keys() as $key) {
            $this->assertContains($key, $installed, "{$key} is not installed after a full run");
        }

        $ran = implode("\n", $connection->ran);

        $this->assertStringContainsString('npm install -g pm2', $ran);
        $this->assertStringContainsString('getcomposer.org', $ran);
        $this->assertStringContainsString('wp-cli.phar', $ran);
    }

    /** PM2 needs Node, so it has to come after it. */
    public function test_pm2_is_installed_after_node(): void
    {
        $connection = $this->bareMachine();

        $this->artisan('panel:install-services --services=all')->assertSuccessful();

        $node = array_search(true, array_map(
            fn ($c) => str_contains($c, 'node -v'),
            $connection->ran
        ), true);
        $pm2 = array_search(true, array_map(
            fn ($c) => str_contains($c, 'npm install -g pm2'),
            $connection->ran
        ), true);

        $this->assertNotFalse($node);
        $this->assertNotFalse($pm2);
        $this->assertGreaterThan($node, $pm2, 'PM2 was installed before Node existed');
    }

    /** PM2 is npm's, not apt's, so it needs a step of its own. */
    public function test_pm2_is_installed_through_npm(): void
    {
        $connection = $this->bareMachine();

        $this->artisan('panel:install-services --services=pm2')->assertSuccessful();

        $ran = implode("\n", $connection->ran);

        $this->assertStringContainsString('npm install -g pm2', $ran);
        // It needs Node, which comes along as a dependency.
        $this->assertContains(
            'node',
            Service::query()->where('status', Service::INSTALLED)->pluck('key')->all()
        );
    }

    public function test_retry_only_touches_what_is_not_installed(): void
    {
        $this->markInstalled(['base', 'nginx']);
        Service::query()->where('key', 'redis')->update([
            'status' => Service::FAILED,
            'last_error' => 'apt exited 100',
        ]);

        $installedAt = Service::where('key', 'nginx')->first()->installed_at;

        $connection = $this->bareMachine();

        $this->artisan('panel:install-services --retry')->assertSuccessful();

        $installs = implode(' ', array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'apt-get install -y')
        ));

        $this->assertStringContainsString('redis-server', $installs);
        $this->assertSame(Service::INSTALLED, Service::where('key', 'redis')->first()->status);

        // Already installed and not asked for: not reinstalled, not restamped.
        $nginx = Service::where('key', 'nginx')->first();
        $this->assertSame(Service::INSTALLED, $nginx->status);
        $this->assertEquals($installedAt, $nginx->installed_at);
    }

    public function test_a_second_run_on_a_finished_machine_does_nothing(): void
    {
        $this->markInstalled(ServiceCatalog::keys());

        $connection = $this->bareMachine();

        $this->artisan('panel:install-services')
            ->expectsOutputToContain('Everything requested is already installed.')
            ->assertSuccessful();

        $this->assertSame([], array_values(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'apt-get install -y')
        )));
    }
}
