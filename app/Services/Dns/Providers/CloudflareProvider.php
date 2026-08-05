<?php

namespace App\Services\Dns\Providers;

use App\Services\Dns\DnsRecord;
use App\Services\Dns\HttpProvider;
use Illuminate\Http\Client\PendingRequest;
use RuntimeException;

/**
 * Cloudflare API v4. Absolute record names, `content`, real priority field,
 * and the only provider here that can also proxy traffic.
 */
class CloudflareProvider extends HttpProvider
{
    protected string $base = 'https://api.cloudflare.com/client/v4';

    public function __construct(protected string $token) {}

    public function supportsProxy(): bool
    {
        return true;
    }

    public function verify(): string
    {
        $body = $this->request('get', '/user/tokens/verify');

        $status = $body['result']['status'] ?? 'unknown';

        if ($status !== 'active') {
            throw new RuntimeException("The token is {$status}, not active.");
        }

        return 'token is active';
    }

    public function zones(): array
    {
        $zones = [];
        $page = 1;

        do {
            $body = $this->request('get', '/zones', ['per_page' => 50, 'page' => $page]);

            foreach ($body['result'] ?? [] as $zone) {
                $zones[] = ['id' => (string) $zone['id'], 'name' => $zone['name']];
            }

            $info = $body['result_info'] ?? [];
            $page++;
        } while (($info['total_pages'] ?? 1) >= $page);

        return $zones;
    }

    public function records(string $zoneId, string $zoneName): array
    {
        $records = [];
        $page = 1;

        do {
            $body = $this->request('get', "/zones/{$zoneId}/dns_records", ['per_page' => 100, 'page' => $page]);

            foreach ($body['result'] ?? [] as $record) {
                $records[] = [
                    'id' => (string) $record['id'],
                    'type' => strtoupper((string) $record['type']),
                    'name' => rtrim((string) $record['name'], '.'),
                    'content' => (string) ($record['content'] ?? ''),
                    'priority' => isset($record['priority']) ? (int) $record['priority'] : null,
                    // 1 is Cloudflare's "automatic", which is not a TTL.
                    'ttl' => ($record['ttl'] ?? 1) > 1 ? (int) $record['ttl'] : null,
                    'proxied' => (bool) ($record['proxied'] ?? false),
                ];
            }

            $info = $body['result_info'] ?? [];
            $page++;
        } while (($info['total_pages'] ?? 1) >= $page);

        return $records;
    }

    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('get', "/zones/{$zoneId}/dns_records", [
            'name' => $record->name,
            'type' => $record->type,
            'per_page' => 1,
        ]);

        return isset($body['result'][0]['id']) ? (string) $body['result'][0]['id'] : null;
    }

    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string
    {
        $body = $this->request('post', "/zones/{$zoneId}/dns_records", $this->payload($record));

        return isset($body['result']['id']) ? (string) $body['result']['id'] : null;
    }

    public function update(string $zoneId, string $zoneName, string $recordId, DnsRecord $record): ?string
    {
        $body = $this->request('put', "/zones/{$zoneId}/dns_records/{$recordId}", $this->payload($record));

        return isset($body['result']['id']) ? (string) $body['result']['id'] : $recordId;
    }

    public function delete(string $zoneId, string $zoneName, string $recordId): void
    {
        $this->request('delete', "/zones/{$zoneId}/dns_records/{$recordId}");
    }

    /**
     * @return array<string, mixed>
     */
    protected function payload(DnsRecord $record): array
    {
        // ttl 1 is Cloudflare's "automatic"; proxied records must use it.
        return array_filter([
            'type' => $record->type,
            'name' => $record->name,
            'content' => $record->content,
            'ttl' => $record->proxied ? 1 : $record->ttlOr(1),
            'priority' => $record->priority,
            'proxied' => $record->proxied,
        ], fn ($value) => $value !== null);
    }

    protected function http(): PendingRequest
    {
        return $this->json()->withToken($this->token);
    }

    /** Cloudflare reports failures in its own envelope, not just by status. */
    protected function request(string $method, string $path, array $data = []): array
    {
        $body = parent::request($method, $path, $data);

        if (($body['success'] ?? true) !== true) {
            throw new RuntimeException('Cloudflare: '.$this->errorMessage($body, 200));
        }

        return $body;
    }
}
