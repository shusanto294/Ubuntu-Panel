<?php

namespace App\Services\Dns\Providers;

use App\Services\Dns\DnsRecord;
use App\Services\Dns\HttpProvider;
use Illuminate\Http\Client\PendingRequest;

/**
 * Linode. Numeric zone ids, relative names with the empty string for the apex,
 * the value is `target`, and the TTL field is `ttl_sec`.
 */
class LinodeProvider extends HttpProvider
{
    protected string $base = 'https://api.linode.com/v4';

    public function __construct(protected string $token) {}

    public function verify(): string
    {
        $body = $this->request('get', '/profile');

        return 'authenticated as '.($body['username'] ?? 'this account');
    }

    public function zones(): array
    {
        $zones = [];
        $page = 1;

        do {
            $body = $this->request('get', '/domains', ['page' => $page, 'page_size' => 100]);

            foreach ($body['data'] ?? [] as $domain) {
                $zones[] = ['id' => (string) $domain['id'], 'name' => $domain['domain']];
            }

            $pages = (int) ($body['pages'] ?? 1);
            $page++;
        } while ($page <= $pages);

        return $zones;
    }

    public function records(string $zoneId, string $zoneName): array
    {
        $records = [];
        $page = 1;

        do {
            $body = $this->request('get', "/domains/{$zoneId}/records", ['page' => $page, 'page_size' => 100]);

            foreach ($body['data'] ?? [] as $record) {
                $records[] = [
                    'id' => (string) $record['id'],
                    'type' => strtoupper((string) $record['type']),
                    'name' => $this->absoluteName($record['name'] ?? '', $zoneName),
                    'content' => (string) ($record['target'] ?? ''),
                    'priority' => isset($record['priority']) ? (int) $record['priority'] : null,
                    'ttl' => isset($record['ttl_sec']) ? (int) $record['ttl_sec'] : null,
                    'proxied' => false,
                ];
            }

            $pages = (int) ($body['pages'] ?? 1);
            $page++;
        } while ($page <= $pages);

        return $records;
    }

    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $name = $record->relativeName($zoneName, '');
        $page = 1;

        do {
            $body = $this->request('get', "/domains/{$zoneId}/records", ['page' => $page, 'page_size' => 100]);

            foreach ($body['data'] ?? [] as $existing) {
                if (strtoupper($existing['type'] ?? '') === $record->type && ($existing['name'] ?? '') === $name) {
                    return (string) $existing['id'];
                }
            }

            $pages = (int) ($body['pages'] ?? 1);
            $page++;
        } while ($page <= $pages);

        return null;
    }

    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', "/domains/{$zoneId}/records", $this->payload($record, $zoneName));

        return isset($body['id']) ? (string) $body['id'] : null;
    }

    public function update(string $zoneId, string $zoneName, string $recordId, DnsRecord $record): ?string
    {
        $this->request('put', "/domains/{$zoneId}/records/{$recordId}", $this->payload($record, $zoneName));

        return $recordId;
    }

    public function delete(string $zoneId, string $zoneName, string $recordId): void
    {
        $this->request('delete', "/domains/{$zoneId}/records/{$recordId}");
    }

    /**
     * @return array<string, mixed>
     */
    protected function payload(DnsRecord $record, string $zoneName): array
    {
        return array_filter([
            'type' => $record->type,
            'name' => $record->relativeName($zoneName, ''),
            'target' => $record->content,
            'priority' => $record->priority,
            'ttl_sec' => $record->ttlOr(300),
        ], fn ($value) => $value !== null);
    }

    protected function http(): PendingRequest
    {
        return $this->json()->withToken($this->token);
    }
}
