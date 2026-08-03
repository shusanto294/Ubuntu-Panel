<?php

namespace App\Console\Commands;

use App\Models\ActivityLog;
use App\Models\User;
use App\Services\System\MetricsStream;
use App\Services\Terminal\LocalShellSession;
use App\Services\Terminal\TerminalTicket;
use Illuminate\Console\Command;
use Throwable;
use Workerman\Connection\TcpConnection;
use Workerman\Timer;
use Workerman\Worker;

/**
 * WebSocket ↔ pty bridge. Each browser terminal gets its own login shell on
 * this machine; keystrokes and output are relayed byte for byte, so vim, top
 * and anything else that needs a tty behave exactly as they do over SSH.
 */
class TerminalServer extends Command
{
    protected $signature = 'panel:terminal-server
                            {action=start : start, stop, restart, status or reload}
                            {--host= : Address to bind (default from config)}
                            {--port= : Port to bind (default from config)}
                            {--d|daemon : Run in the background}';

    protected $description = 'Run the websocket terminal server that gives the panel real shells';

    /** @var array<int, array{session: LocalShellSession, user: User, timer: int}> */
    protected array $sessions = [];

    public function handle(): int
    {
        $host = $this->option('host') ?: config('panel.terminal.host');
        $port = (int) ($this->option('port') ?: config('panel.terminal.port'));

        $worker = new Worker("websocket://{$host}:{$port}");
        $worker->name = 'ubuntu-panel-terminal';
        $worker->count = 1;

        // The query string is only available during the handshake; the session
        // itself has to wait until the 101 has actually gone out.
        $worker->onWebSocketConnect = fn (TcpConnection $connection, $request) => $this->rememberQuery($connection, $request);
        $worker->onWebSocketConnected = fn (TcpConnection $connection) => $this->openSession($connection);
        $worker->onMessage = fn (TcpConnection $connection, $payload) => $this->onMessage($connection, $payload);
        $worker->onClose = fn (TcpConnection $connection) => $this->teardown($connection);
        $worker->onError = fn (TcpConnection $connection) => $this->teardown($connection);

        Worker::$pidFile = storage_path('app/terminal-server.pid');
        Worker::$logFile = storage_path('logs/terminal-server.log');
        Worker::$stdoutFile = $this->option('daemon') ? storage_path('logs/terminal-server.out') : '/dev/stdout';

        if ($this->action() === 'start') {
            $this->info("Terminal server listening on ws://{$host}:{$port}");
        }

        // Workerman reads its own start/stop verbs from argv, which here belongs
        // to artisan, so hand it the arguments it expects.
        $this->rewriteArgv();

        Worker::runAll();

        return self::SUCCESS;
    }

    /** Stash the connection's query string for the moment the socket is live. */
    protected function rememberQuery(TcpConnection $connection, mixed $request): void
    {
        $connection->context->panelQuery = $this->queryFrom($request);
    }

    /**
     * Validate the ticket from the connection URL and open the shell.
     */
    protected function openSession(TcpConnection $connection): void
    {
        $query = $connection->context->panelQuery ?? [];
        $claims = isset($query['ticket']) ? TerminalTicket::redeem((string) $query['ticket']) : null;

        if (! $claims) {
            $this->send($connection, ['type' => 'status', 'state' => 'denied', 'message' => 'This terminal session expired. Reload the page.']);
            $connection->close();

            return;
        }

        $user = User::find($claims['user_id']);

        if (! $user) {
            $this->send($connection, ['type' => 'status', 'state' => 'denied', 'message' => 'Unknown account.']);
            $connection->close();

            return;
        }

        $columns = max(20, min(500, (int) ($query['cols'] ?? 120)));
        $rows = max(5, min(200, (int) ($query['rows'] ?? 30)));

        $session = new LocalShellSession($columns, $rows);

        try {
            $session->open();
        } catch (Throwable $e) {
            $this->send($connection, ['type' => 'status', 'state' => 'failed', 'message' => $e->getMessage()]);
            $connection->close();

            return;
        }

        if ($mode === 'shell') {
            $this->log($user, 'terminal.open', 'success', 'Shell opened from the panel.');
        }

        $this->sessions[$connection->id] = [
            'session' => $session,
            'user' => $user,
            'timer' => Timer::add(0.02, fn () => $this->pump($connection)),
        ];

        $this->send($connection, [
            'type' => 'status',
            'state' => 'connected',
            'message' => 'Shell on '.(gethostname() ?: 'this server'),
        ]);
    }

    /** Relay browser input and resize events to the shell. */
    protected function onMessage(TcpConnection $connection, mixed $payload): void
    {
        $entry = $this->sessions[$connection->id] ?? null;

        if (! $entry || ($entry['mode'] ?? 'shell') !== 'shell') {
            return;
        }

        $frame = json_decode((string) $payload, true);

        if (! is_array($frame)) {
            return;
        }

        match ($frame['type'] ?? '') {
            'input' => $entry['session']->write((string) ($frame['data'] ?? '')),
            'resize' => $entry['session']->resize(
                max(20, min(500, (int) ($frame['cols'] ?? 120))),
                max(5, min(200, (int) ($frame['rows'] ?? 30)))
            ),
            default => null,
        };
    }

    /** Move whatever the tty produced out to the browser. */
    protected function pump(TcpConnection $connection): void
    {
        $entry = $this->sessions[$connection->id] ?? null;

        if (! $entry) {
            return;
        }

        $session = $entry['session'];
        $socket = $session->socket();

        // Only touch the (blocking) SSH read when there is something to read.
        if (is_resource($socket)) {
            $read = [$socket];
            $write = $except = [];

            if (@stream_select($read, $write, $except, 0) === 0) {
                if ($session->isClosed()) {
                    $this->finish($connection, 'The remote shell closed.');
                }

                return;
            }
        }

        $output = $session->read();

        if ($output !== '') {
            $this->send($connection, ['type' => 'output', 'data' => $output]);
        }

        if ($session->isClosed()) {
            $this->finish($connection, 'The remote shell closed.');
        }
    }

    protected function finish(TcpConnection $connection, string $message): void
    {
        $this->send($connection, ['type' => 'status', 'state' => 'closed', 'message' => $message]);
        $this->teardown($connection);
        $connection->close();
    }

    protected function teardown(TcpConnection $connection): void
    {
        $entry = $this->sessions[$connection->id] ?? null;

        if (! $entry) {
            return;
        }

        Timer::del($entry['timer']);

        $entry['session']->close();

        $this->log($entry['user'], 'terminal.close', 'success', 'Shell closed.');

        unset($this->sessions[$connection->id]);
    }

    protected function send(TcpConnection $connection, array $frame): void
    {
        $connection->send(json_encode($frame));
    }

    protected function log(User $user, string $action, string $status, string $message): void
    {
        try {
            ActivityLog::record([
                'user_id' => $user->id,
                'type' => 'terminal',
                'action' => $action,
                'status' => $status,
                'message' => $message,
                'progress' => 100,
                'started_at' => now(),
                'finished_at' => now(),
            ]);
        } catch (Throwable $e) {
            // The audit trail must never take the shell down with it.
        }
    }

    /**
     * @return array<string, string>
     */
    protected function queryFrom(mixed $request): array
    {
        // Workerman hands over a Request object; fall back to the raw buffer so
        // this keeps working if that ever changes.
        $target = is_object($request) && method_exists($request, 'uri')
            ? (string) $request->uri()
            : (is_string($request) ? (string) strtok(substr($request, (int) strpos($request, ' ') + 1), ' ') : '');

        parse_str((string) parse_url($target, PHP_URL_QUERY), $query);

        return array_map('strval', array_filter($query, 'is_scalar'));
    }

    /** start | stop | restart | status | reload, defaulting to start. */
    protected function action(): string
    {
        $action = (string) $this->argument('action');

        return in_array($action, ['start', 'stop', 'restart', 'status', 'reload'], true)
            ? $action
            : 'start';
    }

    /** Workerman parses $argv itself; artisan's arguments would confuse it. */
    protected function rewriteArgv(): void
    {
        global $argv;

        $argv = ['panel:terminal-server', $this->action()];

        if ($this->option('daemon')) {
            $argv[] = '-d';
        }
    }
}
