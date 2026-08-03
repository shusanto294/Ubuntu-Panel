<?php

namespace App\Services\System;

use App\Services\Shell\LocalConnection;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;
use Throwable;

/**
 * What version of the panel is installed, and whether a newer one is published.
 *
 * "Version" is two things: the release number in config, and the commit the
 * install is actually sitting on. The commit is what makes an update check
 * meaningful between releases — pushing to main is how updates ship.
 */
class UpdateChecker
{
    /** GitHub allows 60 unauthenticated calls an hour, so answers are cached. */
    protected const CACHE_KEY = 'panel:latest-version';

    protected const CACHE_MINUTES = 60;

    public function __construct(protected LocalConnection $shell) {}

    /**
     * @return array{version: string, commit: ?string, committed_at: ?string, repository: string}
     */
    public function current(): array
    {
        return [
            'version' => (string) config('panel.version'),
            'commit' => $this->localCommit(),
            'committed_at' => $this->localCommitDate(),
            'repository' => (string) config('panel.repository'),
        ];
    }

    /**
     * The newest published version: a release tag if the repository has any,
     * otherwise the tip of the default branch.
     *
     * @return array{version: ?string, commit: ?string, committed_at: ?string, url: ?string, error: ?string}
     */
    public function latest(bool $fresh = false): array
    {
        if ($fresh) {
            Cache::forget(self::CACHE_KEY);
        }

        return Cache::remember(self::CACHE_KEY, now()->addMinutes(self::CACHE_MINUTES), function () {
            try {
                return $this->fetchLatest();
            } catch (Throwable $e) {
                // Never let a network problem break the page that shows this.
                return [
                    'version' => null,
                    'commit' => null,
                    'committed_at' => null,
                    'url' => null,
                    'error' => $e->getMessage(),
                ];
            }
        });
    }

    /**
     * Everything a page needs to say "you are up to date" or otherwise.
     */
    public function status(bool $fresh = false): array
    {
        $current = $this->current();
        $latest = $this->latest($fresh);

        return [
            'current' => $current,
            'latest' => $latest,
            'available' => $this->isBehind($current, $latest),
            'checked_at' => now()->toIso8601String(),
        ];
    }

    /**
     * An update is only reported when we can actually tell — an unknown local
     * commit (installed from a tarball, say) reports "no update" rather than
     * nagging about one it cannot verify.
     */
    protected function isBehind(array $current, array $latest): bool
    {
        if (($latest['error'] ?? null) !== null) {
            return false;
        }

        if ($current['commit'] && $latest['commit']) {
            return ! str_starts_with($latest['commit'], $current['commit']);
        }

        if ($current['version'] && $latest['version']) {
            return version_compare(
                ltrim($latest['version'], 'v'),
                ltrim($current['version'], 'v'),
                '>'
            );
        }

        return false;
    }

    protected function fetchLatest(): array
    {
        $repository = $this->repositoryPath();
        $branch = (string) config('panel.update_branch', 'main');

        $release = Http::timeout(10)
            ->withHeaders(['Accept' => 'application/vnd.github+json'])
            ->get("https://api.github.com/repos/{$repository}/releases/latest");

        if ($release->successful() && filled($release->json('tag_name'))) {
            return [
                'version' => $release->json('tag_name'),
                'commit' => null,
                'committed_at' => $release->json('published_at'),
                'url' => $release->json('html_url'),
                'error' => null,
            ];
        }

        // No releases cut yet: the tip of the branch is the latest version.
        $commit = Http::timeout(10)
            ->withHeaders(['Accept' => 'application/vnd.github+json'])
            ->get("https://api.github.com/repos/{$repository}/commits/{$branch}");

        if (! $commit->successful()) {
            throw new \RuntimeException('GitHub answered '.$commit->status().'.');
        }

        return [
            'version' => null,
            'commit' => substr((string) $commit->json('sha'), 0, 7),
            'committed_at' => $commit->json('commit.committer.date'),
            'url' => $commit->json('html_url'),
            'error' => null,
        ];
    }

    /** owner/name, from the configured repository URL. */
    public function repositoryPath(): string
    {
        $url = (string) config('panel.repository');

        if (preg_match('#github\.com[:/]([^/]+/[^/.]+)#', $url, $matches)) {
            return $matches[1];
        }

        return 'shusanto294/Ubuntu-Panel';
    }

    protected function localCommit(): ?string
    {
        [$output, $code] = $this->shell->run('git -C '.escapeshellarg(base_path()).' rev-parse --short HEAD');

        return $code === 0 && $output !== '' ? trim($output) : null;
    }

    protected function localCommitDate(): ?string
    {
        [$output, $code] = $this->shell->run('git -C '.escapeshellarg(base_path()).' log -1 --format=%cI');

        return $code === 0 && $output !== '' ? trim($output) : null;
    }
}
