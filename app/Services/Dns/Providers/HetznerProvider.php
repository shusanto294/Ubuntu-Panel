<?php

namespace App\Services\Dns\Providers;

use App\Services\Dns\DnsRecord;
use App\Services\Dns\HttpProvider;
use Illuminate\Http\Client\PendingRequest;

/**
 * Hetzner DNS. Its own header rather than a bearer token, relative names with
 * `@` for the apex, and no priority field at all — MX priority goes on the
 * front of the value, the way it appears in a zone file.
 */
class HetznerProvider extends HttpProvider
{
    protected string $base = 'https://dns.hetzner.com/api/v1';

    public function __construct(protected string $token) {}

    public function verify(): string
    {
        // No account endpoint; the cheapest authenticated call is one zone.
        $body = $this->request('get', '/zones', ['per_page' => 1]);

        return ($body['meta']['pagination']['total_entries'] ?? count($body['zones'] ?? [])).' zone(s) visible';
    }

    public function zones(): array
    {
        $zones = [];
        $page = 1;

        do {
            $body = $this->request('get', '/zones', ['page' => $page, 'per_page' => 100]);

            foreach ($body['zones'] ?? [] as $zone) {
                $zones[] = ['id' => (string) $zone['id'], 'name' => $zone['name']];
            }

            $pages = (int) ($body['meta']['pagination']['last_page'] ?? 1);
            $page++;
        } while ($page <= $pages);

        return $zones;
    }

    public function records(string $zoneId, string $zoneName): array
    {
        $body = $this->request('get', '/records', ['zone_id' => $zoneId, 'per_page' => 100]);

        $records = [];

        foreach ($body['records'] ?? [] as $record) {
            $type = strtoupper((string) $record['type']);
            $value = (string) ($record['value'] ?? '');
            $priority = null;

            // No priority column: "10 mail.example.com" is how Hetzner keeps
            // an MX, so it comes apart here the same way it goes together on
            // the way in.
            if (in_array($type, ['MX', 'SRV'], true) && preg_match('/^(\d+)\s+(.*)$/', $value, $m)) {
                $priority = (int) $m[1];
                $value = $m[2];
            }

            $records[] = [
                'id' => (string) $record['id'],
                'type' => $type,
                'name' => $this->absoluteName($record['name'] ?? '@', $zoneName),
                'content' => $value,
                'priority' => $priority,
                'ttl' => isset($record['ttl']) ? (int) $record['ttl'] : null,
                'proxied' => false,
            ];
        }

        return $records;
    }

    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $name = $record->relativeName($zoneName, '@');

        $body = $this->request('get', '/records', ['zone_id' => $zoneId, 'per_page' => 100]);

        foreach ($body['records'] ?? [] as $existing) {
            if (strtoupper($existing['type'] ?? '') === $record->type && ($existing['name'] ?? '') === $name) {
                return (string) $existing['id'];
            }
        }

        return null;
    }

    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', '/records', $this->payload($record, $zoneName) + ['zone_id' => $zoneId]);

        return isset($body['record']['id']) ? (string) $body['record']['id'] : null;
    }

    public function update(string $zoneId, string $zoneName, string $recordId, DnsRecord $record): ?string
    {
        $this->request('put', "/records/{$recordId}", $this->payload($record, $zoneName) + ['zone_id' => $zoneId]);

        return $recordId;
    }

    public function delete(string $zoneId, string $zoneName, string $recordId): void
    {
        $this->request('delete', "/records/{$recordId}");
    }

    /**
     * @return array<string, mixed>
     */
    protected function payload(DnsRecord $record, string $zoneName): array
    {
        $value = $record->content;

        // No priority column: "10 mail.example.com" is how Hetzner takes an MX.
        if ($record->priority !== null) {
            $value = $record->priority.' '.$value;
        }

        return [
            'type' => $record->type,
            'name' => $record->relativeName($zoneName, '@'),
            'value' => $value,
            'ttl' => $record->ttlOr(300),
        ];
    }

    protected function http(): PendingRequest
    {
        return $this->json()->withHeaders(['Auth-API-Token' => $this->token]);
    }
}
