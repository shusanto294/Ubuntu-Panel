<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * One recorded reading of this machine's CPU, memory and disk.
 *
 * Written once a minute by the scheduler; read by the dashboard graphs when the
 * user asks for a range longer than the page has been open.
 */
class MetricSample extends Model
{
    public $timestamps = false;

    protected $fillable = [
        'cpu_percent', 'memory_percent', 'disk_percent', 'swap_percent',
        'memory_used', 'memory_total', 'disk_used', 'disk_total',
        'load_1', 'sampled_at',
    ];

    protected function casts(): array
    {
        return [
            'cpu_percent' => 'float',
            'memory_percent' => 'float',
            'disk_percent' => 'float',
            'swap_percent' => 'float',
            'memory_used' => 'integer',
            'memory_total' => 'integer',
            'disk_used' => 'integer',
            'disk_total' => 'integer',
            'load_1' => 'float',
            'sampled_at' => 'datetime',
        ];
    }
}
