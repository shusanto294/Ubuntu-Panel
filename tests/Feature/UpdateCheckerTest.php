<?php

namespace Tests\Feature;

use App\Services\System\UpdateChecker;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class UpdateCheckerTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();

        Cache::flush();
        config(['panel.repository' => 'https://github.com/shusanto294/Ubuntu-Panel']);
    }

    protected function fakeGitHub(?string $tag, ?string $sha): void
    {
        Http::fake([
            'api.github.com/repos/*/releases/latest' => $tag
                ? Http::response(['tag_name' => $tag, 'published_at' => '2026-01-01T00:00:00Z', 'html_url' => 'https://example.test/r'])
                : Http::response(['message' => 'Not Found'], 404),
            'api.github.com/repos/*/commits/*' => $sha
                ? Http::response(['sha' => $sha, 'html_url' => 'https://example.test/c', 'commit' => ['committer' => ['date' => '2026-01-01T00:00:00Z']]])
                : Http::response('', 500),
        ]);
    }

    public function test_the_repository_path_comes_from_the_configured_url(): void
    {
        $this->assertSame('shusanto294/Ubuntu-Panel', app(UpdateChecker::class)->repositoryPath());
    }

    public function test_a_release_tag_is_preferred_over_the_branch_tip(): void
    {
        $this->fakeGitHub(tag: 'v9.9.9', sha: 'abc1234');

        $latest = app(UpdateChecker::class)->latest(fresh: true);

        $this->assertSame('v9.9.9', $latest['version']);
    }

    public function test_a_newer_release_is_reported_as_available(): void
    {
        config(['panel.version' => '0.1.0']);
        $this->fakeGitHub(tag: 'v0.2.0', sha: null);

        $status = app(UpdateChecker::class)->status(fresh: true);

        // No local commit in the test environment, so the version is compared.
        $this->assertTrue($status['available']);
    }

    public function test_an_older_release_is_not_reported_as_available(): void
    {
        config(['panel.version' => '2.0.0']);
        $this->fakeGitHub(tag: 'v1.0.0', sha: null);

        $this->assertFalse(app(UpdateChecker::class)->status(fresh: true)['available']);
    }

    public function test_github_being_unreachable_never_claims_an_update(): void
    {
        Http::fake(fn () => Http::response('', 503));

        $status = app(UpdateChecker::class)->status(fresh: true);

        $this->assertFalse($status['available']);
        $this->assertNotNull($status['latest']['error']);
    }

    public function test_the_answer_is_cached_so_the_page_never_waits_on_github(): void
    {
        $this->fakeGitHub(tag: 'v1.2.3', sha: null);

        $checker = app(UpdateChecker::class);
        $checker->latest(fresh: true);
        $checker->latest();
        $checker->latest();

        // One release call plus its 404 fallback path, not one per read.
        Http::assertSentCount(1);
    }

    public function test_the_endpoint_reports_the_installed_version(): void
    {
        config(['panel.version' => '0.1.0']);
        $this->fakeGitHub(tag: 'v0.1.0', sha: null);

        $this->actingAs(\App\Models\User::factory()->create())
            ->getJson(route('system.updates'))
            ->assertOk()
            ->assertJsonPath('current.version', '0.1.0')
            ->assertJsonPath('available', false);
    }
}
