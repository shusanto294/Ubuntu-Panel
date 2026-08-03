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
            'url' => config('panel.terminal.url'),
            'expires_in' => TerminalTicket::TTL_SECONDS,
        ]);
    }
}
