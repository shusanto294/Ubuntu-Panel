<?php

namespace Tests\Feature;

use App\Models\EmailDomain;
use App\Models\User;
use App\Services\Mail\MailManager;
use App\Services\Mail\Roundcube;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceCatalog;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

class RoundcubeTest extends TestCase
{
    use RefreshDatabase;

    protected function domain(string $name = 'example.com'): EmailDomain
    {
        return EmailDomain::create([
            'user_id' => User::factory()->create()->id,
            'domain' => $name,
            'dkim_selector' => 'mail',
            'dkim_public_key' => 'v=DKIM1; k=rsa; p=AAAA',
            'status' => 'active',
        ]);
    }

    public function test_webmail_answers_at_mail_dot_the_domain(): void
    {
        $domain = $this->domain('example.com');

        $this->assertSame('mail.example.com', Roundcube::hostFor($domain));
        $this->assertSame('https://mail.example.com/', Roundcube::urlFor($domain));

        // The address is a typing shortcut for the login form, nothing more.
        $this->assertSame(
            'https://mail.example.com/?_user=info%40example.com',
            Roundcube::urlFor($domain, 'info@example.com')
        );
    }

    public function test_it_is_installed_by_default_and_needs_the_mail_server(): void
    {
        $this->assertContains('roundcube', ServiceCatalog::keys());
        $this->assertTrue(config('panel.services.roundcube.default'));
        $this->assertContains('roundcube', ServiceCatalog::defaults());

        // Ordering matters: the catalogue installs in array order, and webmail
        // against a mail server that is not there configures nothing.
        $this->assertGreaterThan(
            ServiceCatalog::sortOrder('mail'),
            ServiceCatalog::sortOrder('roundcube')
        );
        $this->assertContains('mail', config('panel.services.roundcube.requires'));
    }

    public function test_the_plain_vhost_never_references_a_certificate(): void
    {
        $vhost = app(Roundcube::class)->vhost($this->domain(), tls: false);

        // nginx refuses to start when ssl_certificate names a file that is not
        // there, so the HTTP form must not mention one.
        $this->assertStringNotContainsString('ssl_certificate', $vhost);
        $this->assertStringContainsString('listen 80;', $vhost);
        $this->assertStringContainsString('server_name mail.example.com;', $vhost);
        $this->assertStringNotContainsString('default_server', $vhost);
    }

    public function test_the_tls_vhost_redirects_http_and_keeps_the_acme_path_open(): void
    {
        $vhost = app(Roundcube::class)->vhost($this->domain(), tls: true);

        $this->assertStringContainsString('listen 443 ssl;', $vhost);
        $this->assertStringContainsString(
            'ssl_certificate     /etc/letsencrypt/live/mail.example.com/fullchain.pem;',
            $vhost
        );
        $this->assertStringContainsString('return 301 https://$host$request_uri;', $vhost);
        // Renewal still has to be able to answer a challenge over plain HTTP.
        $this->assertStringContainsString('/.well-known/acme-challenge/', $vhost);
    }

    public function test_roundcubes_private_directories_are_not_served(): void
    {
        $vhost = app(Roundcube::class)->vhost($this->domain());

        $this->assertMatchesRegularExpression(
            '/location ~ \^\/\(config\|temp\|logs\|SQL\|bin\|installer\)\/ \{\s*deny all;/',
            $vhost
        );
    }

    public function test_it_talks_to_dovecot_and_postfix_over_loopback(): void
    {
        $config = app(Roundcube::class)->config('secret');

        // Localhost sidesteps the mail server's own certificate entirely, and
        // the connection that has to be encrypted is the browser's.
        $this->assertStringContainsString("\$config['imap_host'] = 'localhost:143';", $config);
        $this->assertStringContainsString("\$config['smtp_host'] = 'localhost:587';", $config);
        $this->assertStringContainsString("\$config['enable_installer'] = false;", $config);
        $this->assertStringContainsString('mysql://roundcube:secret@localhost/roundcube', $config);
    }

    /**
     * Everything a receiving server scores, in one publish.
     */
    public function test_a_mail_domain_publishes_the_full_deliverability_record_set(): void
    {
        $domain = $this->domain('example.com');
        $domain->update(['manage_dns' => true]);

        $account = $domain->user->dnsAccounts()->create([
            'provider' => 'cloudflare',
            'label' => 'Personal',
            'api_token' => 'cf-token',
        ]);
        $domain->update(['dns_account_id' => $account->id]);

        $written = [];

        $dns = \Mockery::mock(\App\Services\Dns\DnsManager::class);
        $dns->shouldReceive('writeRecords')
            ->once()
            ->andReturnUsing(function ($account, $zone, $records) use (&$written) {
                $written = $records;

                return ['ids' => ['a'], 'log' => 'ok'];
            });

        $this->app->instance(\App\Services\Dns\DnsManager::class, $dns);

        $host = \Mockery::mock(\App\Services\System\HostInfo::class);
        $host->shouldReceive('publicIp')->andReturn('203.0.113.10');
        $host->shouldReceive('hostname')->andReturn('vps');
        $this->app->instance(\App\Services\System\HostInfo::class, $host);

        app(MailManager::class)->publishDns($domain->fresh());

        $types = collect($written);

        $this->assertTrue($types->contains(fn ($r) => $r['type'] === 'MX' && $r['name'] === 'example.com'));
        $this->assertTrue($types->contains(fn ($r) => $r['type'] === 'A' && $r['name'] === 'mail.example.com'));
        $this->assertTrue($types->contains(
            fn ($r) => $r['type'] === 'TXT' && $r['name'] === 'example.com' && str_starts_with($r['content'], 'v=spf1')
        ));
        $this->assertTrue($types->contains(
            fn ($r) => $r['name'] === '_dmarc.example.com' && str_contains($r['content'], 'v=DMARC1')
        ));
        $this->assertTrue($types->contains(
            fn ($r) => $r['name'] === 'mail._domainkey.example.com' && str_contains($r['content'], 'v=DKIM1')
        ));
        $this->assertTrue($types->contains(
            fn ($r) => $r['name'] === '_smtp._tls.example.com' && str_contains($r['content'], 'TLSRPTv1')
        ));
        $this->assertTrue($types->contains(
            fn ($r) => $r['type'] === 'CNAME' && $r['name'] === 'autodiscover.example.com'
        ));

        // A soft fail, deliberately: it scores the same everywhere it is
        // measured and does not destroy mail sent for this domain from
        // somewhere else.
        $spf = $types->first(fn ($r) => $r['type'] === 'TXT' && $r['name'] === 'example.com');
        $this->assertStringEndsWith('~all', $spf['content']);
    }

    /**
     * Postfix and Dovecot present the certificate for the hostname the panel
     * tells you to connect to, or every client that verifies refuses.
     */
    public function test_the_mail_daemons_are_given_the_per_domain_certificates(): void
    {
        $this->domain('example.com');
        $this->domain('other.test');

        $connection = new FakeLocalConnection([
            // Only example.com has been issued one. Matched on the path
            // alone: escapeshellarg quotes it, so `test -f /etc/...` is not a
            // substring of the command that actually runs.
            '/etc/letsencrypt/live/mail.example.com/fullchain.pem' => ['', 0],
            '/etc/letsencrypt/live/mail.other.test/fullchain.pem' => ['', 1],
        ]);

        $this->app->instance(LocalConnection::class, $connection);

        $result = app(\App\Services\Mail\MailCertificates::class)->sync($connection);

        $map = $connection->files['/etc/postfix/vmail_ssl.map'];
        $sni = $connection->files['/etc/dovecot/panel-sni.conf'];

        $this->assertStringContainsString(
            'mail.example.com /etc/letsencrypt/live/mail.example.com/privkey.pem',
            $map
        );
        $this->assertStringNotContainsString('mail.other.test', $map);

        // Naming a certificate that is not on disk stops the daemon dead, which
        // would take mail down for every other domain too.
        $this->assertStringContainsString('local_name mail.example.com {', $sni);
        $this->assertStringNotContainsString('mail.other.test', $sni);

        $this->assertTrue($connection->ranCommandContaining('postmap -F'));
        $this->assertTrue($connection->ranCommandContaining('tls_server_sni_maps=hash:/etc/postfix/vmail_ssl.map'));
        $this->assertStringContainsString('mail.example.com', $result);
    }
}
