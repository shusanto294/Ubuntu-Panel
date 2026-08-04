<?php

namespace App\Console\Commands;

use App\Models\ActivityLog;
use App\Models\User;
use App\Services\System\MetricsStream;
use App\Services\Terminal\LocalShellSession;
use App\Services\Terminal\TerminalTicket;
use Illuminate\Console\Command;
use Throwable;
use Illuminate\Support\Facades\DB;
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

    /**
     * Connections watching things rather than typing at them.
     *
     * @var array<int, array{connection: TcpConnection, user: User, topics: array<int, string>}>
     */
    protected array $watchers = [];

    protected ?MetricsStream $metrics = null;

    /** The last payload sent per topic, so nothing unchanged is sent twice. */
    protected array $lastSent = [];

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

        // One timer for the whole process rather than one per viewer: ten
        // people watching the dashboard costs the same as one.
        $worker->onWorkerStart = function () {
            $this->metrics = app(MetricsStream::class);

            Timer::add(MetricsStream::interval(), fn () => $this->broadcast());
        };

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

        $mode = ($query['mode'] ?? 'shell') === 'stream' ? 'stream' : 'shell';

        // The ticket says what it was issued for, and that is what it gets.
        if (($claims['mode'] ?? 'shell') !== $mode) {
            $this->send($connection, [
                'type' => 'status',
                'state' => 'denied',
                'message' => 'This ticket is not valid for that.',
            ]);
            $connection->close();

            return;
        }

        if ($mode === 'stream') {
            $this->watchers[$connection->id] = [
                'connection' => $connection,
                'user' => $user,
                'topics' => [],
            ];

            $this->send($connection, [
                'type' => 'status',
                'state' => 'connected',
                'message' => 'Streaming from '.(gethostname() ?: 'this server'),
            ]);

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

        $this->log($user, 'terminal.open', 'success', 'Shell opened from the panel.');

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
        $frame = json_decode((string) $payload, true);

        if (! is_array($frame)) {
            return;
        }

        if (isset($this->watchers[$connection->id])) {
            $this->subscribe($connection, $frame);

            return;
        }

        $entry = $this->sessions[$connection->id] ?? null;

        if (! $entry) {
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

    /**
     * What this connection wants to be told about.
     *
     * Topics are `metrics` and `task:<id>`. Anything else is ignored rather
     * than refused: a browser running older assets after an update should lose
     * a feature, not the connection.
     */
    protected function subscribe(TcpConnection $connection, array $frame): void
    {
        if (($frame['type'] ?? '') !== 'subscribe') {
            return;
        }

        $topics = [];

        foreach ((array) ($frame['topics'] ?? []) as $topic) {
            $topic = (string) $topic;

            if ($topic === 'metrics' || preg_match('/^task:\d+$/', $topic)) {
                $topics[] = $topic;
            }
        }

        $this->watchers[$connection->id]['topics'] = array_values(array_unique($topics));

        // Answer immediately rather than making the page wait for the next
        // tick — a console that appears blank for a second reads as broken.
        $this->broadcast(only: $connection->id);
    }

    /**
     * Push one round of updates.
     *
     * Sampled once for everybody, and only sent where it has changed: a task
     * that has not moved since the last tick produces no traffic at all, which
     * is the difference between this and the polling it replaces.
     */
    protected function broadcast(?int $only = null): void
    {
        if ($this->watchers === []) {
            return;
        }

        $watchers = $only !== null
            ? array_filter($this->watchers, fn ($id) => $id === $only, ARRAY_FILTER_USE_KEY)
            : $this->watchers;

        $wanted = [];

        foreach ($watchers as $watcher) {
            foreach ($watcher['topics'] as $topic) {
                $wanted[$topic] = true;
            }
        }

        $payloads = [];

        if (isset($wanted['metrics'])) {
            $payloads['metrics'] = $this->metrics?->sample();
        }

        $taskIds = [];

        foreach (array_keys($wanted) as $topic) {
            if (str_starts_with($topic, 'task:')) {
                $taskIds[] = (int) substr($topic, 5);
            }
        }

        if ($taskIds !== []) {
            try {
                foreach (ActivityLog::whereIn('id', $taskIds)->get() as $task) {
                    $payloads['task:'.$task->id] = $task->toConsolePayload();
                }
            } catch (Throwable $e) {
                // This process outlives connections MySQL is willing to keep.
                // Drop it and let the next tick open a fresh one rather than
                // taking the whole daemon down with the terminals on it.
                try {
                    DB::disconnect();
                } catch (Throwable) {
                    // Nothing further to do.
                }
            }
        }

        foreach ($watchers as $id => $watcher) {
            foreach ($watcher['topics'] as $topic) {
                if (! array_key_exists($topic, $payloads) || $payloads[$topic] === null) {
                    continue;
                }

                // Metrics are new every time by definition; a task usually is
                // not, and re-sending an unchanged one is the cost this whole
                // change exists to remove.
                if ($topic !== 'metrics') {
                    $fingerprint = md5(json_encode($payloads[$topic]));

                    if (($this->lastSent[$id][$topic] ?? null) === $fingerprint) {
                        continue;
                    }

                    $this->lastSent[$id][$topic] = $fingerprint;
                }

                $this->send($watcher['connection'], [
                    'type' => 'update',
                    'topic' => $topic,
                    'data' => $payloads[$topic],
                ]);
            }
        }
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
        unset($this->watchers[$connection->id], $this->lastSent[$connection->id]);

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
