<?php

namespace App\Services\Dns\Providers;

use App\Services\Dns\DnsRecord;
use App\Services\Dns\HttpProvider;
use Illuminate\Http\Client\PendingRequest;

/**
 * Vultr. Zones keyed by domain, relative names with the empty string for the
 * apex, value is `data`, and updates are PATCH rather than PUT.
 */
class VultrProvider extends HttpProvider
{
    protected string $base = 'https://api.vultr.com/v2';

    public function __construct(protected string $token) {}

    public function verify(): string
    {
        $body = $this->request('get', '/account');

        return 'authenticated as '.($body['account']['email'] ?? 'this account');
    }

    public function zones(): array
    {
        $zones = [];
        $cursor = null;

        do {
            $body = $this->request('get', '/domains', array_filter([
                'per_page' => 100,
                'cursor' => $cursor,
            ]));

            foreach ($body['domains'] ?? [] as $domain) {
                $zones[] = ['id' => $domain['domain'], 'name' => $domain['domain']];
            }

            $cursor = $body['meta']['links']['next'] ?? null;
        } while ($cursor);

        return $zones;
    }

    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $name = $record->relativeName($zoneName, '');
        $cursor = null;

        do {
            $body = $this->request('get', "/domains/{$zoneId}/records", array_filter([
                'per_page' => 100,
                'cursor' => $cursor,
            ]));

            foreach ($body['records'] ?? [] as $existing) {
                if (strtoupper($existing['type'] ?? '') === $record->type && ($existing['name'] ?? '') === $name) {
                    return (string) $existing['id'];
                }
            }

            $cursor = $body['meta']['links']['next'] ?? null;
        } while ($cursor);

        return null;
    }

    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', "/domains/{$zoneId}/records", $this->payload($record, $zoneName));

        return isset($body['record']['id']) ? (string) $body['record']['id'] : null;
    }

    public function update(string $zoneId, string $zoneName, string $recordId, DnsRecord $record): ?string
    {
        $this->request('patch', "/domains/{$zoneId}/records/{$recordId}", $this->payload($record, $zoneName));

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
            'data' => $record->content,
            'priority' => $record->priority,
            'ttl' => $record->ttlOr(300),
        ], fn ($value) => $value !== null);
    }

    protected function http(): PendingRequest
    {
        return $this->json()->withToken($this->token);
    }
}
