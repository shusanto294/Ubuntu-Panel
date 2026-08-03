<?php

namespace App\Services\Tasks;

use App\Models\ActivityLog;
use App\Services\Shell\LocalConnection;
use RuntimeException;
use Throwable;

/**
 * Executes a list of Steps over SSH, flushing progress to the ActivityLog after
 * every command so the browser can poll and watch it happen.
 */
class TaskRunner
{
    protected int $index = 0;

    /** @var array<int, array<string, mixed>> */
    protected array $state = [];

    /** Groups whose remaining steps should be abandoned. */
    protected array $skipGroups = [];

    public function __construct(
        protected ActivityLog $log,
        protected LocalConnection $ssh,
    ) {}

    public static function for(ActivityLog $log, LocalConnection $ssh): self
    {
        return new self($log, $ssh);
    }

    /**
     * Run every step. Returns true when all required steps succeeded.
     *
     * @param  array<int, Step>  $steps
     */
    public function run(array $steps): bool
    {
        $this->state = array_map(fn (Step $step) => [
            'name' => $step->name,
            'status' => 'pending',
            'optional' => $step->optional,
        ], $steps);

        $this->log->forceFill([
            'status' => 'running',
            'steps' => $this->state,
            'progress' => 0,
            'started_at' => now(),
        ])->save();

        foreach ($steps as $i => $step) {
            $this->index = $i;

            // An earlier step already established this work cannot succeed.
            if ($step->group !== null && in_array($step->group, $this->skipGroups, true)) {
                $this->markStep('skipped');
                $this->flush();

                continue;
            }

            $this->markStep('running');
            $this->write("\n\033[1m== {$step->name}\033[0m\n");
            $this->log->current_step = $step->name;
            $this->flush();

            try {
                $step->callback
                    ? $this->runCallback($step)
                    : $this->runCommands($step);

                $this->markStep('success');
            } catch (Throwable $e) {
                $this->write("\n!! {$e->getMessage()}\n");

                if ($step->optional) {
                    $this->markStep('skipped');
                    $this->flush();

                    continue;
                }

                $this->markStep('failed');
                $this->finish('failed', $e->getMessage());

                return false;
            }

            $this->flush();
        }

        $this->finish('success', 'Completed '.count($steps).' steps.');

        return true;
    }

    protected function runCommands(Step $step): void
    {
        foreach ($step->commands as $command) {
            $this->write("$ {$command}\n");
            [$output, $code] = $this->ssh->run($command);

            if ($output !== '') {
                $this->write($output."\n");
            }

            $this->flush();

            if ($code !== 0) {
                throw new RuntimeException("Command exited {$code}: {$command}");
            }
        }
    }

    protected function runCallback(Step $step): void
    {
        $output = ($step->callback)($this->ssh, $this);

        if (is_string($output) && $output !== '') {
            $this->write($output."\n");
        }
    }

    /**
     * Abandon every remaining step in a group. Used when one part of a batch
     * fails and the rest of that part would only fail after it.
     */
    public function skipGroup(string $group): void
    {
        if (! in_array($group, $this->skipGroups, true)) {
            $this->skipGroups[] = $group;
        }
    }

    /** Steps may push extra lines into the console (used by callback steps). */
    public function write(string $text): void
    {
        $this->log->appendOutput($text);
    }

    public function flush(): void
    {
        $this->log->steps = $this->state;
        $this->log->progress = $this->calculateProgress();
        $this->log->save();
    }

    protected function markStep(string $status): void
    {
        if (isset($this->state[$this->index])) {
            $this->state[$this->index]['status'] = $status;
        }
    }

    protected function calculateProgress(): int
    {
        $total = count($this->state);

        if ($total === 0) {
            return 100;
        }

        $done = count(array_filter(
            $this->state,
            fn ($step) => in_array($step['status'], ['success', 'skipped', 'failed'], true)
        ));

        return (int) round($done / $total * 100);
    }

    protected function finish(string $status, string $message): void
    {
        $this->log->forceFill([
            'status' => $status,
            'message' => $message,
            'steps' => $this->state,
            'current_step' => null,
            'progress' => $status === 'success' ? 100 : $this->calculateProgress(),
            'finished_at' => now(),
        ])->save();
    }

    public function log(): ActivityLog
    {
        return $this->log;
    }

    public function ssh(): LocalConnection
    {
        return $this->ssh;
    }
}
