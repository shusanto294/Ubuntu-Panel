<?php

namespace App\Services\Dns;

use App\Models\ActivityLog;
use App\Models\DnsAccount;
use App\Models\Site;
use App\Services\System\HostInfo;
use Illuminate\Support\Str;
use RuntimeException;
use Throwable;

/**
 * Keeps DNS in step with the panel, whoever is hosting it.
 *
 * Everything provider-specific lives behind {@see DnsProvider}; this decides
 * what records ought to exist and records what was written, so a site can be
 * torn down again without guessing.
 */
class DnsManager
{
    public function __construct(protected HostInfo $host) {}

    /**
     * Create or refresh the records for every hostname on the site.
     */
    public function syncForSite(Site $site): string
    {
        $account = $site->dnsAccount;

        if (! $account) {
            throw new RuntimeException('Site is set to manage DNS but has no DNS credential attached.');
        }

        $provider = $account->driver();
        $content = $site->dns_content ?: $this->host->publicIp();
        $lines = [];
        $recordIds = $site->dns_record_ids ?? [];

        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'dns',
            'action' => 'dns.sync',
            'status' => 'running',
        ]);

        try {
            foreach ($site->hostnames() as $hostname) {
                $zone = $this->resolveZone($provider, $account, $hostname);

                $record = new DnsRecord(
                    type: $site->dns_type,
                    name: $hostname,
                    content: (string) $content,
                    // Only Cloudflare can proxy; asking anyone else to would be
                    // an unknown field at best.
                    proxied: $site->dns_proxied && $provider->supportsProxy(),
                );

                $existing = $provider->findRecordId($zone['id'], $zone['name'], $record);

                $id = $existing
                    ? $provider->update($zone['id'], $zone['name'], $existing, $record)
                    : $provider->create($zone['id'], $zone['name'], $record);

                $lines[] = sprintf(
                    'DNS %s: %s %s -> %s',
                    $existing ? 'updated' : 'created',
                    $site->dns_type,
                    $hostname,
                    $content
                );

                $recordIds[$hostname] = [
                    'zone_id' => $zone['id'],
                    'zone_name' => $zone['name'],
                    'record_id' => $id ?? $existing,
                ];

                $site->dns_zone_id ??= $zone['id'];
            }

            $site->forceFill([
                'dns_record_ids' => $recordIds,
                'dns_zone_id' => $site->dns_zone_id,
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
     * Never throws — a dead DNS record must not block deleting a site.
     */
    public function deleteForSite(Site $site): string
    {
        $account = $site->dnsAccount;
        $records = $site->dns_record_ids ?? [];

        if (! $account || $records === []) {
            return 'No DNS records tracked for this site.';
        }

        $log = ActivityLog::record([
            'user_id' => $site->user_id,
            'site_id' => $site->id,
            'type' => 'dns',
            'action' => 'dns.delete',
            'status' => 'running',
        ]);

        $message = $this->deleteRecords($account, $records, fallbackZone: $site->dns_zone_id);

        $site->forceFill(['dns_record_ids' => []])->save();

        $log->update(['status' => 'success', 'message' => 'DNS cleanup finished.', 'output' => $message]);

        return $message;
    }

    /**
     * Write an arbitrary set of records into whichever zone owns $anchor.
     *
     * @param  array<int, array<string, mixed>>  $records
     * @return array{ids: array<string, array{zone_id: string, zone_name: string, record_id: ?string}>, log: string, zone_id: string}
     */
    public function writeRecords(DnsAccount $account, string $anchor, array $records): array
    {
        $provider = $account->driver();
        $zone = $provider->findZone($anchor);

        if (! $zone) {
            throw new RuntimeException(
                "No zone for {$anchor} at ".$account->providerLabel().'. Add the domain there first.'
            );
        }

        $ids = [];
        $lines = [];

        foreach ($records as $record) {
            $record = DnsRecord::fromArray($record);

            if ($record->proxied && ! $provider->supportsProxy()) {
                $record = new DnsRecord($record->type, $record->name, $record->content, $record->priority, $record->ttl);
            }

            $existing = $provider->findRecordId($zone['id'], $zone['name'], $record);

            $id = $existing
                ? $provider->update($zone['id'], $zone['name'], $existing, $record)
                : $provider->create($zone['id'], $zone['name'], $record);

            $ids[$record->key()] = [
                'zone_id' => $zone['id'],
                'zone_name' => $zone['name'],
                'record_id' => $id ?? $existing,
            ];

            $lines[] = sprintf(
                '%s %s %s %s',
                $existing ? 'updated' : 'created',
                $record->type,
                $record->name,
                Str::limit($record->content, 60)
            );
        }

        return ['ids' => $ids, 'log' => implode("\n", $lines), 'zone_id' => $zone['id']];
    }

    /**
     * Delete a previously written record set. Never throws.
     *
     * @param  array<string, array{zone_id?: string, zone_name?: string, record_id?: ?string}>  $records
     */
    public function deleteRecords(DnsAccount $account, array $records, ?string $fallbackZone = null): string
    {
        $provider = $account->driver();
        $lines = [];

        foreach ($records as $key => $meta) {
            $zoneId = $meta['zone_id'] ?? $fallbackZone;
            $recordId = $meta['record_id'] ?? null;

            if (! $zoneId || ! $recordId) {
                continue;
            }

            try {
                // No provider here needs the zone's name to delete by id — the
                // ones that key on the name store it as the id too — so a
                // record written before names were tracked deletes fine.
                $provider->delete($zoneId, $meta['zone_name'] ?? $zoneId, $recordId);
                $lines[] = "DNS deleted: {$key}";
            } catch (Throwable $e) {
                $lines[] = "DNS delete failed for {$key}: ".$e->getMessage();
            }
        }

        return implode("\n", $lines) ?: 'Nothing to delete.';
    }

    /**
     * @return array{id: string, name: string}
     */
    protected function resolveZone(DnsProvider $provider, DnsAccount $account, string $hostname): array
    {
        $zone = $provider->findZone($hostname);

        if (! $zone) {
            throw new RuntimeException(
                "No zone for {$hostname} at ".$account->providerLabel().'. Add the domain there first.'
            );
        }

        return $zone;
    }
}
