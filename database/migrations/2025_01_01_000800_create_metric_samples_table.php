<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * One row per minute for this machine.
 *
 * The dashboard's live figures still come straight from /proc; this table only
 * exists so the graphs can look further back than the page has been open. A
 * minute of resolution over a month is ~43k rows, which is nothing, and the
 * collector prunes anything older.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('metric_samples', function (Blueprint $table) {
            $table->id();

            // Percentages, 0–100. CPU is null on the first sample after a
            // restart: it is a delta and there is nothing to compare against.
            $table->float('cpu_percent')->nullable();
            $table->float('memory_percent');
            $table->float('disk_percent');
            $table->float('swap_percent')->nullable();

            // Absolute figures, so a tooltip can say "3.1 GB of 8 GB" rather
            // than only a percentage, and a resized disk stays readable.
            $table->unsignedBigInteger('memory_used')->nullable();
            $table->unsignedBigInteger('memory_total')->nullable();
            $table->unsignedBigInteger('disk_used')->nullable();
            $table->unsignedBigInteger('disk_total')->nullable();

            $table->float('load_1')->nullable();

            $table->timestamp('sampled_at')->index();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('metric_samples');
    }
};
