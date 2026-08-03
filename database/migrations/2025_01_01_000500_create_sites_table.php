<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('sites', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->foreignId('cloudflare_account_id')->nullable()->constrained()->nullOnDelete();
            $table->string('domain')->unique();
            // php | laravel | wordpress | nodejs | nextjs | static
            $table->string('type')->default('php');
            $table->json('aliases')->nullable();
            $table->string('root_path');
            $table->string('web_directory')->default('/public');
            $table->string('php_version')->default('8.3');
            $table->string('status')->default('pending'); // pending|deploying|active|failed|deleting
            $table->boolean('ssl')->default(false);
            $table->text('last_error')->nullable();

            // Node-family apps run behind an nginx reverse proxy on their own port.
            $table->unsignedInteger('app_port')->nullable();
            $table->string('start_command')->nullable();
            $table->string('build_command')->nullable();
            $table->string('node_version')->nullable();

            // Optional git deployment.
            $table->string('repository')->nullable();
            $table->string('branch')->default('main');

            // Auto-created application database (WordPress/Laravel).
            $table->foreignId('database_id')->nullable();

            // WordPress bootstrap details.
            $table->string('wp_admin_user')->nullable();
            $table->text('wp_admin_password')->nullable();
            $table->string('wp_admin_email')->nullable();
            $table->string('wp_title')->nullable();

            // Cloudflare DNS
            $table->boolean('manage_dns')->default(false);
            $table->string('cloudflare_zone_id')->nullable();
            $table->json('cloudflare_record_ids')->nullable();
            $table->string('dns_type')->default('A');
            $table->string('dns_content')->nullable();
            $table->boolean('dns_proxied')->default(true);

            $table->timestamps();

            $table->index('status');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('sites');
    }
};
