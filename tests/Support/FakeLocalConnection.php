<?php

namespace Tests\Support;

use App\Services\Shell\LocalConnection;

/**
 * A LocalConnection that answers from a canned map instead of running anything.
 *
 * Tests bind this in place of the real shell, so the whole service layer —
 * installs, site deployments, database and mail work — can be exercised without
 * touching the machine running the suite.
 */
class FakeLocalConnection extends LocalConnection
{
    /** @var array<int, string> every command the code under test ran */
    public array $ran = [];

    /** @var array<string, string> path => contents */
    public array $files = [];

    /**
     * @param  array<string, array{0: string, 1: int}>  $responses  matched by substring
     */
    public function __construct(
        protected array $responses = [],
        protected int $defaultCode = 0,
    ) {
        parent::__construct(60);
    }

    public function run(string $command): array
    {
        $this->ran[] = $command;

        foreach ($this->responses as $needle => $response) {
            if (str_contains($command, $needle)) {
                return $response;
            }
        }

        return ["ok: {$command}", $this->defaultCode];
    }

    public function stream(string $command, callable $onOutput): int
    {
        [$output, $code] = $this->run($command);

        $onOutput($output);

        return $code;
    }

    public function putFile(string $path, string $contents, bool $sudo = true): void
    {
        $this->ran[] = "write {$path}";
        $this->files[$path] = $contents;
    }

    public function readFile(string $path): string
    {
        return $this->files[$path] ?? '';
    }

    public function exists(string $path): bool
    {
        return isset($this->files[$path]);
    }

    public function timeout(int $seconds): static
    {
        return $this;
    }

    public function disconnect(): void {}

    /** Did the code under test run a command containing this? */
    public function ranCommandContaining(string $needle): bool
    {
        foreach ($this->ran as $command) {
            if (str_contains($command, $needle)) {
                return true;
            }
        }

        return false;
    }
}
