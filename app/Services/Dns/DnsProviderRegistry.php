<?php

namespace App\Services\Dns;

use App\Models\DnsAccount;
use App\Services\Dns\Providers\CloudflareProvider;
use App\Services\Dns\Providers\DigitalOceanProvider;
use App\Services\Dns\Providers\HetznerProvider;
use App\Services\Dns\Providers\LinodeProvider;
use App\Services\Dns\Providers\PorkbunProvider;
use App\Services\Dns\Providers\VultrProvider;
use InvalidArgumentException;

/**
 * The DNS hosts the panel knows how to write to.
 *
 * Everything the UI needs to render a credential form lives here — what the
 * secret is called at that provider, whether there are two of them, and where
 * to go and get one — so adding a provider is a driver plus an entry.
 */
class DnsProviderRegistry
{
    /**
     * @return array<string, array{
     *     label: string,
     *     token_label: string,
     *     secret_label: ?string,
     *     proxy: bool,
     *     help: string,
     *     url: string
     * }>
     */
    public static function all(): array
    {
        return [
            'cloudflare' => [
                'label' => 'Cloudflare',
                'token_label' => 'API token',
                'secret_label' => null,
                'proxy' => true,
                'help' => 'Create a token with Zone · DNS · Edit on the zones you want the panel to manage.',
                'url' => 'https://dash.cloudflare.com/profile/api-tokens',
            ],
            'digitalocean' => [
                'label' => 'DigitalOcean',
                'token_label' => 'Personal access token',
                'secret_label' => null,
                'proxy' => false,
                'help' => 'A token with write scope. The domain has to already exist under Networking · Domains.',
                'url' => 'https://cloud.digitalocean.com/account/api/tokens',
            ],
            'linode' => [
                'label' => 'Linode',
                'token_label' => 'Personal access token',
                'secret_label' => null,
                'proxy' => false,
                'help' => 'A token with read/write access to Domains.',
                'url' => 'https://cloud.linode.com/profile/tokens',
            ],
            'vultr' => [
                'label' => 'Vultr',
                'token_label' => 'API key',
                'secret_label' => null,
                'proxy' => false,
                'help' => 'Enable the API and allow this server\'s address in the access-control list, or every call is rejected.',
                'url' => 'https://my.vultr.com/settings/#settingsapi',
            ],
            'hetzner' => [
                'label' => 'Hetzner DNS',
                'token_label' => 'API token',
                'secret_label' => null,
                'proxy' => false,
                'help' => 'From the Hetzner DNS console, not the Cloud console — they are separate products with separate tokens.',
                'url' => 'https://dns.hetzner.com/settings/api-token',
            ],
            'porkbun' => [
                'label' => 'Porkbun',
                'token_label' => 'API key',
                'secret_label' => 'Secret key',
                'proxy' => false,
                'help' => 'Two keys, and API access has to be switched on per domain in the domain\'s own settings.',
                'url' => 'https://porkbun.com/account/api',
            ],
        ];
    }

    /**
     * @return array<int, string>
     */
    public static function keys(): array
    {
        return array_keys(self::all());
    }

    public static function exists(string $key): bool
    {
        return array_key_exists($key, self::all());
    }

    public static function label(string $key): string
    {
        return self::all()[$key]['label'] ?? ucfirst($key);
    }

    /** Does this provider need a second credential? */
    public static function needsSecret(string $key): bool
    {
        return (self::all()[$key]['secret_label'] ?? null) !== null;
    }

    public static function supportsProxy(string $key): bool
    {
        return (bool) (self::all()[$key]['proxy'] ?? false);
    }

    /**
     * The list the browser renders the "add a credential" form from.
     *
     * @return array<int, array<string, mixed>>
     */
    public static function options(): array
    {
        $options = [];

        foreach (self::all() as $key => $meta) {
            $options[] = ['key' => $key] + $meta;
        }

        return $options;
    }

    /** Build a driver for a stored credential. */
    public static function driver(DnsAccount $account): DnsProvider
    {
        $token = (string) $account->api_token;
        $secret = (string) $account->api_secret;

        return match ($account->provider) {
            'cloudflare' => new CloudflareProvider($token),
            'digitalocean' => new DigitalOceanProvider($token),
            'linode' => new LinodeProvider($token),
            'vultr' => new VultrProvider($token),
            'hetzner' => new HetznerProvider($token),
            'porkbun' => new PorkbunProvider($token, $secret),
            default => throw new InvalidArgumentException("Unknown DNS provider: {$account->provider}"),
        };
    }
}
