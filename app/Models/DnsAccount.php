<?php

namespace App\Models;

use App\Services\Dns\DnsProvider;
use App\Services\Dns\DnsProviderRegistry;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A credential for one DNS host — Cloudflare, DigitalOcean, Linode and so on.
 *
 * Was `CloudflareAccount`, back when Cloudflare was the only one the panel
 * could talk to.
 */
class DnsAccount extends Model
{
    use HasFactory;

    protected $fillable = [
        'user_id', 'provider', 'label', 'api_token', 'api_secret',
        'email', 'account_id', 'verified_at',
    ];

    protected $hidden = ['api_token', 'api_secret'];

    protected function casts(): array
    {
        return [
            'api_token' => 'encrypted',
            'api_secret' => 'encrypted',
            'verified_at' => 'datetime',
        ];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function sites(): HasMany
    {
        return $this->hasMany(Site::class);
    }

    public function emailDomains(): HasMany
    {
        return $this->hasMany(EmailDomain::class);
    }

    public function driver(): DnsProvider
    {
        return DnsProviderRegistry::driver($this);
    }

    public function providerLabel(): string
    {
        return DnsProviderRegistry::label($this->provider);
    }

    public function supportsProxy(): bool
    {
        return DnsProviderRegistry::supportsProxy($this->provider);
    }

    /** What the browser is allowed to see: never the credential itself. */
    public function toPanelArray(): array
    {
        return [
            'id' => $this->id,
            'provider' => $this->provider,
            'provider_label' => $this->providerLabel(),
            'label' => $this->label,
            'supports_proxy' => $this->supportsProxy(),
            'verified_at' => $this->verified_at?->diffForHumans(),
            'sites_count' => $this->sites_count ?? null,
        ];
    }
}
