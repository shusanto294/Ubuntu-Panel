<?php

namespace App\Services\System;

use App\Services\Shell\LocalConnection;
use App\Support\Settings;
use Illuminate\Support\Str;

/**
 * phpMyAdmin, signed in for you — and a login form for everyone else.
 *
 * Installed from upstream rather than apt, because Debian's package drags in a
 * web server and wants to configure it, and the panel already has one.
 *
 * Two ways in, which is two phpMyAdmin "servers" pointed at the same MariaDB:
 *
 *   1. cookie — the ordinary login form, and the default. Sign in with a
 *      database's own username and password and you get that database and
 *      nothing else, because that is all its user is granted.
 *   2. signon — no form. The panel writes the credentials into a PHP session
 *      phpMyAdmin trusts and sends you straight in. That is what the button on
 *      the Databases page does, and it signs you in as an account with
 *      privileges over everything, so one trip manages the lot.
 *
 * That privileged account is the panel's own, not MariaDB's `root`. Root on
 * Ubuntu authenticates over a unix socket and has no password to hand to
 * phpMyAdmin at all; a dedicated user with the same reach is something the
 * panel can create, rotate and hold the password for without touching how the
 * machine's own administration works.
 */
class PhpMyAdmin
{
    public const ROOT = '/usr/share/ubuntu-panel/phpmyadmin';

    public const TEMP = '/var/lib/ubuntu-panel/phpmyadmin';

    public const PATH = '/phpmyadmin';

    /** The session phpMyAdmin reads credentials out of. */
    public const SIGNON_SESSION = 'UbuntuPanelSignon';

    /**
     * The MariaDB account the panel signs administrators in as.
     *
     * Everything on the server, with GRANT — the reach of `root` without being
     * root, which on Ubuntu authenticates over a socket and has no password to
     * give phpMyAdmin in the first place.
     */
    public const ADMIN_USER = 'ubuntu_panel_admin';

    /** phpMyAdmin's server number for the signon route; 1 is the login form. */
    public const SIGNON_SERVER = 2;

    public function __construct(protected Settings $settings) {}

    /** Generated once and kept: it is a live MariaDB account. */
    public function adminPassword(): string
    {
        return $this->settings->rememberSecret(
            'phpmyadmin_admin_password',
            fn () => Str::password(32, symbols: false),
        );
    }

    /**
     * Create (or re-assert) that account. Safe to run repeatedly, and run on
     * demand as well as at install time so a machine that installed phpMyAdmin
     * before this existed does not need the service reinstalling.
     */
    public function ensureAdminUser(LocalConnection $ssh): void
    {
        $password = $this->adminPassword();

        $ssh->mustRun(sprintf(
            'sudo mysql -e "CREATE USER IF NOT EXISTS \'%1$s\'@\'localhost\' IDENTIFIED BY \'%2$s\'; '.
            'ALTER USER \'%1$s\'@\'localhost\' IDENTIFIED BY \'%2$s\'; '.
            'GRANT ALL PRIVILEGES ON *.* TO \'%1$s\'@\'localhost\' WITH GRANT OPTION; FLUSH PRIVILEGES;"',
            self::ADMIN_USER,
            $password
        ));
    }

    /** Is it on the machine? */
    public function isInstalled(): bool
    {
        return is_file(self::ROOT.'/index.php');
    }

    /** Signed in over everything, for the button on the Databases page. */
    public function signOnAsAdmin(): string
    {
        $this->writeSignonSession(self::ADMIN_USER, $this->adminPassword(), 3306);

        return self::PATH.'/index.php?server='.self::SIGNON_SERVER;
    }

    protected function writeSignonSession(string $user, string $password, int $port): void
    {
        $previous = session_name(self::SIGNON_SESSION);

        if (session_status() === PHP_SESSION_ACTIVE) {
            session_write_close();
        }

        session_start();

        $_SESSION['PMA_single_signon_user'] = $user;
        $_SESSION['PMA_single_signon_password'] = $password;
        $_SESSION['PMA_single_signon_host'] = '127.0.0.1';
        $_SESSION['PMA_single_signon_port'] = (string) $port;

        session_write_close();
        session_name($previous);
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

        // Server 1 is the login form, and the default — so anyone arriving at
        // /phpmyadmin without having come through the panel is asked who they
        // are. A database's own credentials get that database and nothing
        // else, because that is all its MariaDB user is granted.
        \$i = 1;
        \$cfg['Servers'][\$i]['auth_type'] = 'cookie';
        \$cfg['Servers'][\$i]['verbose'] = 'MariaDB';
        \$cfg['Servers'][\$i]['host'] = '127.0.0.1';
        \$cfg['Servers'][\$i]['compress'] = false;
        \$cfg['Servers'][\$i]['AllowNoPassword'] = false;

        // Server 2 is the panel's own way in: no form, because the panel has
        // already put the credentials in a session phpMyAdmin trusts. Nothing
        // reaches it without that session, so it is not a second front door.
        \$i = 2;
        \$cfg['Servers'][\$i]['auth_type'] = 'signon';
        \$cfg['Servers'][\$i]['verbose'] = 'MariaDB (Ubuntu Panel)';
        \$cfg['Servers'][\$i]['SignonSession'] = '{$session}';
        \$cfg['Servers'][\$i]['SignonURL'] = '/databases';
        \$cfg['Servers'][\$i]['LogoutURL'] = '/databases';
        \$cfg['Servers'][\$i]['host'] = '127.0.0.1';
        \$cfg['Servers'][\$i]['compress'] = false;
        \$cfg['Servers'][\$i]['AllowNoPassword'] = false;

        \$cfg['ServerDefault'] = 1;

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
