<?php

namespace App\Services\Terminal;

use App\Models\User;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Str;

/**
 * A single-use, short-lived handle the browser trades for a shell.
 *
 * The websocket daemon runs in a different process from the web request, so the
 * ticket carries nothing sensitive — only the user id. The daemon is the same
 * Laravel app, so it opens the shell itself.
 */
class TerminalTicket
{
    public const TTL_SECONDS = 60;

    public static function issue(User $user): string
    {
        $ticket = Str::random(64);

        Cache::put(self::key($ticket), [
            'user_id' => $user->id,
            'issued_at' => now()->toDateTimeString(),
        ], self::TTL_SECONDS);

        return $ticket;
    }

    /**
     * Redeem a ticket. Returns null if it is unknown, expired or already used.
     */
    public static function redeem(string $ticket): ?array
    {
        if ($ticket === '' || ! preg_match('/^[A-Za-z0-9]{64}$/', $ticket)) {
            return null;
        }

        return Cache::pull(self::key($ticket));
    }

    protected static function key(string $ticket): string
    {
        return 'terminal-ticket:'.hash('sha256', $ticket);
    }
}
