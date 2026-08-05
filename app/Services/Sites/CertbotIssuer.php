<?php

namespace App\Services\Sites;

use App\Models\DnsAccount;
use App\Services\Shell\LocalConnection;

/**
 * Asks certbot for a certificate, without letting it touch our vhosts.
 *
 * `--nginx` would rewrite the managed file and the next deploy would undo it,
 * so this only ever obtains the certificate; putting it to use is the caller's
 * job.
 *
 * When the name's DNS is on Cloudflare we validate over DNS-01, which works
 * even with the orange cloud on; an HTTP-01 challenge would be answered by
 * Cloudflare rather than by the origin. That is a Cloudflare-shaped problem and
 * gets a Cloudflare-shaped answer — every other provider serves the origin
 * directly, so the webroot challenge is both simpler and enough.
 */
class CertbotIssuer
{
    /**
     * @param  array<int, string>  $hostnames
     * @return array{issued: bool, output: string}
     */
    public function issue(
        LocalConnection $ssh,
        array $hostnames,
        string $email,
        ?DnsAccount $account = null,
    ): array {
        $domains = implode(' ', array_map(fn ($d) => '-d '.escapeshellarg($d), $hostnames));

        $common = sprintf(
            'certonly --non-interactive --agree-tos -m %s %s --keep-until-expiring '.
            '--deploy-hook "systemctl reload nginx"',
            escapeshellarg($email),
            $domains
        );

        if ($account?->provider === 'cloudflare') {
            $ssh->run('sudo DEBIAN_FRONTEND=noninteractive apt-get install -y python3-certbot-dns-cloudflare');

            $ssh->mustRun('sudo mkdir -p /etc/letsencrypt/panel && sudo chmod 700 /etc/letsencrypt/panel');
            $ssh->putFile(
                '/etc/letsencrypt/panel/cloudflare.ini',
                'dns_cloudflare_api_token = '.$account->api_token
            );
            $ssh->mustRun('sudo chmod 600 /etc/letsencrypt/panel/cloudflare.ini');

            [$output, $code] = $ssh->run(
                'sudo certbot '.$common.
                ' --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/panel/cloudflare.ini'.
                ' --dns-cloudflare-propagation-seconds 30'
            );

            return [
                'issued' => $code === 0,
                'output' => $code === 0
                    ? "Certificate issued via Cloudflare DNS validation.\n".$output
                    : "DNS validation failed.\n".$output,
            ];
        }

        [$output, $code] = $ssh->run(
            'sudo certbot '.$common.' --webroot -w '.escapeshellarg(NginxVhost::ACME_WEBROOT)
        );

        return [
            'issued' => $code === 0,
            'output' => $code === 0 ? "Certificate issued.\n".$output : "certbot failed.\n".$output,
        ];
    }
}
