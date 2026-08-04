<?php

namespace App\Services\Dns;

/**
 * One DNS host the panel can write records to.
 *
 * Every provider models the same three things — zones, records in a zone, and
 * a credential — and disagrees about all the details: whether a record's name
 * is relative or absolute, whether its value is called `content`, `data`,
 * `target` or `value`, whether MX priority is a field or the first word of the
 * value. Drivers absorb that; callers work in {@see DnsRecord}s and fully
 * qualified names.
 *
 * Zones are passed around as id *and* name because providers key on one or the
 * other and there is no cheap way to look the other up mid-operation.
 */
interface DnsProvider
{
    /** Check the credential and return something worth showing the user. */
    public function verify(): string;

    /**
     * Every zone this credential can see.
     *
     * @return array<int, array{id: string, name: string}>
     */
    public function zones(): array;

    /**
     * The zone that owns a hostname — the longest matching suffix, so
     * `a.b.example.com` prefers `b.example.com` over `example.com`.
     *
     * @return array{id: string, name: string}|null
     */
    public function findZone(string $hostname): ?array;

    /** The id of an existing record of the same type and name, if there is one. */
    public function findRecordId(string $zoneId, string $zoneName, DnsRecord $record): ?string;

    /** Create the record and return the provider's id for it. */
    public function create(string $zoneId, string $zoneName, DnsRecord $record): ?string;

    /** Overwrite an existing record. */
    public function update(string $zoneId, string $zoneName, string $recordId, DnsRecord $record): ?string;

    public function delete(string $zoneId, string $zoneName, string $recordId): void;

    /** Can records be served through the provider's proxy/CDN? Cloudflare only. */
    public function supportsProxy(): bool;
}
