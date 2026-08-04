<?php

namespace Tests\Feature;

use App\Models\Service;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceInstaller;
use App\Support\Settings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

class MailProvisionTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    protected function provision(array $responses = []): FakeLocalConnection
    {
        $connection = new FakeLocalConnection($responses + [
            'command -v' => ['', 1],
            'test -f /etc/dovecot' => ['', 1],
        ]);

        $this->app->instance(LocalConnection::class, $connection);

        $this->markInstalled(['base', 'mysql']);

        $installer = $this->app->make(ServiceInstaller::class);
        $installer->queue(['mail']);
        $installer->installQueued();

        return $connection;
    }

    public function test_dovecot_is_configured_in_one_file_that_ignores_the_distribution_fragments(): void
    {
        $files = $this->provision()->files;

        $config = $files['/etc/dovecot/dovecot.conf'] ?? '';

        $this->assertNotSame('', $config);

        // conf.d belongs to the distribution. Ubuntu's own 15-mailboxes.conf
        // declares a second `namespace inbox`, and Dovecot will not start on a
        // duplicate — so the directory is not included and nothing of ours is
        // written into it.
        $this->assertStringNotContainsString('!include conf.d', $config);
        $this->assertSame(1, substr_count($config, 'namespace inbox'));

        foreach (array_keys($files) as $path) {
            $this->assertStringNotContainsString(
                '/etc/dovecot/conf.d/',
                $path,
                'nothing may be written into the distribution\'s conf.d'
            );
        }

        // Everything the fragments used to carry has to still be in there.
        $this->assertStringContainsString('mail_location = maildir:/var/mail/vhosts', $config);
        $this->assertStringContainsString('driver = sql', $config);
        $this->assertStringContainsString('/var/spool/postfix/private/dovecot-lmtp', $config);
        $this->assertStringContainsString('ssl = yes', $config);
    }

    /**
     * The parser is stricter than it looks and only says so at start-up, on
     * the server, at the end of an install — so check the shape here instead.
     */
    public function test_every_dovecot_block_opens_the_way_the_parser_wants(): void
    {
        $config = $this->provision()->files['/etc/dovecot/dovecot.conf'] ?? '';

        foreach (preg_split('/\r?\n/', $config) as $index => $line) {
            if (! str_contains($line, '{')) {
                continue;
            }

            $this->assertTrue(
                str_ends_with(rtrim($line), '{'),
                sprintf(
                    'dovecot.conf line %d puts a setting after the brace, which is a '.
                    "fatal \"Garbage after '{'\": %s",
                    $index + 1,
                    trim($line)
                )
            );
        }

        $this->assertSame(
            substr_count($config, '{'),
            substr_count($config, '}'),
            'unbalanced braces in dovecot.conf'
        );
    }

    public function test_the_mailname_postfix_reads_myorigin_from_is_written(): void
    {
        app(Settings::class)->set('mail_hostname', 'mail.example.com');

        $files = $this->provision()->files;

        $this->assertSame("mail.example.com\n", $files['/etc/mailname'] ?? null);
        $this->assertStringContainsString(
            'myorigin = /etc/mailname',
            $files['/etc/postfix/main.cf'] ?? ''
        );
    }

    public function test_a_missing_postfix_files_manifest_is_restored_before_the_check(): void
    {
        $ran = $this->provision()->ran;

        $repair = collect($ran)->search(
            fn (string $c) => str_contains($c, 'test -f /etc/postfix/postfix-files ||')
        );
        $check = array_search('sudo postfix check', $ran, true);

        $this->assertNotFalse($repair, 'nothing restores the package manifest');
        $this->assertNotFalse($check);
        $this->assertLessThan($check, $repair, 'the repair has to come before the check');

        $command = $ran[$repair];

        // Restore what is missing, keep the main.cf and master.cf just written.
        $this->assertStringContainsString('--reinstall', $command);
        $this->assertStringContainsString('--force-confmiss', $command);
        $this->assertStringContainsString('--force-confold', $command);
    }

    public function test_the_configuration_is_checked_before_anything_is_started(): void
    {
        $ran = $this->provision()->ran;

        $check = array_search('sudo doveconf -n > /dev/null', $ran, true);
        $start = array_search('sudo systemctl enable --now postfix dovecot', $ran, true);

        $this->assertNotFalse($check, 'the Dovecot configuration is never validated');
        $this->assertNotFalse($start);
        $this->assertLessThan($start, $check, 'validation must come before the start');
        $this->assertContains('sudo postfix check', $ran);
    }

    public function test_a_restart_asks_the_journal_why_it_failed(): void
    {
        // systemd says only "Job for dovecot.service failed"; the reason is in
        // the journal, and a panel that does not fetch it cannot be debugged.
        $restart = collect($this->provision()->ran)
            ->first(fn (string $c) => str_contains($c, 'systemctl restart postfix dovecot'));

        $this->assertNotNull($restart);
        $this->assertStringContainsString("journalctl -u 'postfix' -u 'dovecot'", $restart);
        $this->assertStringContainsString('exit 1', $restart);
    }

    public function test_a_failed_step_records_the_output_not_just_the_command(): void
    {
        $this->provision([
            'systemctl restart postfix dovecot' => [
                "Job for dovecot.service failed.\ndovecot: Fatal: Error in configuration file",
                1,
            ],
        ]);

        $mail = Service::where('key', 'mail')->first();

        $this->assertSame(Service::FAILED, $mail->status);
        // "Command exited 1: sudo systemctl restart …" on its own is a dead end.
        $this->assertStringContainsString('Error in configuration file', $mail->last_error);
    }
}
