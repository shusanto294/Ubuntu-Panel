<?php

namespace App\Services\Cloudflare;

use App\Models\ActivityLog;
use App\Models\CloudflareAccount;
use App\Models\Site;
use App\Services\System\HostInfo;
use Illuminate\Support\Str;
use RuntimeException;
use Throwable;

/**
 * Keeps a site's Cloudflare DNS records in sync with the panel.
 */
class CloudflareDnsManager
{
    public function __construct(protected HostInfo $host) {}

    /**
     * Create/refresh the records for every hostname on the site.
     * Returns a human readable log line block.
     */
    public function syncForSite(Site $site): string
    {
        $account = $site->cloudflareAccount;

        if (! $account) {
            throw new RuntimeException('Site is set to manage DNS but has no Cloudflare account attached.');
        }

        $client = CloudflareClient::for($account);
        $content = $site->dns_content ?: $this->host->publicIp();
        $lines = [];
        $recordIds = $site->cloudflare_record_ids ?? [];

        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'dns',
            'action' => 'dns.sync',
            'status' => 'running',
        ]);

        try {
            foreach ($site->hostnames() as $hostname) {
                $zone = $this->resolveZone($client, $site, $hostname);

                $payload = [
                    'type' => $site->dns_type,
                    'name' => $hostname,
                    'content' => $content,
                    'ttl' => 1,
                    'proxied' => $site->dns_proxied,
                ];

                $existing = collect($client->dnsRecords($zone['id'], ['name' => $hostname, 'type' => $site->dns_type]))->first();

                if ($existing) {
                    $record = $client->updateDnsRecord($zone['id'], $existing['id'], $payload);
                    $lines[] = "DNS updated: {$site->dns_type} {$hostname} -> {$content}";
                } else {
                    $record = $client->createDnsRecord($zone['id'], $payload);
                    $lines[] = "DNS created: {$site->dns_type} {$hostname} -> {$content}";
                }

                $recordIds[$hostname] = ['zone_id' => $zone['id'], 'record_id' => $record['id'] ?? null];

                if (! $site->cloudflare_zone_id) {
                    $site->cloudflare_zone_id = $zone['id'];
                }
            }

            $site->forceFill([
                'cloudflare_record_ids' => $recordIds,
                'cloudflare_zone_id' => $site->cloudflare_zone_id,
                'dns_content' => $content,
            ])->save();

            $message = implode("\n", $lines);
            $log->update(['status' => 'success', 'message' => 'DNS synced.', 'output' => $message]);

            return $message;
        } catch (Throwable $e) {
            $log->update(['status' => 'failed', 'message' => $e->getMessage(), 'output' => implode("\n", $lines)]);
            throw $e;
        }
    }

    /**
     * Delete every record the panel created for this site.
     * Never throws — a dead DNS record must not block site deletion.
     */
    public function deleteForSite(Site $site): string
    {
        $account = $site->cloudflareAccount;
        $records = $site->cloudflare_record_ids ?? [];

        if (! $account || $records === []) {
            return 'No Cloudflare records tracked for this site.';
        }

        $client = CloudflareClient::for($account);
        $lines = [];

        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'dns',
            'action' => 'dns.delete',
            'status' => 'running',
        ]);

        foreach ($records as $hostname => $meta) {
            $zoneId = $meta['zone_id'] ?? $site->cloudflare_zone_id;
            $recordId = $meta['record_id'] ?? null;

            if (! $zoneId || ! $recordId) {
                continue;
            }

            try {
                $client->deleteDnsRecord($zoneId, $recordId);
                $lines[] = "DNS deleted: {$hostname}";
            } catch (Throwable $e) {
                $lines[] = "DNS delete failed for {$hostname}: ".$e->getMessage();
            }
        }

        $site->forceFill(['cloudflare_record_ids' => []])->save();

        $message = implode("\n", $lines) ?: 'Nothing to delete.';
        $log->update(['status' => 'success', 'message' => 'DNS cleanup finished.', 'output' => $message]);

        return $message;
    }

    /**
     * Create or update an arbitrary set of records in the zone that owns $anchor.
     *
     * @param  array<int, array{type: string, name: string, content: string, priority?: int, proxied?: bool, ttl?: int}>  $records
     * @return array{ids: array<string, array{zone_id: string, record_id: ?string}>, log: string, zone_id: string}
     */
    public function writeRecords(CloudflareAccount $account, string $anchor, array $records): array
    {
        $client = CloudflareClient::for($account);
        $zone = $client->findZoneForHostname($anchor);

        if (! $zone) {
            throw new RuntimeException("No Cloudflare zone found for {$anchor}.");
        }

        $ids = [];
        $lines = [];

        foreach ($records as $record) {
            $payload = array_filter([
                'type' => $record['type'],
                'name' => $record['name'],
                'content' => $record['content'],
                'ttl' => $record['ttl'] ?? 1,
                'priority' => $record['priority'] ?? null,
                'proxied' => $record['proxied'] ?? false,
            ], fn ($value) => $value !== null);

            $existing = collect($client->dnsRecords($zone['id'], [
                'name' => $record['name'],
                'type' => $record['type'],
            ]))->first();

            $result = $existing
                ? $client->updateDnsRecord($zone['id'], $existing['id'], $payload)
                : $client->createDnsRecord($zone['id'], $payload);

            $key = $record['type'].':'.$record['name'];
            $ids[$key] = ['zone_id' => $zone['id'], 'record_id' => $result['id'] ?? null];
            $lines[] = sprintf('%s %s %s %s', $existing ? 'updated' : 'created', $record['type'], $record['name'], Str::limit($record['content'], 60));
        }

        return ['ids' => $ids, 'log' => implode("\n", $lines), 'zone_id' => $zone['id']];
    }

    /**
     * Delete a previously written record set. Never throws.
     *
     * @param  array<string, array{zone_id: string, record_id: ?string}>  $records
     */
    public function deleteRecords(CloudflareAccount $account, array $records): string
    {
        $client = CloudflareClient::for($account);
        $lines = [];

        foreach ($records as $key => $meta) {
            if (empty($meta['zone_id']) || empty($meta['record_id'])) {
                continue;
            }

            try {
                $client->deleteDnsRecord($meta['zone_id'], $meta['record_id']);
                $lines[] = "deleted {$key}";
            } catch (Throwable $e) {
                $lines[] = "delete failed for {$key}: ".$e->getMessage();
            }
        }

        return implode("\n", $lines) ?: 'Nothing to delete.';
    }

    protected function resolveZone(CloudflareClient $client, Site $site, string $hostname): array
    {
        if ($site->cloudflare_zone_id && str_ends_with($hostname, $site->domain)) {
            return ['id' => $site->cloudflare_zone_id];
        }

        $zone = $client->findZoneForHostname($hostname);

        if (! $zone) {
            throw new RuntimeException("No Cloudflare zone found for {$hostname}. Add the domain to Cloudflare first.");
        }

        return $zone;
    }
}
