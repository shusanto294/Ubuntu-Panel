<?php

namespace App\Services\Cloudflare;

use App\Models\CloudflareAccount;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;
use RuntimeException;

/**
 * Minimal Cloudflare API v4 client scoped to a stored API token.
 */
class CloudflareClient
{
    protected const BASE = 'https://api.cloudflare.com/client/v4';

    public function __construct(protected string $token) {}

    public static function for(CloudflareAccount $account): self
    {
        return new self((string) $account->api_token);
    }

    protected function http(): PendingRequest
    {
        return Http::withToken($this->token)
            ->acceptJson()
            ->timeout(30)
            ->retry(2, 250);
    }

    /** Verify the token is valid. */
    public function verifyToken(): array
    {
        return $this->request('get', '/user/tokens/verify');
    }

    /**
     * All zones visible to this token.
     *
     * @return array<int, array<string, mixed>>
     */
    public function zones(): array
    {
        $zones = [];
        $page = 1;

        do {
            $result = $this->request('get', '/zones', ['per_page' => 50, 'page' => $page]);
            $zones = array_merge($zones, $result['result'] ?? []);
            $info = $result['result_info'] ?? [];
            $page++;
        } while (($info['total_pages'] ?? 1) >= $page);

        return $zones;
    }

    /** Find the zone whose name is a suffix of the given hostname. */
    public function findZoneForHostname(string $hostname): ?array
    {
        $best = null;

        foreach ($this->zones() as $zone) {
            $name = $zone['name'] ?? '';

            if ($name === $hostname || str_ends_with($hostname, '.'.$name)) {
                if ($best === null || strlen($name) > strlen($best['name'])) {
                    $best = $zone;
                }
            }
        }

        return $best;
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    public function dnsRecords(string $zoneId, array $filters = []): array
    {
        $result = $this->request('get', "/zones/{$zoneId}/dns_records", $filters + ['per_page' => 100]);

        return $result['result'] ?? [];
    }

    public function createDnsRecord(string $zoneId, array $payload): array
    {
        $result = $this->request('post', "/zones/{$zoneId}/dns_records", $payload);

        return $result['result'] ?? [];
    }

    public function updateDnsRecord(string $zoneId, string $recordId, array $payload): array
    {
        $result = $this->request('put', "/zones/{$zoneId}/dns_records/{$recordId}", $payload);

        return $result['result'] ?? [];
    }

    public function deleteDnsRecord(string $zoneId, string $recordId): void
    {
        $this->request('delete', "/zones/{$zoneId}/dns_records/{$recordId}");
    }

    protected function request(string $method, string $path, array $data = []): array
    {
        $response = $this->http()->{$method}(self::BASE.$path, $data);
        $body = $response->json() ?? [];

        if (! $response->successful() || ($body['success'] ?? false) !== true) {
            throw new RuntimeException('Cloudflare API error: '.$this->errorMessage($body, $response->status()));
        }

        return $body;
    }

    protected function errorMessage(array $body, int $status): string
    {
        $errors = collect($body['errors'] ?? [])
            ->map(fn ($e) => trim(($e['code'] ?? '').' '.($e['message'] ?? '')))
            ->filter()
            ->implode('; ');

        return $errors !== '' ? $errors : "HTTP {$status}";
    }
}
