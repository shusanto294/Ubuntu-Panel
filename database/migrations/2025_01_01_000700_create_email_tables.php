<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('email_domains', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->foreignId('cloudflare_account_id')->nullable()->constrained()->nullOnDelete();
            $table->string('domain')->unique();
            $table->string('status')->default('pending'); // pending|active|failed|deleting
            $table->string('dkim_selector')->default('mail');
            $table->text('dkim_public_key')->nullable();
            $table->boolean('manage_dns')->default(false);
            $table->json('dns_record_ids')->nullable();
            $table->text('last_error')->nullable();
            $table->timestamps();
        });

        Schema::create('email_accounts', function (Blueprint $table) {
            $table->id();
            $table->foreignId('email_domain_id')->constrained()->cascadeOnDelete();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->string('local_part');
            $table->text('password');
            $table->unsignedInteger('quota_mb')->default(2048);
            $table->string('status')->default('pending'); // pending|active|failed
            $table->text('last_error')->nullable();
            $table->timestamps();

            $table->unique(['email_domain_id', 'local_part']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('email_accounts');
        Schema::dropIfExists('email_domains');
    }
};
