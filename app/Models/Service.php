<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One installable piece of software on this machine, with its own status.
 */
class Service extends Model
{
    use HasFactory;

    public const NOT_INSTALLED = 'not_installed';

    public const QUEUED = 'queued';

    public const INSTALLING = 'installing';

    public const INSTALLED = 'installed';

    public const FAILED = 'failed';

    protected $fillable = [
        'key', 'status', 'version', 'last_error',
        'task_id', 'sort_order', 'queued_at', 'installed_at',
    ];

    protected function casts(): array
    {
        return [
            'queued_at' => 'datetime',
            'installed_at' => 'datetime',
        ];
    }


    /** Is a given piece of software installed on this machine? */
    public static function installed(string $key): bool
    {
        return static::query()->where('key', $key)->where('status', self::INSTALLED)->exists();
    }

    /** The services that can hold a database. */
    public const ENGINE_KEYS = ['mysql', 'postgres', 'mongodb'];

    /**
     * Database engines this machine can actually host.
     *
     * @return array<int, string>
     */
    public static function availableEngines(): array
    {
        return static::query()
            ->whereIn('key', self::ENGINE_KEYS)
            ->where('status', self::INSTALLED)
            ->pluck('key')
            ->all();
    }

    /**
     * @return array<int, string>
     */
    public static function installedKeys(): array
    {
        return static::query()->where('status', self::INSTALLED)->pluck('key')->all();
    }

    public static function hasPending(): bool
    {
        return static::query()->whereIn('status', [self::QUEUED, self::INSTALLING])->exists();
    }

    public function task(): BelongsTo
    {
        return $this->belongsTo(ActivityLog::class, 'task_id');
    }

    /** Static metadata for this service from config/panel.php. */
    public function meta(): array
    {
        return config('panel.services.'.$this->key, []);
    }

    public function label(): string
    {
        return $this->meta()['label'] ?? $this->key;
    }

    public function isCore(): bool
    {
        return (bool) ($this->meta()['core'] ?? false);
    }

    public function isInstalled(): bool
    {
        return $this->status === self::INSTALLED;
    }

    public function isPending(): bool
    {
        return in_array($this->status, [self::QUEUED, self::INSTALLING], true);
    }

    public function toArray(): array
    {
        $meta = $this->meta();

        return [
            'id' => $this->id,
            'key' => $this->key,
            'label' => $this->label(),
            'group' => $meta['group'] ?? 'other',
            'description' => $meta['description'] ?? null,
            'core' => $this->isCore(),
            'requires' => $meta['requires'] ?? [],
            'status' => $this->status,
            'version' => $this->version,
            'last_error' => $this->last_error,
            'task_id' => $this->task_id,
            'installed_at' => $this->installed_at?->toDateTimeString(),
        ];
    }
}
