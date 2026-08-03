<?php

namespace App\Support;

use App\Models\Setting;
use Illuminate\Support\Facades\Crypt;

/**
 * Reads and writes the panel's settings, with the whole table cached in memory
 * for the request — these are read on nearly every page.
 */
class Settings
{
    /** @var array<string, Setting>|null */
    protected ?array $loaded = null;

    public function get(string $key, mixed $default = null): mixed
    {
        $value = $this->all()[$key] ?? null;

        return $value?->plainValue() ?? $default;
    }

    public function set(string $key, ?string $value, bool $secret = false): void
    {
        Setting::updateOrCreate(
            ['key' => $key],
            [
                'value' => $secret && $value !== null ? Crypt::encryptString($value) : $value,
                'secret' => $secret,
            ]
        );

        $this->loaded = null;
    }

    public function forget(string $key): void
    {
        Setting::whereKey($key)->delete();

        $this->loaded = null;
    }

    public function boolean(string $key, bool $default = false): bool
    {
        $value = $this->get($key);

        return $value === null ? $default : in_array($value, ['1', 'true', 'yes', 'on'], true);
    }

    /** A password the panel generated once and must keep using. */
    public function secret(string $key): ?string
    {
        return $this->get($key);
    }

    public function rememberSecret(string $key, callable $generate): string
    {
        $existing = $this->get($key);

        if ($existing !== null && $existing !== '') {
            return $existing;
        }

        $value = (string) $generate();

        $this->set($key, $value, secret: true);

        return $value;
    }

    /** Default PHP version for new sites. */
    public function phpVersion(): string
    {
        return (string) $this->get('php_version', config('panel.php_versions')[0]);
    }

    public function nodeVersion(): string
    {
        return (string) $this->get('node_version', config('panel.node_versions')[0]);
    }

    /**
     * @return array<string, Setting>
     */
    protected function all(): array
    {
        return $this->loaded ??= Setting::all()->keyBy('key')->all();
    }

    public function flush(): void
    {
        $this->loaded = null;
    }
}
