<?php

namespace App\Services\Dns;

use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;
use RuntimeException;

/**
 * The parts every REST-shaped DNS provider does the same way.
 */
abstract class HttpProvider implements DnsProvider
{
    protected string $base = '';

    abstract protected function http(): PendingRequest;

    public function supportsProxy(): bool
    {
        return false;
    }

    public function findZone(string $hostname): ?array
    {
        $hostname = rtrim($hostname, '.');
        $best = null;

        foreach ($this->zones() as $zone) {
            $name = rtrim($zone['name'], '.');

            if ($name !== $hostname && ! str_ends_with($hostname, '.'.$name)) {
                continue;
            }

            // The most specific zone wins, so a delegated subdomain beats the
            // parent it was carved out of.
            if ($best === null || strlen($name) > strlen($best['name'])) {
                $best = ['id' => (string) $zone['id'], 'name' => $name];
            }
        }

        return $best;
    }

    /**
     * A provider's relative name, made absolute.
     *
     * Providers spell the apex as `@`, as the empty string, or as the zone
     * name itself, and everything else as either a label or the whole
     * hostname. Callers get one shape.
     */
    protected function absoluteName(?string $name, string $zone): string
    {
        $zone = rtrim($zone, '.');
        $name = rtrim((string) $name, '.');

        if ($name === '' || $name === '@' || $name === $zone) {
            return $zone;
        }

        return str_ends_with($name, '.'.$zone) ? $name : $name.'.'.$zone;
    }

    /**
     * @param  array<string, mixed>  $data
     * @return array<string, mixed>
     */
    protected function request(string $method, string $path, array $data = []): array
    {
        $url = str_starts_with($path, 'http') ? $path : $this->base.$path;

        $response = $this->http()->{$method}($url, $data);

        if (! $response->successful()) {
            throw new RuntimeException(static::class.': '.$this->errorMessage($response->json() ?? [], $response->status()));
        }

        return $response->json() ?? [];
    }

    /**
     * @param  array<string, mixed>  $body
     */
    protected function errorMessage(array $body, int $status): string
    {
        foreach (['message', 'error', 'reason', 'detail'] as $key) {
            if (is_string($body[$key] ?? null) && $body[$key] !== '') {
                return $body[$key];
            }
        }

        if (isset($body['errors']) && is_array($body['errors'])) {
            $flat = [];

            array_walk_recursive($body['errors'], function ($value) use (&$flat) {
                if (is_scalar($value) && (string) $value !== '') {
                    $flat[] = (string) $value;
                }
            });

            if ($flat !== []) {
                return implode('; ', array_slice($flat, 0, 4));
            }
        }

        return "HTTP {$status}";
    }

    protected function json(): PendingRequest
    {
        return Http::acceptJson()->timeout(30)->retry(2, 250);
    }
}
