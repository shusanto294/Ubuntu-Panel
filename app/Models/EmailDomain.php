<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

class EmailDomain extends Model
{
    use HasFactory;

    protected $fillable = [
        'user_id', 'dns_account_id', 'domain', 'status',
        'dkim_selector', 'dkim_public_key', 'manage_dns', 'dns_record_ids', 'last_error',
    ];

    protected function casts(): array
    {
        return [
            'dns_record_ids' => 'array',
            'manage_dns' => 'boolean',
        ];
    }


    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function dnsAccount(): BelongsTo
    {
        return $this->belongsTo(DnsAccount::class);
    }

    public function accounts(): HasMany
    {
        return $this->hasMany(EmailAccount::class);
    }
}
