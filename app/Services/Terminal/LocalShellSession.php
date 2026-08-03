<?php

namespace App\Services\Terminal;

use RuntimeException;

/**
 * A real login shell on this machine, backed by a pty.
 *
 * The panel runs on the box it manages, so the terminal no longer goes over the
 * network: bytes go straight into a local shell and come back out of its tty.
 * Full-screen programs (vim, top, htop) work because it is a genuine pty, not a
 * pipe.
 */
class LocalShellSession
{
    /** @var resource|null */
    protected $process = null;

    /** @var array<int, resource> */
    protected array $pipes = [];

    protected bool $closed = false;

    public function __construct(
        protected int $columns = 120,
        protected int $rows = 30,
    ) {}

    public function open(): void
    {
        $descriptors = [
            ['pty'],
            ['pty'],
            ['pty'],
        ];

        $environment = [
            'TERM' => 'xterm-256color',
            'COLUMNS' => (string) $this->columns,
            'LINES' => (string) $this->rows,
            'HOME' => getenv('HOME') ?: '/root',
            'PATH' => getenv('PATH') ?: '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
            'LANG' => getenv('LANG') ?: 'C.UTF-8',
        ];

        // The panel's user has passwordless sudo; the terminal is an admin tool,
        // so it gets a root login shell the same as any other control panel.
        $process = @proc_open(
            $this->command(),
            $descriptors,
            $pipes,
            base_path(),
            $environment
        );

        if (! is_resource($process)) {
            throw new RuntimeException('Could not start a shell on this machine.');
        }

        foreach ($pipes as $pipe) {
            if (is_resource($pipe)) {
                stream_set_blocking($pipe, false);
            }
        }

        $this->process = $process;
        $this->pipes = $pipes;
    }

    protected function command(): string
    {
        // Already root (a panel installed by the root user): no sudo needed.
        return function_exists('posix_geteuid') && posix_geteuid() === 0
            ? '/bin/bash -il'
            : 'sudo -n -i';
    }

    /** The stream the event loop watches, so idle sessions cost nothing. */
    public function socket()
    {
        return $this->pipes[1] ?? null;
    }

    /** Whatever the tty has produced since the last call. */
    public function read(): string
    {
        $output = '';

        foreach ([1, 2] as $descriptor) {
            $pipe = $this->pipes[$descriptor] ?? null;

            if (is_resource($pipe)) {
                $chunk = stream_get_contents($pipe);

                if ($chunk !== false) {
                    $output .= $chunk;
                }
            }
        }

        return $output;
    }

    public function write(string $data): void
    {
        $pipe = $this->pipes[0] ?? null;

        if (is_resource($pipe)) {
            @fwrite($pipe, $data);
        }
    }

    /**
     * A pty's window size cannot be changed from PHP without an ioctl, so the
     * shell is told the new size instead. Programs started after a resize pick
     * it up; a full-screen program already running keeps the old geometry.
     */
    public function resize(int $columns, int $rows): void
    {
        $this->columns = $columns;
        $this->rows = $rows;

        $this->write(sprintf("stty columns %d rows %d 2>/dev/null\n", $columns, $rows));
    }

    public function isClosed(): bool
    {
        if ($this->closed || ! is_resource($this->process)) {
            return true;
        }

        $status = proc_get_status($this->process);

        return ! ($status['running'] ?? false);
    }

    public function close(): void
    {
        $this->closed = true;

        foreach ($this->pipes as $pipe) {
            if (is_resource($pipe)) {
                @fclose($pipe);
            }
        }

        $this->pipes = [];

        if (is_resource($this->process)) {
            @proc_terminate($this->process);
            @proc_close($this->process);
        }

        $this->process = null;
    }
}
