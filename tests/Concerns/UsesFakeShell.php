<?php

namespace Tests\Concerns;

use App\Services\Shell\LocalConnection;
use Tests\Support\FakeLocalConnection;

/**
 * Swaps the machine's shell for a fake one, so a test never runs real commands.
 */
trait UsesFakeShell
{
    protected FakeLocalConnection $shell;

    /**
     * @param  array<string, array{0: string, 1: int}>  $responses
     */
    protected function fakeShell(array $responses = [], int $defaultCode = 0): FakeLocalConnection
    {
        $this->shell = new FakeLocalConnection($responses, $defaultCode);

        $this->app->instance(LocalConnection::class, $this->shell);

        return $this->shell;
    }
}
