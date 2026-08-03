<?php

namespace App\Services\Mail;

use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use Illuminate\Support\Str;

/**
 * Postfix + Dovecot + OpenDKIM with virtual mailboxes stored in MariaDB.
 *
 * Mailboxes live in /var/mail/vhosts/<domain>/<user> and are owned by the
 * `vmail` user (uid/gid 5000), the layout Dovecot's virtual-user docs use.
 */
class MailServerProvisioner
{
    public const DB_NAME = 'mailserver';

    public const DB_USER = 'mailuser';

    /** The mail hostname, defaulting to mail.<server host>. */
    public function hostname(): string
    {
        if (filled($server->mail_hostname)) {
            return $server->mail_hostname;
        }

        $hostname = 'mail.'.parse_url('http://'.$server->host, PHP_URL_HOST);

        $server->update(['mail_hostname' => $hostname]);

        return $hostname;
    }

    /**
     * Everything after the packages are installed. The apt install itself is
     * handled by the catalogue so it can share one transaction with the rest.
     *
     * @return array<int, Step>
     */
    public function configureSteps(): array
    {
        $password = $this->ensureDbPassword($server);
        $hostname = $this->hostname($server);

        return [
            Step::make('Create the mail database', [
                sprintf(
                    'sudo mysql -e "CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"',
                    self::DB_NAME
                ),
                sprintf(
                    'sudo mysql -e "CREATE USER IF NOT EXISTS \'%s\'@\'localhost\' IDENTIFIED BY \'%s\'; ALTER USER \'%1$s\'@\'localhost\' IDENTIFIED BY \'%2$s\'; GRANT SELECT, INSERT, UPDATE, DELETE ON %3$s.* TO \'%1$s\'@\'localhost\'; FLUSH PRIVILEGES;"',
                    self::DB_USER,
                    $password,
                    self::DB_NAME
                ),
                sprintf('sudo mysql %s -e %s', self::DB_NAME, escapeshellarg($this->schemaSql())),
            ]),

            Step::make('Create the vmail user', [
                'getent group vmail >/dev/null || sudo groupadd -g 5000 vmail',
                'id vmail >/dev/null 2>&1 || sudo useradd -g vmail -u 5000 vmail -d /var/mail/vhosts -m',
                'sudo mkdir -p /var/mail/vhosts',
                'sudo chown -R vmail:vmail /var/mail/vhosts',
            ]),

            Step::call('Write Postfix configuration', function (LocalConnection $ssh) use ($password, $hostname) {
                foreach ($this->postfixMysqlMaps($password) as $path => $contents) {
                    $ssh->putFile($path, $contents);
                }

                $ssh->mustRun('sudo chmod 640 /etc/postfix/mysql-*.cf && sudo chgrp postfix /etc/postfix/mysql-*.cf');
                $ssh->putFile('/etc/postfix/main.cf', $this->postfixMainCf($hostname));
                $ssh->putFile('/etc/postfix/master.cf', $this->postfixMasterCf());

                return 'Postfix main.cf, master.cf and MySQL maps written.';
            }),

            Step::call('Write Dovecot configuration', function (LocalConnection $ssh) use ($password) {
                foreach ($this->dovecotFiles($password) as $path => $contents) {
                    $ssh->putFile($path, $contents);
                }

                $ssh->mustRun('sudo chown -R vmail:dovecot /etc/dovecot && sudo chmod -R o-rwx /etc/dovecot');

                return 'Dovecot configured for MySQL virtual users.';
            }),

            Step::make('Configure OpenDKIM', [
                'sudo mkdir -p /etc/opendkim/keys',
                'sudo touch /etc/opendkim/KeyTable /etc/opendkim/SigningTable /etc/opendkim/TrustedHosts',
                'grep -q "127.0.0.1" /etc/opendkim/TrustedHosts || printf "127.0.0.1\nlocalhost\n::1\n" | sudo tee /etc/opendkim/TrustedHosts > /dev/null',
                'sudo chown -R opendkim:opendkim /etc/opendkim',
                'sudo chmod -R go-rwx /etc/opendkim/keys',
            ]),

            Step::call('Wire OpenDKIM into Postfix', function (LocalConnection $ssh) {
                $ssh->putFile('/etc/opendkim.conf', $this->openDkimConf());
                $ssh->putFile('/etc/default/opendkim', "RUNDIR=/run/opendkim\nSOCKET=inet:8891@localhost\nUSER=opendkim\nGROUP=opendkim\nPIDFILE=\$RUNDIR/\$NAME.pid\n");

                return 'OpenDKIM listening on localhost:8891.';
            }),

            Step::make('Start mail services', [
                'sudo systemctl enable --now opendkim',
                'sudo systemctl restart opendkim',
                'sudo systemctl enable --now postfix dovecot',
                'sudo systemctl restart postfix dovecot',
                'sudo systemctl is-active postfix dovecot opendkim',
            ]),

            Step::make('Open mail ports', [
                'sudo ufw allow 25/tcp',
                'sudo ufw allow 465/tcp',
                'sudo ufw allow 587/tcp',
                'sudo ufw allow 993/tcp',
                'sudo ufw allow 995/tcp',
            ], optional: true),
        ];
    }

    public function ensureDbPassword(): string
    {
        if (blank($server->mail_db_password)) {
            $server->mail_db_password = Str::password(32, symbols: false);
            $server->save();
        }

        return (string) $server->mail_db_password;
    }

    protected function schemaSql(): string
    {
        return <<<'SQL'
        CREATE TABLE IF NOT EXISTS virtual_domains (
          id INT NOT NULL AUTO_INCREMENT,
          name VARCHAR(191) NOT NULL,
          PRIMARY KEY (id), UNIQUE KEY name (name)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

        CREATE TABLE IF NOT EXISTS virtual_users (
          id INT NOT NULL AUTO_INCREMENT,
          domain_id INT NOT NULL,
          email VARCHAR(191) NOT NULL,
          password VARCHAR(191) NOT NULL,
          quota BIGINT NOT NULL DEFAULT 0,
          PRIMARY KEY (id), UNIQUE KEY email (email),
          FOREIGN KEY (domain_id) REFERENCES virtual_domains(id) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

        CREATE TABLE IF NOT EXISTS virtual_aliases (
          id INT NOT NULL AUTO_INCREMENT,
          domain_id INT NOT NULL,
          source VARCHAR(191) NOT NULL,
          destination VARCHAR(191) NOT NULL,
          PRIMARY KEY (id),
          FOREIGN KEY (domain_id) REFERENCES virtual_domains(id) ON DELETE CASCADE
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
        SQL;
    }

    /**
     * @return array<string, string>
     */
    protected function postfixMysqlMaps(string $password): array
    {
        $credentials = sprintf(
            "user = %s\npassword = %s\nhosts = 127.0.0.1\ndbname = %s\n",
            self::DB_USER,
            $password,
            self::DB_NAME
        );

        return [
            '/etc/postfix/mysql-virtual-mailbox-domains.cf' => $credentials.
                "query = SELECT 1 FROM virtual_domains WHERE name='%s'\n",

            '/etc/postfix/mysql-virtual-mailbox-maps.cf' => $credentials.
                "query = SELECT 1 FROM virtual_users WHERE email='%s'\n",

            '/etc/postfix/mysql-virtual-alias-maps.cf' => $credentials.
                "query = SELECT destination FROM virtual_aliases WHERE source='%s'\n",

            '/etc/postfix/mysql-email2email.cf' => $credentials.
                "query = SELECT email FROM virtual_users WHERE email='%s'\n",
        ];
    }

    protected function postfixMainCf(string $hostname): string
    {
        return <<<CONF
        # Managed by Ubuntu Panel
        smtpd_banner = \$myhostname ESMTP
        biff = no
        append_dot_mydomain = no
        readme_directory = no
        compatibility_level = 3.6

        myhostname = {$hostname}
        myorigin = /etc/mailname
        mydestination = localhost
        mynetworks = 127.0.0.0/8 [::ffff:127.0.0.0]/104 [::1]/128
        inet_interfaces = all
        inet_protocols = all
        message_size_limit = 52428800
        mailbox_size_limit = 0
        recipient_delimiter = +

        # TLS - certbot certificates are swapped in per domain when available
        smtpd_tls_cert_file = /etc/ssl/certs/ssl-cert-snakeoil.pem
        smtpd_tls_key_file = /etc/ssl/private/ssl-cert-snakeoil.key
        smtpd_use_tls = yes
        smtpd_tls_security_level = may
        smtp_tls_security_level = may
        smtpd_tls_auth_only = yes
        smtpd_tls_session_cache_database = btree:\${data_directory}/smtpd_scache
        smtp_tls_session_cache_database = btree:\${data_directory}/smtp_scache

        # SASL via Dovecot
        smtpd_sasl_type = dovecot
        smtpd_sasl_path = private/auth
        smtpd_sasl_auth_enable = yes
        smtpd_relay_restrictions = permit_mynetworks permit_sasl_authenticated defer_unauth_destination
        smtpd_recipient_restrictions = permit_mynetworks permit_sasl_authenticated reject_unauth_destination

        # Virtual mailboxes in MySQL, delivered over LMTP to Dovecot
        virtual_transport = lmtp:unix:private/dovecot-lmtp
        virtual_mailbox_domains = mysql:/etc/postfix/mysql-virtual-mailbox-domains.cf
        virtual_mailbox_maps = mysql:/etc/postfix/mysql-virtual-mailbox-maps.cf
        virtual_alias_maps = mysql:/etc/postfix/mysql-virtual-alias-maps.cf, mysql:/etc/postfix/mysql-email2email.cf

        # DKIM signing
        milter_protocol = 6
        milter_default_action = accept
        smtpd_milters = inet:localhost:8891
        non_smtpd_milters = inet:localhost:8891
        CONF;
    }

    protected function postfixMasterCf(): string
    {
        return <<<'CONF'
        # Managed by Ubuntu Panel
        smtp      inet  n       -       y       -       -       smtpd
        submission inet n       -       y       -       -       smtpd
          -o syslog_name=postfix/submission
          -o smtpd_tls_security_level=encrypt
          -o smtpd_sasl_auth_enable=yes
          -o smtpd_client_restrictions=permit_sasl_authenticated,reject
        smtps     inet  n       -       y       -       -       smtpd
          -o syslog_name=postfix/smtps
          -o smtpd_tls_wrappermode=yes
          -o smtpd_sasl_auth_enable=yes
          -o smtpd_client_restrictions=permit_sasl_authenticated,reject
        pickup    unix  n       -       y       60      1       pickup
        cleanup   unix  n       -       y       -       0       cleanup
        qmgr      unix  n       -       n       300     1       qmgr
        tlsmgr    unix  -       -       y       1000?   1       tlsmgr
        rewrite   unix  -       -       y       -       -       trivial-rewrite
        bounce    unix  -       -       y       -       0       bounce
        defer     unix  -       -       y       -       0       bounce
        trace     unix  -       -       y       -       0       bounce
        verify    unix  -       -       y       -       1       verify
        flush     unix  n       -       y       1000?   0       flush
        proxymap  unix  -       -       n       -       -       proxymap
        proxywrite unix -       -       n       -       1       proxymap
        smtp      unix  -       -       y       -       -       smtp
        relay     unix  -       -       y       -       -       smtp
        showq     unix  n       -       y       -       -       showq
        error     unix  -       -       y       -       -       error
        retry     unix  -       -       y       -       -       error
        discard   unix  -       -       y       -       -       discard
        local     unix  -       n       n       -       -       local
        virtual   unix  -       n       n       -       -       virtual
        lmtp      unix  -       -       y       -       -       lmtp
        anvil     unix  -       -       y       -       1       anvil
        scache    unix  -       -       y       -       1       scache
        postlog   unix-dgram n  -       n       -       1       postlogd
        CONF;
    }

    /**
     * @return array<string, string>
     */
    protected function dovecotFiles(string $password): array
    {
        return [
            '/etc/dovecot/dovecot.conf' => <<<'CONF'
            # Managed by Ubuntu Panel
            protocols = imap pop3 lmtp
            listen = *, ::
            base_dir = /var/run/dovecot/
            !include conf.d/*.conf
            CONF,

            '/etc/dovecot/conf.d/10-mail.conf' => <<<'CONF'
            mail_location = maildir:/var/mail/vhosts/%d/%n
            mail_privileged_group = mail
            namespace inbox {
              inbox = yes
              mailbox Drafts { special_use = \Drafts; auto = subscribe }
              mailbox Junk   { special_use = \Junk;   auto = subscribe }
              mailbox Sent   { special_use = \Sent;   auto = subscribe }
              mailbox Trash  { special_use = \Trash;  auto = subscribe }
            }
            first_valid_uid = 5000
            last_valid_uid = 5000
            CONF,

            '/etc/dovecot/conf.d/10-auth.conf' => <<<'CONF'
            disable_plaintext_auth = yes
            auth_mechanisms = plain login
            !include auth-sql.conf.ext
            CONF,

            '/etc/dovecot/conf.d/auth-sql.conf.ext' => <<<'CONF'
            passdb {
              driver = sql
              args = /etc/dovecot/dovecot-sql.conf.ext
            }
            userdb {
              driver = static
              args = uid=vmail gid=vmail home=/var/mail/vhosts/%d/%n
            }
            CONF,

            '/etc/dovecot/dovecot-sql.conf.ext' => sprintf(
                "driver = mysql\nconnect = host=127.0.0.1 dbname=%s user=%s password=%s\n".
                "default_pass_scheme = SHA512-CRYPT\n".
                "password_query = SELECT email as user, password FROM virtual_users WHERE email='%%u'\n",
                self::DB_NAME,
                self::DB_USER,
                $password
            ),

            '/etc/dovecot/conf.d/10-master.conf' => <<<'CONF'
            service imap-login {
              inet_listener imap  { port = 143 }
              inet_listener imaps { port = 993; ssl = yes }
            }
            service pop3-login {
              inet_listener pop3  { port = 110 }
              inet_listener pop3s { port = 995; ssl = yes }
            }
            service lmtp {
              unix_listener /var/spool/postfix/private/dovecot-lmtp {
                mode = 0600
                user = postfix
                group = postfix
              }
            }
            service auth {
              unix_listener /var/spool/postfix/private/auth {
                mode = 0666
                user = postfix
                group = postfix
              }
              unix_listener auth-userdb {
                mode = 0600
                user = vmail
              }
              user = dovecot
            }
            service auth-worker {
              user = vmail
            }
            CONF,

            '/etc/dovecot/conf.d/10-ssl.conf' => <<<'CONF'
            ssl = yes
            ssl_cert = </etc/ssl/certs/ssl-cert-snakeoil.pem
            ssl_key = </etc/ssl/private/ssl-cert-snakeoil.key
            ssl_min_protocol = TLSv1.2
            ssl_prefer_server_ciphers = yes
            CONF,
        ];
    }

    protected function openDkimConf(): string
    {
        return <<<'CONF'
        # Managed by Ubuntu Panel
        Syslog                  yes
        UMask                   007
        Canonicalization        relaxed/simple
        Mode                    sv
        SubDomains              no
        AutoRestart             yes
        AutoRestartRate         10/1M
        Background              yes
        DNSTimeout              5
        SignatureAlgorithm      rsa-sha256
        UserID                  opendkim:opendkim
        Socket                  inet:8891@localhost
        PidFile                 /run/opendkim/opendkim.pid
        KeyTable                refile:/etc/opendkim/KeyTable
        SigningTable            refile:/etc/opendkim/SigningTable
        ExternalIgnoreList      refile:/etc/opendkim/TrustedHosts
        InternalHosts           refile:/etc/opendkim/TrustedHosts
        CONF;
    }
}
