<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Crypt;

/**
 * Panel-wide settings: the defaults and generated credentials that used to live
 * on a server row, now that there is exactly one machine to describe.
 *
 * Values marked secret (service passwords the panel generated during an
 * install) are encrypted with APP_KEY, the same as they were before.
 */
class Setting extends Model
{
    protected $primaryKey = 'key';

    protected $keyType = 'string';

    public $incrementing = false;

    protected $fillable = ['key', 'value', 'secret'];

    protected function casts(): array
    {
        return ['secret' => 'boolean'];
    }

    public function plainValue(): ?string
    {
        if ($this->value === null || ! $this->secret) {
            return $this->value;
        }

        try {
            return Crypt::decryptString($this->value);
        } catch (\Throwable $e) {
            // A rotated APP_KEY should not take the whole page down.
            return null;
        }
    }
}
