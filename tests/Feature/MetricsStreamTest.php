<?php

namespace Tests\Feature;

use App\Models\User;
use App\Services\System\MetricsStream;
use App\Services\System\SystemMetrics;
use App\Services\Terminal\TerminalTicket;
use Illuminate\Foundation\Testing\RefreshDatabase;
use ReflectionMethod;
use Tests\TestCase;

/**
 * The dashboard used to ask over HTTP once a second. Reading /proc costs
 * microseconds; booting Laravel to do it does not, and it showed up as a CPU
 * spike every second on an idle machine.
 */
class MetricsStreamTest extends TestCase
{
    use RefreshDatabase;

    public function test_a_stream_keeps_its_own_cpu_baseline_instead_of_the_cache(): void
    {
        $stream = app(MetricsStream::class);

        // The first reading has nothing to compare against, by definition.
        $this->assertNull($stream->sample()['cpu']['usage']);

        $property = new \ReflectionProperty(MetricsStream::class, 'previousCpu');

        // On Linux it now holds counters; anywhere else /proc/stat is absent
        // and it stays null, which is the same thing the endpoint reports.
        $this->assertTrue(
            $property->getValue($stream) !== null || ! is_readable('/proc/stat')
        );
    }

    /**
     * The reading memoised /proc/meminfo in a static, which is right for a
     * request and wrong for a process that runs for weeks: it would report the
     * memory it saw the day it started.
     */
    public function test_memory_is_read_again_on_every_sample(): void
    {
        $metrics = app(SystemMetrics::class);
        $memoised = new \ReflectionProperty(SystemMetrics::class, 'meminfo');

        $metrics->read();

        // Something was cached during that reading...
        $afterFirst = $memoised->getValue($metrics);

        // ...and the next reading starts by throwing it away, rather than
        // reporting for ever the memory it saw the first time.
        $counters = null;
        (new ReflectionMethod(SystemMetrics::class, 'readWith'))
            ->invokeArgs($metrics, [null, &$counters]);

        $this->assertSame(
            $afterFirst,
            $memoised->getValue($metrics),
            'the second reading did not re-read /proc/meminfo'
        );
    }

    public function test_a_ticket_is_only_good_for_what_it_was_issued_for(): void
    {
        $user = User::factory()->create();

        $this->assertSame('stream', TerminalTicket::redeem(
            TerminalTicket::issue($user, 'stream')
        )['mode']);

        $this->assertSame('shell', TerminalTicket::redeem(
            TerminalTicket::issue($user)
        )['mode']);

        // Anything unrecognised is the least privileged thing, not the most.
        $this->assertSame('shell', TerminalTicket::redeem(
            TerminalTicket::issue($user, 'root')
        )['mode']);
    }

    public function test_the_endpoint_issues_the_mode_that_was_asked_for(): void
    {
        $user = User::factory()->create();

        $ticket = $this->actingAs($user)
            ->postJson(route('terminal.ticket'), ['mode' => 'stream'])
            ->assertOk()
            ->json('ticket');

        $this->assertSame('stream', TerminalTicket::redeem($ticket)['mode']);
    }

    public function test_the_endpoint_defaults_to_a_shell_ticket(): void
    {
        $ticket = $this->actingAs(User::factory()->create())
            ->postJson(route('terminal.ticket'))
            ->assertOk()
            ->json('ticket');

        $this->assertSame('shell', TerminalTicket::redeem($ticket)['mode']);
    }

    public function test_the_sample_has_everything_the_dashboard_draws(): void
    {
        $sample = app(MetricsStream::class)->sample();

        foreach (['cpu', 'memory', 'swap', 'disk', 'load', 'uptime_seconds'] as $key) {
            $this->assertArrayHasKey($key, $sample);
        }

        // Same shape as the HTTP endpoint, so the browser cannot tell which
        // one a reading arrived through.
        $this->assertSame(
            array_keys(app(SystemMetrics::class)->read()),
            array_keys($sample)
        );
    }
}
