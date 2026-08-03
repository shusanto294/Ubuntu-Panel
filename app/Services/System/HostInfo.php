<?php

namespace App\Services\System;

use App\Services\Shell\LocalConnection;
use Illuminate\Support\Facades\Cache;

/**
 * Facts about the machine the panel runs on: its hostname, its OS, and the
 * address the outside world reaches it on (which DNS records point at).
 */
class HostInfo
{
    public function __construct(protected LocalConnection $shell) {}

    public function hostname(): string
    {
        return gethostname() ?: 'localhost';
    }

    /**
     * The public IPv4 address of this machine.
     *
     * The default route's source address is right on almost every VPS, and
     * unlike asking an external service it needs no network call. Cached
     * because DNS records and site defaults ask for it often.
     */
    public function publicIp(): ?string
    {
        return Cache::remember('panel:public-ip', now()->addHour(), function () {
            [$output, $code] = $this->shell->run(
                "ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1; i<=NF; i++) if (\$i == \"src\") print \$(i+1)}'"
            );

            $ip = trim(strtok(trim($output), "\n") ?: '');

            if ($code === 0 && filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4)) {
                return $ip;
            }

            // Not Linux, or no default route: fall back to whatever the
            // hostname resolves to.
            $resolved = gethostbyname($this->hostname());

            return filter_var($resolved, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) ? $resolved : null;
        });
    }

    /** Pretty OS name, e.g. "Ubuntu 24.04.1 LTS". */
    public function os(): ?string
    {
        if (! is_readable('/etc/os-release')) {
            return null;
        }

        foreach (preg_split('/\r?\n/', (string) file_get_contents('/etc/os-release')) as $line) {
            if (str_starts_with($line, 'PRETTY_NAME=')) {
                return trim(substr($line, 12), '"');
            }
        }

        return null;
    }

    public function forget(): void
    {
        Cache::forget('panel:public-ip');
    }
}
