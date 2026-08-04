<?php

namespace App\Services\Tasks;

use App\Models\ActivityLog;
use App\Services\Shell\LocalConnection;
use Illuminate\Support\Str;
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

    /** @var array<string, string> group => why it failed */
    protected array $failures = [];

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

                // A step that belongs to one service takes only that service
                // down. Installing nine things and abandoning eight of them
                // because the fifth would not configure is not a batch, it is
                // a queue that stops at the first pothole — and it is why a
                // failed mail install used to leave nginx and MariaDB marked
                // failed beside it.
                if ($step->group !== null) {
                    $this->failures[$step->group] = $e->getMessage();
                    $this->skipGroup($step->group);
                    $this->flush();

                    continue;
                }

                // Ungrouped steps are the shared ones — waiting for apt, the
                // single install transaction. Nothing after them can work.
                $this->finish('failed', $e->getMessage());

                return false;
            }

            $this->flush();
        }

        if ($this->failures !== []) {
            $this->finish('failed', $this->failureSummary());

            return false;
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
                throw new RuntimeException($this->failureMessage($command, $code, $output));
            }
        }
    }

    /**
     * What went wrong, in the one line that ends up beside the failed row.
     *
     * The full console is kept on the activity log, but the summary is all
     * most failures are ever read through — and a summary that names only the
     * command ("Command exited 1: sudo systemctl restart postfix dovecot")
     * says nothing about the cause. The last few lines of output usually do.
     */
    protected function failureMessage(string $command, int $code, string $output): string
    {
        $message = "Command exited {$code}: {$command}";

        $lines = array_values(array_filter(
            array_map('rtrim', preg_split('/\r?\n/', trim($output)) ?: []),
            fn (string $line) => $line !== '' && $line !== '--- journal ---',
        ));

        if ($lines === []) {
            return $message;
        }

        $tail = array_slice($lines, -5);

        return $message.' — '.Str::limit(implode(' | ', $tail), 500);
    }

    protected function runCallback(Step $step): void
    {
        $output = ($step->callback)($this->ssh, $this);

        if (is_string($output) && $output !== '') {
            $this->write($output."\n");
        }
    }

    /**
     * Why each part of the batch failed, keyed by group.
     *
     * The caller uses this to blame the right service rather than painting
     * every unfinished one with the same error.
     *
     * @return array<string, string>
     */
    public function failures(): array
    {
        return $this->failures;
    }

    protected function failureSummary(): string
    {
        return count($this->failures).' of the batch failed: '.implode('; ', array_map(
            fn (string $group, string $why) => $group.' — '.$why,
            array_keys($this->failures),
            $this->failures
        ));
    }

    /** Has this part of the batch already been written off? */
    public function isSkipped(string $group): bool
    {
        return in_array($group, $this->skipGroups, true);
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
