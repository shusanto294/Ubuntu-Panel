<?php

namespace App\Services\Mail;

use App\Models\ActivityLog;
use App\Models\EmailAccount;
use App\Models\EmailDomain;
use App\Services\Dns\DnsManager;
use App\Services\System\HostInfo;
use App\Support\Settings;
use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use App\Services\Tasks\TaskRunner;
use RuntimeException;
use Throwable;

/**
 * Creates and removes mail domains and mailboxes on a provisioned mail server.
 */
class MailManager
{
    public function __construct(
        protected DnsManager $dns,
        protected Settings $settings,
        protected HostInfo $host,
    ) {}

    public function createDomain(EmailDomain $domain): bool
    {

        if (! $this->settings->boolean('mail_configured')) {
            $domain->update(['status' => 'failed', 'last_error' => 'The mail server is not installed on this machine.']);

            return false;
        }

        $log = ActivityLog::record([
            'user_id' => $domain->user_id,
            'type' => 'mail',
            'action' => 'mail.domain.create',
            'status' => 'running',
            'message' => $domain->domain,
        ]);

        $domain->update(['status' => 'pending']);
        $connection = app(LocalConnection::class)->timeout(600);

        try {
            $name = $domain->domain;
            $selector = $domain->dkim_selector ?: 'mail';

            $steps = [
                Step::make('Register the domain', [
                    $this->sql(sprintf(
                        "INSERT IGNORE INTO virtual_domains (name) VALUES ('%s');",
                        $this->escape($name)
                    )),
                ]),

                Step::make('Create the mailbox directory', [
                    'sudo mkdir -p '.escapeshellarg("/var/mail/vhosts/{$name}"),
                    'sudo chown -R vmail:vmail /var/mail/vhosts',
                ]),

                // Every probe below runs under sudo, including the plain
                // `test` and `grep` ones. /etc/opendkim/keys is mode 0700
                // opendkim, so the panel user cannot traverse it: an
                // unprivileged `test -f` on the private key answers "missing"
                // about a key that is there, and the domain would be issued a
                // brand new DKIM key every time it synced — invalidating the
                // TXT record already published for it.
                Step::make('Generate the DKIM key', [
                    'sudo mkdir -p '.escapeshellarg("/etc/opendkim/keys/{$name}"),
                    sprintf(
                        'sudo test -f /etc/opendkim/keys/%1$s/%2$s.private || sudo opendkim-genkey -b 2048 -d %1$s -D /etc/opendkim/keys/%1$s -s %2$s -v',
                        $name,
                        $selector
                    ),
                    sprintf('sudo chown -R opendkim:opendkim /etc/opendkim/keys/%s', $name),
                    sprintf('sudo chmod 600 /etc/opendkim/keys/%s/%s.private', $name, $selector),
                    sprintf(
                        'sudo grep -q "%1$s._domainkey.%2$s" /etc/opendkim/KeyTable || echo "%1$s._domainkey.%2$s %2$s:%1$s:/etc/opendkim/keys/%2$s/%1$s.private" | sudo tee -a /etc/opendkim/KeyTable > /dev/null',
                        $selector,
                        $name
                    ),
                    sprintf(
                        'sudo grep -q "@%2$s" /etc/opendkim/SigningTable || echo "*@%2$s %1$s._domainkey.%2$s" | sudo tee -a /etc/opendkim/SigningTable > /dev/null',
                        $selector,
                        $name
                    ),
                    sprintf(
                        'sudo grep -q "^%1$s$" /etc/opendkim/TrustedHosts || echo "%1$s" | sudo tee -a /etc/opendkim/TrustedHosts > /dev/null',
                        $name
                    ),
                    'sudo systemctl restart opendkim',
                ]),

                Step::call('Read the DKIM public key', function (LocalConnection $ssh) use ($domain, $name, $selector) {
                    $raw = $ssh->mustRun(sprintf('sudo cat /etc/opendkim/keys/%s/%s.txt', $name, $selector));
                    $value = $this->parseDkimRecord($raw);

                    $domain->update(['dkim_public_key' => $value]);

                    return "DKIM TXT value:\n".$value;
                }),

                Step::make('Reload mail services', [
                    'sudo systemctl reload postfix || sudo systemctl restart postfix',
                    'sudo systemctl restart dovecot',
                ]),
            ];

            if ($domain->manage_dns) {
                $steps[] = Step::call('Publish mail DNS records', function () use ($domain) {
                    return $this->publishDns($domain);
                });
            }

            $ok = TaskRunner::for($log, $connection)->run($steps);

            $domain->update([
                'status' => $ok ? 'active' : 'failed',
                'last_error' => $ok ? null : $log->fresh()->message,
            ]);

            return $ok;
        } catch (Throwable $e) {
            $domain->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    public function deleteDomain(EmailDomain $domain): bool
    {
        $name = $domain->domain;

        $log = ActivityLog::record([
            'user_id' => $domain->user_id,
            'type' => 'mail',
            'action' => 'mail.domain.delete',
            'status' => 'running',
            'message' => $name,
        ]);

        $domain->update(['status' => 'deleting']);
        $connection = app(LocalConnection::class)->timeout(600);

        try {
            $steps = [];

            if ($domain->manage_dns && $domain->dnsAccount && $domain->dns_record_ids) {
                $steps[] = Step::call('Remove mail DNS records', fn () => $this->dns->deleteRecords(
                    $domain->dnsAccount,
                    $domain->dns_record_ids
                ));
            }

            $steps[] = Step::make('Remove mailboxes and domain', [
                // Cascades to virtual_users and virtual_aliases.
                $this->sql(sprintf("DELETE FROM virtual_domains WHERE name='%s';", $this->escape($name))),
                'sudo rm -rf '.escapeshellarg("/var/mail/vhosts/{$name}"),
            ]);

            $steps[] = Step::make('Remove the DKIM key', [
                'sudo rm -rf '.escapeshellarg("/etc/opendkim/keys/{$name}"),
                sprintf('sudo sed -i "/%s/d" /etc/opendkim/KeyTable /etc/opendkim/SigningTable /etc/opendkim/TrustedHosts', preg_quote($name, '/')),
                'sudo systemctl restart opendkim',
            ], optional: true);

            $ok = TaskRunner::for($log, $connection)->run($steps);

            if ($ok) {
                $domain->delete();
            } else {
                $domain->update(['status' => 'failed', 'last_error' => $log->fresh()->message]);
            }

            return $ok;
        } catch (Throwable $e) {
            $domain->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    public function createAccount(EmailAccount $account, string $plainPassword): bool
    {
        $domain = $account->domain;
        $address = $account->local_part.'@'.$domain->domain;

        $log = ActivityLog::record([
            'user_id' => $account->user_id,
            'type' => 'mail',
            'action' => 'mail.account.create',
            'status' => 'running',
            'message' => $address,
        ]);

        $connection = app(LocalConnection::class)->timeout(300);

        try {
            $ok = TaskRunner::for($log, $connection)->run([
                Step::call('Hash the password', function (LocalConnection $ssh) use ($address, $plainPassword, $domain, $account) {
                    $hash = trim($ssh->mustRun('doveadm pw -s SHA512-CRYPT -p '.escapeshellarg($plainPassword)));

                    if (! str_starts_with($hash, '{SHA512-CRYPT}')) {
                        throw new RuntimeException('Unexpected doveadm output: '.$hash);
                    }

                    $ssh->mustRun($this->sql(sprintf(
                        "INSERT INTO virtual_users (domain_id, email, password, quota) ".
                        "SELECT id, '%s', '%s', %d FROM virtual_domains WHERE name='%s' ".
                        "ON DUPLICATE KEY UPDATE password=VALUES(password), quota=VALUES(quota);",
                        $this->escape($address),
                        $this->escape($hash),
                        $account->quota_mb * 1024 * 1024,
                        $this->escape($domain->domain)
                    )));

                    return "Mailbox row written for {$address}.";
                }),

                Step::make('Create the maildir', [
                    'sudo mkdir -p '.escapeshellarg("/var/mail/vhosts/{$domain->domain}/{$account->local_part}"),
                    'sudo chown -R vmail:vmail /var/mail/vhosts',
                    'sudo chmod -R 700 '.escapeshellarg("/var/mail/vhosts/{$domain->domain}"),
                ]),
            ]);

            $account->update([
                'status' => $ok ? 'active' : 'failed',
                'last_error' => $ok ? null : $log->fresh()->message,
            ]);

            return $ok;
        } catch (Throwable $e) {
            $account->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    public function deleteAccount(EmailAccount $account, bool $deleteMail = true): bool
    {
        $domain = $account->domain;
        $address = $account->local_part.'@'.$domain->domain;

        $log = ActivityLog::record([
            'user_id' => $account->user_id,
            'type' => 'mail',
            'action' => 'mail.account.delete',
            'status' => 'running',
            'message' => $address,
        ]);

        $connection = app(LocalConnection::class)->timeout(300);

        try {
            $commands = [
                $this->sql(sprintf("DELETE FROM virtual_users WHERE email='%s';", $this->escape($address))),
            ];

            if ($deleteMail) {
                $commands[] = 'sudo rm -rf '.escapeshellarg("/var/mail/vhosts/{$domain->domain}/{$account->local_part}");
            }

            $ok = TaskRunner::for($log, $connection)->run([
                Step::make('Remove the mailbox', $commands),
            ]);

            if ($ok) {
                $account->delete();
            } else {
                $account->update(['status' => 'failed', 'last_error' => $log->fresh()->message]);
            }

            return $ok;
        } catch (Throwable $e) {
            $account->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    /** MX, SPF, DKIM, DMARC and the mail host A record. */
    public function publishDns(EmailDomain $domain): string
    {
        $account = $domain->dnsAccount;

        if (! $account) {
            throw new RuntimeException('No DNS provider attached to this mail domain.');
        }

        $mailHost = $this->settings->get('mail_hostname') ?: 'mail.'.$domain->domain;
        $selector = $domain->dkim_selector ?: 'mail';
        $ip = $this->host->publicIp();

        if (! $ip) {
            // Without an address the A record would be published empty, which
            // is worse than not publishing it: mail would resolve nowhere.
            throw new RuntimeException(
                'Could not work out this machine\'s public address, so the mail records cannot be written.'
            );
        }

        $records = [
            ['type' => 'A', 'name' => $mailHost, 'content' => $ip, 'proxied' => false],
            ['type' => 'MX', 'name' => $domain->domain, 'content' => $mailHost, 'priority' => 10],
            ['type' => 'TXT', 'name' => $domain->domain, 'content' => 'v=spf1 mx a:'.$mailHost.' ~all'],
            [
                'type' => 'TXT',
                'name' => '_dmarc.'.$domain->domain,
                'content' => 'v=DMARC1; p=quarantine; rua=mailto:postmaster@'.$domain->domain,
            ],
        ];

        if ($domain->dkim_public_key) {
            $records[] = [
                'type' => 'TXT',
                'name' => $selector.'._domainkey.'.$domain->domain,
                'content' => $domain->dkim_public_key,
            ];
        }

        $result = $this->dns->writeRecords($account, $domain->domain, $records);

        $domain->update(['dns_record_ids' => $result['ids']]);

        return $result['log'];
    }

    /** Run SQL against the mail database using socket auth as root. */
    protected function sql(string $statement): string
    {
        return sprintf(
            'sudo mysql %s -e %s',
            MailServerProvisioner::DB_NAME,
            escapeshellarg($statement)
        );
    }

    protected function escape(string $value): string
    {
        return str_replace(["\\", "'"], ["\\\\", "\\'"], $value);
    }

    /**
     * opendkim-genkey writes a BIND-format TXT record split across quoted chunks.
     * Cloudflare wants the joined value.
     */
    protected function parseDkimRecord(string $raw): string
    {
        preg_match_all('/"([^"]*)"/', $raw, $matches);

        $value = implode('', $matches[1] ?? []);

        if ($value === '') {
            throw new RuntimeException('Could not parse the generated DKIM record: '.$raw);
        }

        return $value;
    }
}
