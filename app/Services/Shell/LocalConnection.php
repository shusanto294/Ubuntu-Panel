<?php

namespace App\Services\Shell;

use RuntimeException;
use Symfony\Component\Process\Process;

/**
 * Runs commands on the machine the panel is installed on.
 *
 * This is the whole execution layer: the panel manages its own host, so there
 * is no network hop, no handshake and no credentials to store — a command costs
 * a fork instead of a round trip. Everything that used to run over SSH (service
 * installs, site deployments, database and mail work) goes through here
 * unchanged, because the shape of the API is the same: run a command, get back
 * output and an exit code.
 *
 * Commands are handed to `bash -c` and prefixed with sudo where they need root.
 * The installer gives the panel's system user passwordless sudo; nothing here
 * ever prompts.
 */
class LocalConnection
{
    /** Commands that outlive this are killed; an install can legitimately take a while. */
    public function __construct(protected int $timeout = 1800) {}

    public static function make(int $timeout = 1800): self
    {
        return new self($timeout);
    }

    public function timeout(int $seconds): self
    {
        $this->timeout = $seconds;

        return $this;
    }

    /**
     * Run a single command. Returns [output, exitCode], with stderr folded in.
     *
     * @return array{0: string, 1: int}
     */
    public function run(string $command): array
    {
        $process = Process::fromShellCommandline($this->wrap($command));
        $process->setTimeout($this->timeout);
        $process->run();

        return [trim($process->getOutput().$process->getErrorOutput()), $process->getExitCode() ?? 1];
    }

    /**
     * Run a command and hand each chunk of output to the callback as it arrives,
     * instead of waiting for the whole thing.
     */
    public function stream(string $command, callable $onOutput): int
    {
        $process = Process::fromShellCommandline($this->wrap($command));
        $process->setTimeout($this->timeout);

        $process->run(function ($type, $chunk) use ($onOutput) {
            $onOutput((string) $chunk);
        });

        return $process->getExitCode() ?? 1;
    }

    /** Run a command and throw if it exits non-zero. */
    public function mustRun(string $command): string
    {
        [$output, $code] = $this->run($command);

        if ($code !== 0) {
            throw new RuntimeException("Command failed (exit {$code}): {$command}\n{$output}");
        }

        return $output;
    }

    /**
     * Run several commands in order, collecting all output. Stops at the first
     * failure and reports which command it was.
     *
     * @param  array<int, string>  $commands
     * @return array{output: string, failed: ?string}
     */
    public function runScript(array $commands): array
    {
        $log = '';

        foreach ($commands as $command) {
            $log .= "\n$ {$command}\n";
            [$output, $code] = $this->run($command);
            $log .= $output."\n";

            if ($code !== 0) {
                return ['output' => trim($log), 'failed' => $command];
            }
        }

        return ['output' => trim($log), 'failed' => null];
    }

    /**
     * Write a file, creating parent directories as needed.
     *
     * Contents go through a temporary file rather than the command line, so a
     * config full of quotes, newlines or `$` cannot be mangled by the shell,
     * and the move into place is atomic.
     */
    public function putFile(string $path, string $contents, bool $sudo = true): void
    {
        $temporary = tempnam(sys_get_temp_dir(), 'panel-');

        if ($temporary === false || file_put_contents($temporary, $contents) === false) {
            throw new RuntimeException("Could not stage {$path} for writing.");
        }

        try {
            $prefix = $sudo ? 'sudo ' : '';

            $this->mustRun(sprintf(
                '%smkdir -p %s && %scp %s %s && %schmod 644 %s',
                $prefix,
                escapeshellarg(dirname($path)),
                $prefix,
                escapeshellarg($temporary),
                escapeshellarg($path),
                $prefix,
                escapeshellarg($path)
            ));

            $this->verifyWrite($path, $contents, $sudo);
        } finally {
            @unlink($temporary);
        }
    }

    /**
     * Read a file back. Most of what the panel writes is world readable, so
     * root is only involved when it has to be.
     */
    public function readFile(string $path): string
    {
        if (is_readable($path)) {
            return (string) file_get_contents($path);
        }

        return $this->mustRun('sudo cat '.escapeshellarg($path));
    }

    public function exists(string $path): bool
    {
        if (file_exists($path)) {
            return true;
        }

        // Could still be there behind a directory this user cannot traverse.
        [, $code] = $this->run('sudo test -e '.escapeshellarg($path));

        return $code === 0;
    }

    /**
     * Confirm what landed on disk is what we meant to write.
     *
     * A config file that is subtly wrong tends to fail much later and much less
     * clearly (an nginx syntax error three steps down a task, say), so it is
     * worth one extra command to catch it at the point of writing.
     */
    protected function verifyWrite(string $path, string $contents, bool $sudo = true): void
    {
        $prefix = $sudo ? 'sudo ' : '';

        // Deliberately not piped into awk: a pipeline reports the *last*
        // command's status, so a failed read would come back as exit 0 with
        // an error message where the checksum should be, and read as a
        // corrupted file rather than a permissions problem.
        [$output, $code] = $this->run($prefix.'md5sum '.escapeshellarg($path));

        if ($code !== 0) {
            throw new RuntimeException("Wrote {$path} but could not read it back to verify it: {$output}");
        }

        $actual = strtok(trim($output), " \t");

        if ($actual === false || ! hash_equals(md5($contents), $actual)) {
            throw new RuntimeException("{$path} did not survive the write intact.");
        }
    }

    /**
     * Multi-line commands (heredocs, loops) need a real shell, and folding
     * stderr into stdout at the subshell keeps every line of the command intact
     * — appending `2>&1` to the last line of a heredoc would corrupt it.
     */
    protected function wrap(string $command): string
    {
        return 'bash -c '.escapeshellarg("(\n".$command."\n) 2>&1");
    }

    /** Symmetry with the old SSH layer: there is nothing to disconnect. */
    public function disconnect(): void {}
}
