<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Cloudflare stops being the only DNS host the panel can write to.
 *
 * The tables were named for it because it was the only one; now the credential
 * carries which provider it belongs to, and gets a second secret for the ones
 * that issue a key/secret pair rather than a single token.
 *
 * Existing rows are Cloudflare by definition, so the column defaults to it and
 * nothing has to be migrated by hand.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::rename('cloudflare_accounts', 'dns_accounts');

        Schema::table('dns_accounts', function (Blueprint $table) {
            $table->string('provider')->default('cloudflare')->after('user_id');
            $table->text('api_secret')->nullable()->after('api_token');
        });

        Schema::table('sites', function (Blueprint $table) {
            $table->renameColumn('cloudflare_account_id', 'dns_account_id');
            $table->renameColumn('cloudflare_zone_id', 'dns_zone_id');
            $table->renameColumn('cloudflare_record_ids', 'dns_record_ids');
        });

        Schema::table('email_domains', function (Blueprint $table) {
            $table->renameColumn('cloudflare_account_id', 'dns_account_id');
        });
    }

    public function down(): void
    {
        Schema::table('email_domains', function (Blueprint $table) {
            $table->renameColumn('dns_account_id', 'cloudflare_account_id');
        });

        Schema::table('sites', function (Blueprint $table) {
            $table->renameColumn('dns_record_ids', 'cloudflare_record_ids');
            $table->renameColumn('dns_zone_id', 'cloudflare_zone_id');
            $table->renameColumn('dns_account_id', 'cloudflare_account_id');
        });

        Schema::table('dns_accounts', function (Blueprint $table) {
            $table->dropColumn(['provider', 'api_secret']);
        });

        Schema::rename('dns_accounts', 'cloudflare_accounts');
    }
};
