<?php

namespace Tests\Feature;

use App\Models\MetricSample;
use App\Models\User;
use App\Services\System\MetricHistory;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class MetricHistoryTest extends TestCase
{
    use RefreshDatabase;

    protected function sample(int $minutesAgo, float $cpu, float $memory = 40, float $disk = 50): MetricSample
    {
        return MetricSample::create([
            'cpu_percent' => $cpu,
            'memory_percent' => $memory,
            'disk_percent' => $disk,
            'memory_used' => 4_000_000_000,
            'memory_total' => 8_000_000_000,
            'disk_used' => 50_000_000_000,
            'disk_total' => 100_000_000_000,
            'sampled_at' => now()->subMinutes($minutesAgo),
        ]);
    }

    public function test_an_hour_comes_back_a_point_a_minute(): void
    {
        $this->sample(1, 10);
        $this->sample(2, 20);
        $this->sample(3, 30);

        $series = app(MetricHistory::class)->series('1h');

        $this->assertSame('1h', $series['range']);
        $this->assertSame(60, $series['bucket_seconds']);
        $this->assertCount(3, $series['points']);

        // Oldest first.
        $this->assertSame(30.0, $series['points'][0]['cpu']);
        $this->assertSame(10.0, $series['points'][2]['cpu']);
    }

    public function test_a_wider_range_averages_the_samples_in_each_bucket(): void
    {
        // Buckets are wall-clock aligned, so "one and two minutes ago" only
        // share one if the clock is not near a boundary. Pin it to the middle
        // of a fifteen-minute bucket instead of running the suite and hoping.
        $this->travelTo(now()->startOfHour()->addMinutes(37)->addSeconds(30));

        // Two readings inside the same fifteen-minute bucket.
        $this->sample(1, 10);
        $this->sample(2, 30);

        $series = app(MetricHistory::class)->series('24h');

        $this->assertSame(900, $series['bucket_seconds']);
        $this->assertCount(1, $series['points']);
        $this->assertSame(20.0, $series['points'][0]['cpu']);
    }

    public function test_samples_outside_the_window_are_left_out(): void
    {
        $this->sample(10, 10);
        $this->sample(200, 90);

        $series = app(MetricHistory::class)->series('1h');

        $this->assertCount(1, $series['points']);
        $this->assertSame(10.0, $series['points'][0]['cpu']);
    }

    public function test_a_missing_cpu_reading_does_not_drag_the_average_down(): void
    {
        // The first sample after a restart has no CPU delta to work from.
        MetricSample::create([
            'cpu_percent' => null,
            'memory_percent' => 40,
            'disk_percent' => 50,
            'sampled_at' => now()->subMinutes(2),
        ]);
        $this->sample(3, 60);

        $series = app(MetricHistory::class)->series('24h');

        // Averaged over the one row that had a reading, not both.
        $this->assertSame(60.0, $series['points'][0]['cpu']);
        $this->assertSame(40.0, $series['points'][0]['memory']);
    }

    public function test_an_unknown_range_falls_back_to_the_default(): void
    {
        $this->assertSame(
            MetricHistory::DEFAULT_RANGE,
            app(MetricHistory::class)->series('all-time')['range'],
        );
    }

    public function test_pruning_drops_only_what_is_past_the_retention_window(): void
    {
        $this->sample(60, 10);
        $this->sample((MetricHistory::RETAIN_DAYS + 1) * 24 * 60, 90);

        $this->assertSame(1, app(MetricHistory::class)->prune());
        $this->assertSame(1, MetricSample::count());
    }

    public function test_the_endpoint_serves_the_requested_range(): void
    {
        $this->sample(1, 10);

        $this->actingAs(User::factory()->create())
            ->getJson(route('system.metrics.history', ['range' => '7d']))
            ->assertOk()
            ->assertJsonPath('history.range', '7d')
            ->assertJsonPath('history.bucket_seconds', 3600)
            ->assertJsonCount(1, 'history.points');
    }

    public function test_guests_cannot_read_the_history(): void
    {
        $this->getJson(route('system.metrics.history'))->assertUnauthorized();
    }

    public function test_the_dashboard_ships_the_first_range_with_the_page(): void
    {
        $this->sample(1, 10);

        $this->actingAs(User::factory()->create())
            ->get(route('dashboard'))
            ->assertOk()
            ->assertInertia(fn ($page) => $page
                ->component('System/Overview')
                ->where('history.range', MetricHistory::DEFAULT_RANGE)
                ->has('history.points', 1)
                ->has('historyRanges', count(MetricHistory::RANGES))
                ->where('historyRanges.0.key', '1h')
            );
    }

    public function test_the_collector_records_a_sample(): void
    {
        $this->artisan('panel:collect-metrics')->assertSuccessful();

        $this->assertSame(1, MetricSample::count());
    }
}
