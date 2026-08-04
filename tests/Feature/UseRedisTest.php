<?php

namespace Tests\Feature;

use App\Support\Settings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

/**
 * Moving the stores is cheap to get right and expensive to get wrong: a panel
 * that comes back up pointing at a Redis it cannot reach is worse than one
 * merely using more CPU than it needs to. So the command declines rather than
 * guesses, and these are the reasons it declines.
 */
class UseRedisTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();

        // Nothing here may touch the real .env.
        $this->assertStringContainsString('testing', app()->environment());
    }

    public function test_it_declines_when_no_redis_password_has_been_recorded(): void
    {
        app(Settings::class)->forget('redis_password');

        $this->artisan('panel:use-redis')
            ->expectsOutputToContain('Staying on the database')
            ->assertSuccessful();

        $this->assertNotSame('redis', config('queue.default'));
    }

    public function test_it_declines_while_jobs_are_still_queued(): void
    {
        app(Settings::class)->set('redis_password', 'a-password', secret: true);

        DB::table('jobs')->insert([
            'queue' => 'default',
            'payload' => '{}',
            'attempts' => 0,
            'available_at' => time(),
            'created_at' => time(),
        ]);

        $this->artisan('panel:use-redis')
            ->expectsOutputToContain('Staying on the database')
            ->assertSuccessful();
    }

    /**
     * Jobs live in whichever store they were dispatched to, so switching with
     * work outstanding strands it in a table nothing reads any more.
     */
    public function test_the_queued_job_check_names_the_count(): void
    {
        app(Settings::class)->set('redis_password', 'a-password', secret: true);

        foreach (range(1, 3) as $i) {
            DB::table('jobs')->insert([
                'queue' => 'default',
                'payload' => '{}',
                'attempts' => 0,
                'available_at' => time(),
                'created_at' => time(),
            ]);
        }

        $this->artisan('panel:use-redis')
            ->expectsOutputToContain('3 job(s) are still queued')
            ->assertSuccessful();
    }

    public function test_it_says_nothing_to_do_when_already_on_redis(): void
    {
        config([
            'queue.default' => 'redis',
            'session.driver' => 'redis',
            'cache.default' => 'redis',
        ]);

        $this->artisan('panel:use-redis')
            ->expectsOutputToContain('Already using Redis.')
            ->assertSuccessful();
    }

    /** A failed switch must leave the panel exactly as it found it. */
    public function test_declining_changes_nothing(): void
    {
        app(Settings::class)->forget('redis_password');

        $before = [config('queue.default'), config('session.driver'), config('cache.default')];

        $this->artisan('panel:use-redis')->assertSuccessful();

        $this->assertSame(
            $before,
            [config('queue.default'), config('session.driver'), config('cache.default')]
        );
    }
}
