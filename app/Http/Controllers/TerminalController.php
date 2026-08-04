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
     * hostname, over TLS or not. Only an explicitly configured absolute URL
     * bypasses that.
     */
    protected function socketUrl(): string
    {
        $configured = config('panel.terminal.url');

        if (is_string($configured) && $configured !== '') {
            return $configured;
        }

        return '/'.ltrim((string) config('panel.terminal.path', '/terminal-ws'), '/');
    }
}
