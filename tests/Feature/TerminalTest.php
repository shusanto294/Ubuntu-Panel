<?php

namespace Tests\Feature;

use App\Models\User;
use App\Services\Terminal\TerminalTicket;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Cache;
use Tests\TestCase;

class TerminalTest extends TestCase
{
    use RefreshDatabase;

    public function test_the_owner_gets_a_ticket_and_the_socket_url(): void
    {
        config(['panel.terminal.url' => 'ws://127.0.0.1:6001']);

        $user = User::factory()->create();

        $response = $this->actingAs($user)
            ->postJson(route('terminal.ticket'))
            ->assertOk()
            ->assertJsonPath('url', 'ws://127.0.0.1:6001')
            ->assertJsonPath('expires_in', TerminalTicket::TTL_SECONDS);

        $ticket = $response->json('ticket');

        $this->assertMatchesRegularExpression('/^[A-Za-z0-9]{64}$/', $ticket);

        $claims = TerminalTicket::redeem($ticket);

        $this->assertSame($user->id, $claims['user_id']);
            }

    public function test_a_ticket_only_works_once(): void
    {
        $user = User::factory()->create();
        $ticket = TerminalTicket::issue($user, null);

        $this->assertNotNull(TerminalTicket::redeem($ticket));
        $this->assertNull(TerminalTicket::redeem($ticket));
    }

    public function test_an_expired_ticket_is_rejected(): void
    {
        $user = User::factory()->create();
        $ticket = TerminalTicket::issue($user, null);

        $this->travel(TerminalTicket::TTL_SECONDS + 5)->seconds();

        $this->assertNull(TerminalTicket::redeem($ticket));
    }

    public function test_a_malformed_ticket_never_reaches_the_cache(): void
    {
        Cache::shouldReceive('pull')->never();

        $this->assertNull(TerminalTicket::redeem('../../etc/passwd'));
        $this->assertNull(TerminalTicket::redeem(''));
        $this->assertNull(TerminalTicket::redeem(str_repeat('a', 63)));
    }

    public function test_the_raw_ticket_is_not_the_cache_key(): void
    {
        $user = User::factory()->create();
        $ticket = TerminalTicket::issue($user, null);

        // Anyone who can read the cache store should not learn a usable ticket.
        $this->assertNull(Cache::get('terminal-ticket:'.$ticket));
        $this->assertNotNull(Cache::get('terminal-ticket:'.hash('sha256', $ticket)));
    }

    public function test_another_user_cannot_get_a_ticket_for_your_server(): void
    {
        $owner = User::factory()->create();
        $intruder = User::factory()->create();

        $this->actingAs($intruder)
            ->postJson(route('terminal.ticket'))
            ->assertForbidden();
    }

    public function test_guests_are_rejected(): void
    {

        $this->postJson(route('terminal.ticket'))
            ->assertUnauthorized();
    }

    public function test_the_terminal_server_command_is_registered(): void
    {
        $this->assertArrayHasKey(
            'panel:terminal-server',
            $this->app[\Illuminate\Contracts\Console\Kernel::class]->all()
        );
    }
}
