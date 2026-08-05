<?php

namespace App\Services\Dns\Providers;

use App\Services\Dns\DnsRecord;
use App\Services\Dns\HttpProvider;
use Illuminate\Http\Client\PendingRequest;
use RuntimeException;

/**
 * Porkbun. Everything is a POST, the credentials travel in the body rather
 * than a header, and it needs two of them — the API key and its secret.
 * Priority is `prio`, and record names come back fully qualified but must be
 * sent relative.
 */
class PorkbunProvider extends HttpProvider
{
    protected string $base = 'https://api.porkbun.com/api/json/v3';

    public function __construct(protected string $key, protected string $secret) {}

    public function verify(): string
    {
        $body = $this->request('post', '/ping');

        return 'authenticated from '.($body['yourIp'] ?? 'this server');
    }

    public function zones(): array
    {
        $body = $this->request('post', '/domain/listAll');

        return array_map(
            fn (array $domain) => ['id' => $domain['domain'], 'name' => $domain['domain']],
            $body['domains'] ?? []
        );
    }

    public function records(string $zoneId, string $zoneName): array
    {
        $body = $this->request('post', "/dns/retrieve/{$zoneId}");

        $records = [];

        foreach ($body['records'] ?? [] as $record) {
            $records[] = [
                'id' => (string) $record['id'],
                'type' => strtoupper((string) $record['type']),
                'name' => $this->absoluteName($record['name'] ?? '', $zoneName),
                'content' => (string) ($record['content'] ?? ''),
                'priority' => isset($record['prio']) && $record['prio'] !== '' && $record['prio'] !== '0'
                    ? (int) $record['prio']
                    : null,
                'ttl' => isset($record['ttl']) ? (int) $record['ttl'] : null,
                'proxied' => false,
            ];
        }

        return $records;
    }

    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', "/dns/retrieve/{$zoneId}");

        foreach ($body['records'] ?? [] as $existing) {
            $name = rtrim($existing['name'] ?? '', '.');

            if (strtoupper($existing['type'] ?? '') === $record->type && $name === $record->name) {
                return (string) $existing['id'];
            }
        }

        return null;
    }

    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', "/dns/create/{$zoneId}", $this->payload($record, $zoneName));

        return isset($body['id']) ? (string) $body['id'] : null;
    }

    public function update(string $zoneId, string $zoneName, string $recordId, DnsRecord $record): ?string
    {
        $this->request('post', "/dns/edit/{$zoneId}/{$recordId}", $this->payload($record, $zoneName));

        return $recordId;
    }

    public function delete(string $zoneId, string $zoneName, string $recordId): void
    {
        $this->request('post', "/dns/delete/{$zoneId}/{$recordId}");
    }

    /**
     * @return array<string, mixed>
     */
    protected function payload(DnsRecord $record, string $zoneName): array
    {
        return array_filter([
            'type' => $record->type,
            'name' => $record->relativeName($zoneName, ''),
            'content' => $record->content,
            'prio' => $record->priority,
            'ttl' => $record->ttlOr(600),
        ], fn ($value) => $value !== null && $value !== '');
    }

    /** Credentials ride in the body of every call, so they go in here. */
    protected function request(string $method, string $path, array $data = []): array
    {
        $body = parent::request('post', $path, $data + [
            'apikey' => $this->key,
            'secretapikey' => $this->secret,
        ]);

        if (($body['status'] ?? 'SUCCESS') !== 'SUCCESS') {
            throw new RuntimeException('Porkbun: '.($body['message'] ?? 'request rejected'));
        }

        return $body;
    }

    protected function http(): PendingRequest
    {
        return $this->json()->asJson();
    }
}
