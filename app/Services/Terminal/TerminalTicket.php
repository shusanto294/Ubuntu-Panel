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

    /** What a ticket may be traded for. */
    public const MODES = ['shell', 'stream'];

    /**
     * A ticket is good for one thing only.
     *
     * The dashboard needs a socket to watch numbers on; that is not a reason
     * for the page showing them to be holding something that opens a root
     * shell. Both require a signed-in session to obtain, so this is not a
     * privilege boundary so much as a blast radius one — but it costs a string.
     */
    public static function issue(User $user, string $mode = 'shell'): string
    {
        $ticket = Str::random(64);

        Cache::put(self::key($ticket), [
            'user_id' => $user->id,
            'mode' => in_array($mode, self::MODES, true) ? $mode : 'shell',
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
