<?php

namespace App\Services\Dns;

/**
 * One DNS record, in the panel's own terms.
 *
 * Names are always fully qualified here. Providers disagree about this — some
 * want `www`, some want `@` for the apex, some want the whole hostname — so the
 * conversion belongs in each driver rather than in every caller.
 */
class DnsRecord
{
    public function __construct(
        public readonly string $type,
        public readonly string $name,
        public readonly string $content,
        public readonly ?int $priority = null,
        public readonly int $ttl = 0,
        public readonly bool $proxied = false,
    ) {}

    /**
     * @param  array{type: string, name: string, content: string, priority?: int|null, ttl?: int|null, proxied?: bool|null}  $record
     */
    public static function fromArray(array $record): self
    {
        return new self(
            strtoupper($record['type']),
            rtrim($record['name'], '.'),
            $record['content'],
            isset($record['priority']) ? (int) $record['priority'] : null,
            (int) ($record['ttl'] ?? 0),
            (bool) ($record['proxied'] ?? false),
        );
    }

    /** The key this record is tracked under once written. */
    public function key(): string
    {
        return $this->type.':'.$this->name;
    }

    /**
     * The name relative to its zone: `www` for www.example.com in example.com,
     * and $apex for the zone's own name.
     */
    public function relativeName(string $zone, string $apex = '@'): string
    {
        $zone = rtrim($zone, '.');

        if ($this->name === $zone) {
            return $apex;
        }

        return str_ends_with($this->name, '.'.$zone)
            ? substr($this->name, 0, -(strlen($zone) + 1))
            : $this->name;
    }

    /** TTL in seconds, or the provider's own idea of "automatic". */
    public function ttlOr(int $default): int
    {
        return $this->ttl > 0 ? $this->ttl : $default;
    }
}
