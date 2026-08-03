<?php

namespace App\Services\Tasks;

use Closure;

/**
 * A single named unit of work inside a task.
 *
 * Either a list of shell commands, or a closure that receives the LocalConnection
 * and the TaskRunner and returns a string of output (used for steps that need
 * PHP-side logic).
 */
class Step
{
    /**
     * @param  array<int, string>  $commands
     * @param  string|null  $group  steps sharing a group can be abandoned together
     */
    public function __construct(
        public string $name,
        public array $commands = [],
        public ?Closure $callback = null,
        public bool $optional = false,
        public ?string $group = null,
    ) {}

    /**
     * @param  array<int, string>|string  $commands
     */
    public static function make(string $name, array|string $commands, bool $optional = false): self
    {
        return new self($name, is_array($commands) ? $commands : [$commands], null, $optional);
    }

    public static function call(string $name, Closure $callback, bool $optional = false): self
    {
        return new self($name, [], $callback, $optional);
    }

    /**
     * Tag the step so the runner can skip it if that part of the work is
     * already known to have failed.
     */
    public function for(string $group): self
    {
        $this->group = $group;

        return $this;
    }
}
