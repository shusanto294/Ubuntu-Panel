<?php

namespace Tests\Feature;

use App\Models\Database;
use App\Models\User;
use App\Services\Databases\DatabaseManager;
use Illuminate\Foundation\Testing\RefreshDatabase;
use ReflectionMethod;
use Tests\TestCase;

/**
 * The commands the panel hands to a shell have to survive being handed to one.
 *
 * The verify step for MariaDB wrapped its SQL in double quotes and then escaped
 * the inner value as well, so the quoting nested and mysql was given a stray
 * backslash: `ERROR at line 1: Unknown command '\'`. It was the one builder in
 * this class not following the shape of the others, and nothing noticed because
 * the tests never looked at the strings.
 */
class DatabaseCommandTest extends TestCase
{
    use RefreshDatabase;

    protected function commands(string $method, string $engine = 'mysql'): array
    {
        $database = new Database([
            'engine' => $engine,
            'name' => 'my_app',
            'username' => 'my_app_u',
            'password' => 'a-password',
        ]);

        $reflection = new ReflectionMethod(DatabaseManager::class, $method);

        return $reflection->invoke(app(DatabaseManager::class), $database);
    }

    public static function engines(): array
    {
        return ['mysql', 'postgres', 'mongodb'];
    }

    public function test_every_command_is_something_a_shell_can_parse(): void
    {
        foreach (self::engines() as $engine) {
            foreach (['createCommands', 'verifyCommands', 'dropCommands'] as $method) {
                foreach ($this->commands($method, $engine) as $command) {
                    // `bash -n` reads it without running it: a quoting mistake
                    // is a syntax error, which is what this is looking for.
                    exec('bash -n -c '.escapeshellarg($command).' 2>&1', $output, $code);

                    $this->assertSame(
                        0,
                        $code,
                        "{$engine} {$method} produced something bash cannot parse:\n{$command}\n".
                        implode("\n", $output)
                    );
                }
            }
        }
    }

    public function test_the_mariadb_verify_is_quoted_once(): void
    {
        $command = $this->commands('verifyCommands')[0];

        $this->assertStringContainsString('my_app', $command);
        // The signature of the bug: an escaped quote inside a quoted string.
        $this->assertStringNotContainsString('\\"', $command);
        $this->assertStringNotContainsString("''\\''", $command);
    }

    /**
     * `SHOW DATABASES LIKE` exits 0 when it matches nothing, so it confirmed
     * databases that were never created.
     */
    public function test_verifying_fails_when_the_database_is_not_there(): void
    {
        $command = $this->commands('verifyCommands')[0];

        $this->assertStringContainsString('grep -q', $command);
        $this->assertStringNotContainsString('SHOW DATABASES LIKE', $command);
    }
}
