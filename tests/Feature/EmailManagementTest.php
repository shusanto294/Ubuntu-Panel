<?php

namespace Tests\Feature;

use App\Jobs\CreateEmailAccount;
use App\Jobs\CreateEmailDomain;
use App\Jobs\DeleteEmailAccount;
use App\Models\EmailAccount;
use App\Models\EmailDomain;
use App\Models\User;
use App\Services\Mail\MailManager;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

class EmailManagementTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();

        Cache::put('panel:public-ip', '203.0.113.10', now()->addHour());
    }

    /** A machine with (or without) the mail stack installed. */
    protected function withMailServer(bool $configured = true): void
    {
        $this->markInstalled($configured ? ['mysql', 'mail'] : ['mysql']);

        app(\App\Support\Settings::class)->set('mail_configured', $configured ? '1' : '0');
        app(\App\Support\Settings::class)->set('mail_hostname', 'mail.example.com');
    }

    public function test_a_mail_domain_can_be_added(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withMailServer();

        $this->actingAs($user)->post(route('email.domains.store'), [
            'domain' => 'Example.com',
            'dkim_selector' => 'mail',
        ])->assertRedirect();

        $domain = EmailDomain::first();

        $this->assertSame('example.com', $domain->domain);
        $this->assertSame('pending', $domain->status);
        Queue::assertPushed(CreateEmailDomain::class);
    }

    public function test_a_domain_cannot_be_added_before_the_mail_stack_is_installed(): void
    {
        $user = User::factory()->create();
        $this->withMailServer(configured: false);

        $this->actingAs($user)->post(route('email.domains.store'), [
            'domain' => 'example.com',
        ])->assertSessionHasErrors('domain');

        $this->assertSame(0, EmailDomain::count());
    }

    public function test_a_mailbox_can_be_created(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withMailServer();

        $domain = EmailDomain::create([
            'user_id' => $user->id, 'domain' => 'example.com', 'status' => 'active',
        ]);

        $this->actingAs($user)->post(route('email.accounts.store', $domain), [
            'local_part' => 'Info',
            'password' => 'a-long-enough-password',
            'quota_mb' => 4096,
        ])->assertRedirect();

        $account = EmailAccount::first();

        $this->assertSame('info', $account->local_part);
        $this->assertSame('info@example.com', $account->address());
        $this->assertSame(4096, $account->quota_mb);
        Queue::assertPushed(CreateEmailAccount::class);
    }

    public function test_mailbox_passwords_are_encrypted_at_rest(): void
    {
        $user = User::factory()->create();
        $this->withMailServer();

        $domain = EmailDomain::create([
            'user_id' => $user->id, 'domain' => 'example.com',
        ]);

        $account = $domain->accounts()->create([
            'user_id' => $user->id, 'local_part' => 'info', 'password' => 'mailbox-secret',
        ]);

        $raw = $this->getConnection()->table('email_accounts')->where('id', $account->id)->first();

        $this->assertNotSame('mailbox-secret', $raw->password);
        $this->assertSame('mailbox-secret', $account->fresh()->password);
    }

    public function test_duplicate_addresses_are_rejected(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withMailServer();

        $domain = EmailDomain::create([
            'user_id' => $user->id, 'domain' => 'example.com',
        ]);

        $payload = ['local_part' => 'info', 'password' => 'a-long-enough-password', 'quota_mb' => 2048];

        $this->actingAs($user)->post(route('email.accounts.store', $domain), $payload)->assertRedirect();
        $this->actingAs($user)->post(route('email.accounts.store', $domain), $payload)->assertSessionHasErrors('local_part');
    }

    public function test_deleting_a_mailbox_queues_the_removal(): void
    {
        Queue::fake();
        $user = User::factory()->create();
        $this->withMailServer();

        $domain = EmailDomain::create([
            'user_id' => $user->id, 'domain' => 'example.com',
        ]);
        $account = $domain->accounts()->create([
            'user_id' => $user->id, 'local_part' => 'info', 'password' => 'secret',
        ]);

        $this->actingAs($user)->delete(route('email.accounts.destroy', $account))->assertRedirect();

        Queue::assertPushed(DeleteEmailAccount::class);
    }

    public function test_mail_dns_publishes_mx_spf_dkim_and_dmarc(): void
    {
        Http::fake([
            'api.cloudflare.com/client/v4/zones?*' => Http::response([
                'success' => true,
                'result' => [['id' => 'zone-1', 'name' => 'example.com']],
                'result_info' => ['total_pages' => 1],
            ]),
            'api.cloudflare.com/client/v4/zones/zone-1/dns_records?*' => Http::response([
                'success' => true, 'result' => [],
            ]),
            'api.cloudflare.com/client/v4/zones/zone-1/dns_records' => Http::response([
                'success' => true, 'result' => ['id' => 'record-1'],
            ]),
        ]);

        $user = User::factory()->create();
        $account = $user->dnsAccounts()->create(['provider' => 'cloudflare', 'label' => 'Personal', 'api_token' => 'cf-token']);
        $this->withMailServer();

        $domain = EmailDomain::create([
            'user_id' => $user->id,
            'dns_account_id' => $account->id,
            'domain' => 'example.com',
            'dkim_selector' => 'mail',
            'dkim_public_key' => 'v=DKIM1; k=rsa; p=MIIBIjANB',
            'manage_dns' => true,
        ]);

        app(MailManager::class)->publishDns($domain);

        $ids = $domain->fresh()->dns_record_ids;

        $this->assertArrayHasKey('MX:example.com', $ids);
        $this->assertArrayHasKey('TXT:example.com', $ids);
        $this->assertArrayHasKey('TXT:_dmarc.example.com', $ids);
        $this->assertArrayHasKey('TXT:mail._domainkey.example.com', $ids);
        $this->assertArrayHasKey('A:mail.example.com', $ids);
    }

    public function test_a_user_cannot_add_a_mailbox_to_another_users_domain(): void
    {
        $owner = User::factory()->create();
        $intruder = User::factory()->create();
        $this->withMailServer();

        $domain = EmailDomain::create([
            'user_id' => $owner->id, 'domain' => 'example.com',
        ]);

        $this->actingAs($intruder)->post(route('email.accounts.store', $domain), [
            'local_part' => 'info', 'password' => 'a-long-enough-password', 'quota_mb' => 2048,
        ])->assertForbidden();
    }

    /**
     * The mailbox password never reaches a command line.
     *
     * It used to: `doveadm pw -p <password>` put it in `ps` for every user on
     * the machine, in the task console, and — because doveadm could not read
     * /etc/dovecot as the panel user, so the command always failed — in the
     * error shown on the Email page. The panel was displaying the password
     * somebody had just typed into a form.
     */
    public function test_creating_a_mailbox_never_puts_the_password_in_a_command(): void
    {
        $this->withMailServer();

        $user = User::factory()->create();
        $domain = EmailDomain::create([
            'user_id' => $user->id,
            'domain' => 'example.com',
            'dkim_selector' => 'mail',
            'status' => 'active',
        ]);

        $account = $domain->accounts()->create([
            'user_id' => $user->id,
            'local_part' => 'info',
            'password' => 'a-secret-password',
            'quota_mb' => 2048,
            'status' => 'pending',
        ]);

        $connection = new \Tests\Support\FakeLocalConnection;
        $this->app->instance(\App\Services\Shell\LocalConnection::class, $connection);

        app(MailManager::class)->createAccount($account, 'a-secret-password');

        foreach ($connection->ran as $command) {
            $this->assertStringNotContainsString('a-secret-password', $command);
            $this->assertStringNotContainsString('doveadm pw', $command);
        }

        // What does go to the server is the hash Dovecot stores.
        $this->assertTrue($connection->ranCommandContaining('{SHA512-CRYPT}$6$'));
    }

    /** Secrets that do reach a command line stay out of the console and the row. */
    public function test_the_task_log_redacts_passwords(): void
    {
        $redacted = \App\Services\Tasks\TaskRunner::redact(
            "sudo mysql -e \"CREATE USER 'app'@'localhost' IDENTIFIED BY 'hunter2';\""
        );

        $this->assertStringNotContainsString('hunter2', $redacted);
        $this->assertStringContainsString("IDENTIFIED BY '***'", $redacted);
    }
}
