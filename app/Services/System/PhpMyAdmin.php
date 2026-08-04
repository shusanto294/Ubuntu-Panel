<?php

namespace App\Services\System;

use App\Models\Database;
use App\Support\Settings;

/**
 * phpMyAdmin, signed in for you.
 *
 * Installed from upstream rather than apt, because Debian's package drags in
 * a web server and wants to configure it, and the panel already has one.
 *
 * Access works through phpMyAdmin's `signon` authentication: it reads the
 * credentials out of a PHP session written by something it trusts, rather than
 * showing a login form. The panel writes that session when you click through
 * from a database, using that database's own user — so you land in the right
 * place already logged in, and phpMyAdmin refuses everyone who has not come
 * through the panel, because without a signon session there is nothing to log
 * in with and no form to type into.
 */
class PhpMyAdmin
{
    public const ROOT = '/usr/share/ubuntu-panel/phpmyadmin';

    public const TEMP = '/var/lib/ubuntu-panel/phpmyadmin';

    public const PATH = '/phpmyadmin';

    /** The session phpMyAdmin reads credentials out of. */
    public const SIGNON_SESSION = 'UbuntuPanelSignon';

    public function __construct(protected Settings $settings) {}

    /** Is it on the machine? */
    public function isInstalled(): bool
    {
        return is_file(self::ROOT.'/index.php');
    }

    /**
     * Hand phpMyAdmin the credentials for one database, and say where to go.
     *
     * Writing a native PHP session from inside a request that has its own
     * (database-backed) Laravel session is safe because the two do not share a
     * handler — but the name has to be swapped back afterwards, or anything
     * later in the request that touches native sessions gets the wrong one.
     */
    public function signOn(Database $database): string
    {
        $previous = session_name(self::SIGNON_SESSION);

        if (session_status() === PHP_SESSION_ACTIVE) {
            session_write_close();
        }

        session_start();

        $_SESSION['PMA_single_signon_user'] = (string) $database->username;
        $_SESSION['PMA_single_signon_password'] = (string) $database->password;
        $_SESSION['PMA_single_signon_host'] = '127.0.0.1';
        $_SESSION['PMA_single_signon_port'] = (string) $database->defaultPort();

        session_write_close();
        session_name($previous);

        // Land on the database itself rather than the server overview.
        return self::PATH.'/index.php?db='.rawurlencode($database->name);
    }

    /**
     * phpMyAdmin's own configuration.
     *
     * The blowfish secret is generated once and kept: it encrypts what
     * phpMyAdmin stores in its cookies, so changing it logs everyone out and
     * changing it on every provision run would do that on every provision run.
     */
    public function config(): string
    {
        $secret = $this->settings->rememberSecret(
            'phpmyadmin_blowfish',
            fn () => bin2hex(random_bytes(16)),
        );

        $session = self::SIGNON_SESSION;
        $temp = self::TEMP;
        $path = self::PATH;

        return <<<PHP
        <?php
        // Managed by Ubuntu Panel. Edited by hand, this is overwritten on the
        // next provision run.

        declare(strict_types=1);

        \$cfg['blowfish_secret'] = '{$secret}';
        \$cfg['TempDir'] = '{$temp}';

        // No login form: the panel puts the credentials in a session and sends
        // you here. Arriving without one gets you nothing, which is the point —
        // this is reachable on the panel's own port.
        \$i = 1;
        \$cfg['Servers'][\$i]['auth_type'] = 'signon';
        \$cfg['Servers'][\$i]['SignonSession'] = '{$session}';
        \$cfg['Servers'][\$i]['SignonURL'] = '/databases';
        \$cfg['Servers'][\$i]['LogoutURL'] = '/databases';
        \$cfg['Servers'][\$i]['host'] = '127.0.0.1';
        \$cfg['Servers'][\$i]['compress'] = false;
        \$cfg['Servers'][\$i]['AllowNoPassword'] = false;

        \$cfg['ShowServerInfo'] = false;
        \$cfg['CheckConfigurationPermissions'] = false;
        \$cfg['PmaAbsoluteUri'] = '{$path}/';
        PHP;
    }

    /** Where nginx serves it from, and with which PHP. */
    public function nginxLocation(): string
    {
        $root = self::ROOT;
        $path = self::PATH;
        $socket = sprintf('/run/php/php%d.%d-fpm.sock', PHP_MAJOR_VERSION, PHP_MINOR_VERSION);

        return <<<NGINX
        # phpMyAdmin, on the panel's own origin so its session cookie is ours.
        location ^~ {$path} {
            alias {$root}/;
            index index.php;

            try_files \$uri \$uri/ =404;

            location ~ \.php\$ {
                include snippets/fastcgi-php.conf;
                fastcgi_pass unix:{$socket};
                # With `alias`, this is the resolved path; SCRIPT_FILENAME built
                # from document_root would point at the panel's own public/.
                fastcgi_param SCRIPT_FILENAME \$request_filename;
                fastcgi_read_timeout 300;
            }

            # Nothing under here should ever be fetched directly.
            location ~ ^{$path}/(libraries|templates|setup)/ {
                deny all;
            }
        }
        NGINX;
    }
}
