<?php

namespace App\Services\Mail;

use App\Models\EmailDomain;
use App\Services\Shell\LocalConnection;

/**
 * Points Postfix and Dovecot at the per-domain certificates.
 *
 * A mail domain gets a Let's Encrypt certificate for `mail.<domain>` so its
 * webmail has one. The same certificate is what an application connecting to
 * `mail.<domain>:587` needs to see, and until this existed it saw the snakeoil
 * pair instead — so every client that verifies certificates, which is every
 * client worth using, refused the connection to a hostname the panel had just
 * told the user to use.
 *
 * Both daemons pick per-name certificates by SNI: Postfix from a hashed map,
 * Dovecot from `local_name` blocks. The map is rebuilt from what is actually on
 * disk rather than from what the database thinks was issued — a certificate
 * named in either file and missing from the filesystem stops the daemon dead,
 * and the database has no way of knowing that certbot's renewal removed one.
 */
class MailCertificates
{
    public const POSTFIX_MAP = '/etc/postfix/vmail_ssl.map';

    public const DOVECOT_SNI = '/etc/dovecot/panel-sni.conf';

    /**
     * Rebuild both files and reload both daemons.
     *
     * @return string what the run did, for the task log
     */
    public function sync(LocalConnection $ssh): string
    {
        $hosts = [];

        foreach (EmailDomain::all() as $domain) {
            $host = Roundcube::hostFor($domain);
            $live = Roundcube::certificatePath($domain);

            [, $code] = $ssh->run('sudo test -f '.escapeshellarg($live.'/fullchain.pem'));

            if ($code === 0) {
                $hosts[$host] = $live;
            }
        }

        $ssh->putFile(self::POSTFIX_MAP, $this->postfixMap($hosts));
        $ssh->putFile(self::DOVECOT_SNI, $this->dovecotSni($hosts));

        // `postmap -F` stores the file *contents* keyed by name, which is what
        // lets Postfix read a key it has no permission to open at request time.
        $ssh->mustRun('sudo postmap -F hash:'.escapeshellarg(self::POSTFIX_MAP));
        $ssh->mustRun('sudo postconf -e '.escapeshellarg('tls_server_sni_maps=hash:'.self::POSTFIX_MAP));

        $ssh->mustRun('sudo chown root:dovecot '.escapeshellarg(self::DOVECOT_SNI));
        $ssh->mustRun('sudo chmod 640 '.escapeshellarg(self::DOVECOT_SNI));

        // Dovecot reads the key as root before dropping privileges, but it
        // cannot traverse /etc/letsencrypt/live at all unless told to.
        $ssh->run('sudo chmod 755 /etc/letsencrypt/live /etc/letsencrypt/archive');

        $ssh->mustRun('sudo postfix check');
        $ssh->mustRun('sudo doveconf -n > /dev/null');

        $ssh->run('sudo systemctl reload postfix');
        $ssh->run('sudo systemctl reload dovecot');

        if ($hosts === []) {
            return 'No mail certificates yet; Postfix and Dovecot stay on the self-signed pair.';
        }

        return 'SMTP and IMAP now present the real certificate for: '.implode(', ', array_keys($hosts));
    }

    /**
     * @param  array<string, string>  $hosts  hostname => live directory
     */
    protected function postfixMap(array $hosts): string
    {
        $lines = ['# Managed by Ubuntu Panel'];

        foreach ($hosts as $host => $live) {
            $lines[] = sprintf('%s %s/privkey.pem %s/fullchain.pem', $host, $live, $live);
        }

        return implode("\n", $lines)."\n";
    }

    /**
     * @param  array<string, string>  $hosts  hostname => live directory
     */
    protected function dovecotSni(array $hosts): string
    {
        $blocks = ['# Managed by Ubuntu Panel'];

        foreach ($hosts as $host => $live) {
            // Dovecot's parser wants `{` last on its line and one setting per
            // line; the compact form is a fatal "Garbage after '{'".
            $blocks[] = <<<CONF
            local_name {$host} {
              ssl_cert = <{$live}/fullchain.pem
              ssl_key = <{$live}/privkey.pem
            }
            CONF;
        }

        return implode("\n\n", $blocks)."\n";
    }
}
