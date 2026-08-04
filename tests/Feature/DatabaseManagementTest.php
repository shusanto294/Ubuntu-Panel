<?php

namespace Tests\Feature;

use App\Jobs\CreateDatabase;
use App\Jobs\DeleteDatabase;
use App\Models\Database;
use App\Models\Service;
use App\Models\User;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceCatalog;
use Tests\Support\FakeLocalConnection;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

class DatabaseManagementTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    /** Put the engines a test needs on the machine. */
    protected function withEngines(array $services = ['mysql', 'postgres', 'mongodb']): void
    {
        $this->markInstalled($services);
        $this->machineWith($services);
    }

    /**
     * A machine whose probes answer for exactly these services.
     *
     * The panel asks the box rather than trusting its own rows now, so a test
     * that says "MariaDB is installed" has to be able to say it to the probe
     * as well, not only to the database.
     *
     * @param  array<int, string>  $services
     */
    protected function machineWith(array $services): FakeLocalConnection
    {
        $responses = [];

        foreach (ServiceCatalog::keys() as $key) {
            $detect = ServiceCatalog::meta($key)['detect'] ?? null;

            if ($detect) {
                $responses[$detect.' >/dev/null 2>&1'] = in_array($key, $services, true)
                    ? ['found', 0]
                    : ['', 1];
            }
        }

        $connection = new FakeLocalConnection($responses);

        $this->app->instance(LocalConnection::class, $connection);

        return $connection;
    }


    /**
     * A batch that failed halfway leaves rows saying `failed` for software that
     * installed fine, and the page then refuses to create a database on a
     * machine that is running MariaDB well enough to be storing the session.
     */
    public function test_the_page_checks_the_machine_before_saying_nothing_is_installed(): void
    {
        $this->markInstalled([]);
        Service::query()->where('key', 'mysql')->update([
            'status' => Service::FAILED,
            'last_error' => 'apt exited 100',
        ]);

        // The software is there whatever the row says.
        $this->machineWith(['mysql']);

        $engines = $this->actingAs(User::factory()->create())
            ->get(route('databases.index'))
            ->viewData('page')['props']['availableEngines'];

        $this->assertContains('mysql', $engines);
        $this->assertSame(Service::INSTALLED, Service::where('key', 'mysql')->first()->status);
    }

    /**
     * The page probes on load, and a probe can fail for reasons that have
     * nothing to do with the software: no PATH, no shell, a full disk. Writing
     * that back over correct rows is how a working panel talks itself into
     * believing MariaDB is gone — so a page load may only ever promote.
     */
    public function test_a_failing_probe_cannot_unrecord_installed_software(): void
    {
        $this->markInstalled(['mysql', 'postgres']);

        // Nothing answers, the way it looks from a process with no PATH.
        $this->machineWith([]);

        $engines = $this->actingAs(User::factory()->create())
            ->get(route('databases.index'))
            ->viewData('page')['props']['availableEngines'];

        $this->assertContains('mysql', $engines);
        $this->assertSame(Service::INSTALLED, Service::where('key', 'mysql')->first()->status);
    }

    /** The reconciling pass is the one the user asks for, and it does demote. */
    public function test_an_explicit_refresh_does_correct_a_row_that_is_wrong(): void
    {
        $this->markInstalled(['mysql']);
        $this->machineWith([]);

        $this->artisan('panel:detect-services')->assertSuccessful();

        $this->assertSame(Service::NOT_INSTALLED, Service::where('key', 'mysql')->first()->status);
    }

    /** Three probes, not the whole catalogue — cheap enough to do every time. */
    public function test_it_only_probes_the_engines(): void
    {
        $this->markInstalled([]);

        $connection = $this->machineWith([]);

        $this->actingAs(User::factory()->create())->get(route('databases.index'));

        $probes = array_values(array_filter(
            $connection->ran,
            fn ($c) => str_contains($c, 'command -v')
        ));

        $this->assertCount(count(Service::ENGINE_KEYS), $probes);
    }

    public function test_a_database_can_be_created_with_generated_credentials(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withEngines();

        $this->actingAs($user)->post(route('databases.store'), [
            'engine' => 'mysql',
            'name' => 'my_app',
        ])->assertRedirect();

        $database = Database::first();

        $this->assertSame('my_app', $database->name);
        $this->assertNotEmpty($database->username);
        $this->assertNotEmpty($database->password);
        $this->assertSame('pending', $database->status);
        Queue::assertPushed(CreateDatabase::class);
    }

    public function test_the_password_is_encrypted_at_rest(): void
    {
        $user = User::factory()->create();

        $database = Database::create([
            'user_id' => $user->id,
            'engine' => 'mysql',
            'name' => 'my_app',
            'username' => 'my_app_u',
            'password' => 'plain-text-secret',
        ]);

        $raw = $this->getConnection()->table('databases')->where('id', $database->id)->first();

        $this->assertNotSame('plain-text-secret', $raw->password);
        $this->assertSame('plain-text-secret', $database->fresh()->password);
    }

    public function test_an_engine_that_is_not_installed_is_rejected(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user)->post(route('databases.store'), [
            'engine' => 'mongodb',
            'name' => 'my_app',
        ])->assertSessionHasErrors('engine');

        $this->assertSame(0, Database::count());
    }

    public function test_database_names_are_restricted_to_safe_characters(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user)->post(route('databases.store'), [
            'engine' => 'mysql',
            'name' => 'drop; DROP TABLE users',
        ])->assertSessionHasErrors('name');
    }

    public function test_duplicate_names_on_the_same_engine_are_rejected(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withEngines();

        $payload = ['engine' => 'mysql', 'name' => 'my_app'];

        $this->actingAs($user)->post(route('databases.store'), $payload)->assertRedirect();
        $this->actingAs($user)->post(route('databases.store'), $payload)->assertSessionHasErrors('name');
    }

    public function test_deleting_a_database_queues_the_drop(): void
    {
        Queue::fake();
        $user = User::factory()->create();

        $database = Database::create([
            'user_id' => $user->id, 'engine' => 'mysql', 'name' => 'my_app',
            'username' => 'my_app_u', 'password' => 'secret',
        ]);

        $this->actingAs($user)->delete(route('databases.destroy', $database))->assertRedirect();

        Queue::assertPushed(DeleteDatabase::class);
    }

    public function test_credentials_are_scoped_to_the_owner(): void
    {
        $owner = User::factory()->create();
        $intruder = User::factory()->create();

        $database = Database::create([
            'user_id' => $owner->id, 'engine' => 'mysql', 'name' => 'my_app',
            'username' => 'my_app_u', 'password' => 'secret',
        ]);

        $this->actingAs($owner)
            ->getJson(route('databases.credentials', $database))
            ->assertOk()
            ->assertJsonPath('password', 'secret')
            ->assertJsonPath('port', 3306);

        $this->actingAs($intruder)
            ->getJson(route('databases.credentials', $database))
            ->assertForbidden();
    }
}
