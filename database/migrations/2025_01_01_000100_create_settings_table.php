<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Panel-wide settings: defaults for new sites, the mail hostname, and the
 * service passwords the panel generates during an install. There is one machine
 * to describe, so a key/value table is the whole of it.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('settings', function (Blueprint $table) {
            $table->string('key')->primary();
            $table->text('value')->nullable();
            // Generated passwords are encrypted with APP_KEY.
            $table->boolean('secret')->default(false);
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('settings');
    }
};
