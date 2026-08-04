<?php

namespace Tests\Feature;

use App\Services\Shell\LocalConnection;
use ReflectionMethod;
use Tests\TestCase;

class LocalConnectionTest extends TestCase
{
    protected function environment(): array
    {
        $method = new ReflectionMethod(LocalConnection::class, 'environment');

        return $method->invoke(new LocalConnection(30));
    }

    /**
     * PHP-FPM defaults to `clear_env = yes`, so a command run from a web
     * request inherits no PATH at all. Every `command -v` then fails and the
     * panel decides nothing is installed on a machine running the lot — which
     * is exactly what the Databases page did, while the queue worker, given a
     * normal environment by systemd, disagreed.
     */
    public function test_commands_get_a_path_whatever_the_caller_inherited(): void
    {
        $path = $this->environment()['PATH'];

        foreach (['/usr/bin', '/usr/sbin', '/bin', '/sbin', '/usr/local/bin'] as $directory) {
            $this->assertStringContainsString($directory, $path);
        }

        // And wherever this PHP itself lives, for boxes that put it elsewhere.
        $this->assertStringContainsString(dirname(PHP_BINARY), $path);
    }

    public function test_commands_get_a_home_because_composer_and_npm_write_there(): void
    {
        $this->assertNotSame('', $this->environment()['HOME']);
    }

    /** The end of it: a probe has to find a system binary. */
    public function test_a_probe_finds_something_that_is_definitely_installed(): void
    {
        [, $code] = (new LocalConnection(30))->run('command -v ls >/dev/null 2>&1');

        $this->assertSame(0, $code);
    }
}
