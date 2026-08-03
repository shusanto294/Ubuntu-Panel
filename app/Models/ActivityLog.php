<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class ActivityLog extends Model
{
    /** Keep the tail of very chatty tasks rather than unbounded output. */
    public const MAX_OUTPUT = 400_000;

    protected $fillable = [
        'user_id', 'site_id', 'type', 'action', 'status', 'message', 'output',
        'steps', 'current_step', 'progress', 'started_at', 'finished_at',
    ];

    protected function casts(): array
    {
        return [
            'steps' => 'array',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
        ];
    }


    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    public function isRunning(): bool
    {
        return $this->status === 'running';
    }

    public static function record(array $attributes): self
    {
        return static::create($attributes);
    }

    /** Append output, trimming from the front once it gets too large. */
    public function appendOutput(string $chunk): void
    {
        $output = (string) $this->output.$chunk;

        if (strlen($output) > self::MAX_OUTPUT) {
            $output = "…output truncated…\n".substr($output, -self::MAX_OUTPUT);
        }

        $this->output = $output;
    }

    /** Shape consumed by the polling endpoint and the live console component. */
    public function toConsolePayload(): array
    {
        return [
            'id' => $this->id,
            'type' => $this->type,
            'action' => $this->action,
            'status' => $this->status,
            'message' => $this->message,
            'output' => (string) $this->output,
            'steps' => $this->steps ?? [],
            'current_step' => $this->current_step,
            'progress' => (int) $this->progress,
            'running' => $this->isRunning(),
            'started_at' => $this->started_at?->toDateTimeString(),
            'finished_at' => $this->finished_at?->toDateTimeString(),
            'created_at' => $this->created_at?->toDateTimeString(),
        ];
    }
}
