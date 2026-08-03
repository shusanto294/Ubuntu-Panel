<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class Database extends Model
{
    use HasFactory;

    protected $fillable = [
        'user_id', 'engine', 'name', 'username', 'password',
        'charset', 'status', 'last_error', 'managed_by_site',
    ];

    protected $hidden = ['password'];

    protected function casts(): array
    {
        return [
            'password' => 'encrypted',
            'managed_by_site' => 'boolean',
        ];
    }


    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function engineLabel(): string
    {
        return match ($this->engine) {
            'mysql' => 'MariaDB / MySQL',
            'postgres' => 'PostgreSQL',
            'mongodb' => 'MongoDB',
            default => $this->engine,
        };
    }

    public function defaultPort(): int
    {
        return match ($this->engine) {
            'postgres' => 5432,
            'mongodb' => 27017,
            default => 3306,
        };
    }
}
