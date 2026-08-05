<?php

namespace App\Services\Dns\Providers;

use App\Services\Dns\DnsRecord;
use App\Services\Dns\HttpProvider;
use Illuminate\Http\Client\PendingRequest;

/**
 * DigitalOcean. Zones are keyed by domain name rather than an id, record names
 * are relative with `@` for the apex, and the value is called `data`.
 */
class DigitalOceanProvider extends HttpProvider
{
    protected string $base = 'https://api.digitalocean.com/v2';

    public function __construct(protected string $token) {}

    public function verify(): string
    {
        $body = $this->request('get', '/account');

        return 'authenticated as '.($body['account']['email'] ?? 'this account');
    }

    public function zones(): array
    {
        $zones = [];
        $page = 1;

        do {
            $body = $this->request('get', '/domains', ['per_page' => 100, 'page' => $page]);

            foreach ($body['domains'] ?? [] as $domain) {
                // The name is the id here; there is nothing else to key on.
                $zones[] = ['id' => $domain['name'], 'name' => $domain['name']];
            }

            $more = isset($body['links']['pages']['next']);
            $page++;
        } while ($more);

        return $zones;
    }

    public function records(string $zoneId, string $zoneName): array
    {
        $records = [];
        $page = 1;

        do {
            $body = $this->request('get', "/domains/{$zoneId}/records", ['per_page' => 100, 'page' => $page]);

            foreach ($body['domain_records'] ?? [] as $record) {
                $records[] = [
                    'id' => (string) $record['id'],
                    'type' => strtoupper((string) $record['type']),
                    'name' => $this->absoluteName($record['name'] ?? '@', $zoneName),
                    'content' => (string) ($record['data'] ?? ''),
                    'priority' => isset($record['priority']) ? (int) $record['priority'] : null,
                    'ttl' => isset($record['ttl']) ? (int) $record['ttl'] : null,
                    'proxied' => false,
                ];
            }

            $page++;
        } while (! empty($body['links']['pages']['next'] ?? null));

        return $records;
    }

    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('get', "/domains/{$zoneId}/records", [
            'name' => $record->name,
            'type' => $record->type,
            'per_page' => 100,
        ]);

        foreach ($body['domain_records'] ?? [] as $existing) {
            if (strtoupper($existing['type'] ?? '') === $record->type) {
                return (string) $existing['id'];
            }
        }

        return null;
    }

    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', "/domains/{$zoneId}/records", $this->payload($record, $zoneName));

        return isset($body['domain_record']['id']) ? (string) $body['domain_record']['id'] : null;
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
            'name' => $record->relativeName($zoneName, '@'),
            'data' => $record->content,
            'priority' => $record->priority,
            'ttl' => $record->ttlOr(1800),
        ], fn ($value) => $value !== null);
    }

    protected function http(): PendingRequest
    {
        return $this->json()->withToken($this->token);
    }
}
