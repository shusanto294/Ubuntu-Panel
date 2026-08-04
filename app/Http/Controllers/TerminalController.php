<?php

namespace App\Http\Controllers;

use App\Services\Terminal\TerminalTicket;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * Hands the browser a one-shot ticket for the websocket terminal daemon.
 * Everything after this happens over the socket, not over HTTP.
 */
class TerminalController extends Controller
{
    public function ticket(Request $request): JsonResponse
    {
        return response()->json([
            'ticket' => TerminalTicket::issue($request->user()),
            'url' => $this->socketUrl(),
            'expires_in' => TerminalTicket::TTL_SECONDS,
        ]);
    }

    /**
     * Where the browser should dial.
     *
     * A path (the default) means "same origin as this page" and is resolved in
     * the browser, so it stays correct whether the panel is reached by IP, by
     * hostname, over TLS or not. An explicitly configured absolute URL bypasses
     * that — for a daemon genuinely running somewhere else.
     *
     * Except a loopback one. `ws://127.0.0.1:6001` is the daemon's bind address
     * on *this* machine, and to a browser it means the machine the browser is
     * running on, so it can only ever fail. That used to be the default, and a
     * config cache written before it changed still hands it out; ignoring it
     * here means the terminal comes back on its own rather than staying broken
     * until someone works out which cache to clear.
     */
    protected function socketUrl(): string
    {
        $configured = config('panel.terminal.url');

        if (is_string($configured) && $configured !== '' && ! $this->isLoopback($configured)) {
            return $configured;
        }

        return '/'.ltrim((string) config('panel.terminal.path', '/terminal-ws'), '/');
    }

    protected function isLoopback(string $url): bool
    {
        $host = strtolower((string) parse_url($url, PHP_URL_HOST));

        return $host === 'localhost'
            || $host === '::1'
            || $host === '[::1]'
            || str_starts_with($host, '127.');
    }
}
