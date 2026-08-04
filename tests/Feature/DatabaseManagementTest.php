<?php

namespace Tests\Feature;

use App\Jobs\CreateDatabase;
use App\Jobs\DeleteDatabase;
use App\Models\Database;
use App\Models\User;
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
