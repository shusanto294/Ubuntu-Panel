<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * One row per installable piece of software on this machine, with its status.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('services', function (Blueprint $table) {
            $table->id();
            $table->string('key')->unique();
            // not_installed | queued | installing | installed | failed
            $table->string('status')->default('not_installed');
            $table->string('version')->nullable();
            $table->text('last_error')->nullable();
            $table->foreignId('task_id')->nullable();
            $table->unsignedSmallInteger('sort_order')->default(0);
            $table->timestamp('queued_at')->nullable();
            $table->timestamp('installed_at')->nullable();
            $table->timestamps();

            $table->index('status');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('services');
    }
};
