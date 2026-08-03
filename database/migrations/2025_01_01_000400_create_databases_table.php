<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('databases', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->string('engine');            // mysql | postgres | mongodb
            $table->string('name');
            $table->string('username')->nullable();
            $table->text('password')->nullable();
            $table->string('charset')->nullable();
            $table->string('status')->default('pending'); // pending|creating|ready|failed|deleting
            $table->text('last_error')->nullable();
            $table->boolean('managed_by_site')->default(false);
            $table->timestamps();

            $table->unique(['engine', 'name']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('databases');
    }
};
