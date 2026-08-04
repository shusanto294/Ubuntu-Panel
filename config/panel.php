<?php

return [

    /*
    | Where this panel came from and what version it is. The update check
    | compares the installed commit against the tip of `update_branch`, so
    | pushing to that branch is how an update ships.
    |
    | Bump `version` in the same commit as every push to that branch — see
    | "Publishing an update" in the README.
    */
    'version' => '1.3.3',
    'repository' => env('PANEL_REPOSITORY', 'https://github.com/shusanto294/Ubuntu-Panel'),
    'update_branch' => env('PANEL_UPDATE_BRANCH', 'main'),
    'system_user' => env('PANEL_SYSTEM_USER', 'ubuntupanel'),

    /*
    | PHP versions the panel can install and assign to sites.
    */
    'php_versions' => ['8.5', '8.4', '8.3', '8.2', '8.1'],

    /*
    | Node.js major versions offered when provisioning.
    */
    'node_versions' => ['22', '20', '18'],

    /*
    | Where site directories are created on the remote host.
    */
    'sites_root' => env('PANEL_SITES_ROOT', '/var/www'),

    /*
    | Cloudflare DNS record types offered when creating a site.
    */
    'dns_types' => ['A', 'AAAA', 'CNAME'],

    /*
    | Port range handed out to node-family apps behind the nginx reverse proxy.
    */
    'app_port_range' => [3000, 3999],

    /*
    | Everything the panel can install on a server. Array order is install order.
    |
    | core     — the web stack; without it the server cannot host anything
    | default  — pre-ticked when adding a server
    | requires — pulled in automatically when the service is selected
    | detect   — shell test that decides whether the service is already present
    | version  — command whose first line is shown next to the service
    */
    'services' => [
        'base' => [
            'label' => 'Base packages',
            'group' => 'core',
            'description' => 'curl, git, unzip, gnupg, ufw and the apt repositories the rest depends on.',
            'core' => true,
            'default' => true,
            'detect' => 'command -v curl && command -v git && command -v unzip && command -v gpg',
            'version' => 'lsb_release -ds 2>/dev/null || . /etc/os-release && echo "$PRETTY_NAME"',
        ],
        'nginx' => [
            'label' => 'nginx',
            'group' => 'core',
            'description' => 'Web server. Every site gets a vhost here.',
            'core' => true,
            'default' => true,
            'requires' => ['base'],
            'detect' => 'command -v nginx',
            'version' => 'nginx -v 2>&1',
        ],
        'php' => [
            'label' => 'PHP-FPM',
            'group' => 'core',
            'description' => 'PHP-FPM with the extensions WordPress and Laravel need.',
            'core' => true,
            'default' => true,
            'requires' => ['base'],
            'detect' => 'command -v php',
            'version' => 'php -v 2>/dev/null | head -1',
        ],
        'composer' => [
            'label' => 'Composer',
            'group' => 'core',
            'description' => 'PHP dependency manager, needed for Laravel deployments.',
            'core' => true,
            'default' => true,
            'requires' => ['php'],
            'detect' => 'command -v composer',
            'version' => 'composer --version 2>/dev/null | head -1',
        ],
        'certbot' => [
            'label' => 'Certbot',
            'group' => 'core',
            'description' => "Let's Encrypt client for free TLS certificates.",
            'core' => true,
            'default' => true,
            'requires' => ['nginx'],
            'detect' => 'command -v certbot',
            'version' => 'certbot --version 2>&1 | head -1',
        ],
        'mysql' => [
            'label' => 'MariaDB',
            'group' => 'database',
            'description' => 'Required for WordPress and most Laravel sites.',
            'default' => true,
            'requires' => ['base'],
            'detect' => 'command -v mariadb || command -v mysql',
            'version' => 'mysql --version 2>/dev/null | head -1',
        ],
        'postgres' => [
            'label' => 'PostgreSQL',
            'group' => 'database',
            'description' => 'For Laravel and Node apps that prefer Postgres.',
            'default' => false,
            'requires' => ['base'],
            'detect' => 'command -v psql',
            'version' => 'psql --version 2>/dev/null | head -1',
        ],
        'mongodb' => [
            'label' => 'MongoDB',
            'group' => 'database',
            'description' => 'Document database, installed from the official repo.',
            'default' => false,
            'requires' => ['base'],
            'detect' => 'command -v mongod',
            'version' => 'mongod --version 2>/dev/null | head -1',
        ],
        'redis' => [
            'label' => 'Redis',
            'group' => 'database',
            'description' => 'Cache, queue and session store. Password protected.',
            'default' => true,
            'requires' => ['base'],
            'detect' => 'command -v redis-server',
            'version' => 'redis-server --version 2>/dev/null | head -1',
        ],
        'node' => [
            'label' => 'Node.js',
            'group' => 'runtime',
            'description' => 'Needed for Node.js and Next.js sites.',
            'default' => true,
            'requires' => ['base'],
            'detect' => 'command -v node',
            'version' => 'node -v 2>/dev/null',
        ],
        'pm2' => [
            'label' => 'PM2',
            'group' => 'runtime',
            'description' => 'Node.js process manager. The panel runs Node and Next.js sites under systemd, so this is for managing processes yourself.',
            'default' => true,
            'requires' => ['node'],
            'detect' => 'command -v pm2',
            'version' => 'pm2 --version 2>/dev/null | tail -1',
        ],
        'wpcli' => [
            'label' => 'WP-CLI',
            'group' => 'runtime',
            'description' => 'Needed to install and manage WordPress sites.',
            'default' => true,
            'requires' => ['php'],
            'detect' => 'command -v wp',
            'version' => 'wp --version --allow-root 2>/dev/null | head -1',
        ],
        'mail' => [
            'label' => 'Mail server',
            'group' => 'mail',
            'description' => 'Postfix, Dovecot and OpenDKIM with mailboxes stored in MariaDB.',
            'default' => false,
            'requires' => ['mysql'],
            'detect' => 'command -v postfix && test -f /etc/dovecot/dovecot-sql.conf.ext',
            'version' => 'postconf -h mail_version 2>/dev/null',
        ],
    ],

    /*
    | Site types the panel knows how to deploy, and what each one needs.
    */
    'site_types' => [
        'wordpress' => ['label' => 'WordPress', 'web_directory' => '', 'runtime' => 'php', 'database' => true],
        'laravel' => ['label' => 'Laravel', 'web_directory' => 'public', 'runtime' => 'php', 'database' => true],
        'php' => ['label' => 'PHP', 'web_directory' => 'public', 'runtime' => 'php'],
        'nodejs' => ['label' => 'Node.js', 'web_directory' => '', 'runtime' => 'node', 'proxied' => true],
        'nextjs' => ['label' => 'Next.js', 'web_directory' => '', 'runtime' => 'node', 'proxied' => true, 'build' => 'npm run build', 'start' => 'npm run start'],
        'static' => ['label' => 'Static HTML', 'web_directory' => '', 'runtime' => 'static'],
    ],

    /*
    | Websocket terminal daemon (php artisan panel:terminal-server).
    |
    | The daemon binds to loopback only and nginx proxies `path` through to it,
    | so the browser dials the panel's own origin. That matters: the daemon's
    | bind address is 127.0.0.1 *on the server*, which is a different machine
    | from the one the browser is on, and the panel is served over TLS, which
    | forbids a plain ws:// socket anyway.
    |
    | `url` overrides the whole thing with an absolute address, for the case
    | where the daemon lives somewhere else entirely.
    */
    'terminal' => [
        'host' => env('PANEL_TERMINAL_HOST', '127.0.0.1'),
        'port' => env('PANEL_TERMINAL_PORT', 6001),
        'path' => env('PANEL_TERMINAL_PATH', '/terminal-ws'),
        'url' => env('PANEL_TERMINAL_URL'),
    ],

    /*
    | Usage metrics.
    |
    | One snapshot per server — CPU, memory and disk — collected by the queue
    | and shown on the server list so it is obvious which boxes are running out
    | of room. No history is kept and nothing in the HTTP path ever connects
    | over SSH, so pages render from the database and load immediately.
    |
    | `stale_after` is how old (in seconds) a snapshot may be before the list
    | fades it and the scheduler samples that server again.
    */
    'metrics' => [
        'stale_after' => (int) env('PANEL_METRICS_STALE_AFTER', 120),

        /*
        | Live figures on a server page stream over the terminal websocket: the
        | server runs a loop that prints one reading per interval and the panel
        | forwards the line. Nothing is installed on the server and no command
        | is sent per sample, so the cadence costs a round trip only once.
        */
        'stream_interval' => (int) env('PANEL_METRICS_STREAM_INTERVAL', 1),
    ],

    /*
    | Database engines the panel can create databases on.
    */
    'database_engines' => [
        'mysql' => 'MariaDB / MySQL',
        'postgres' => 'PostgreSQL',
        'mongodb' => 'MongoDB',
    ],

];
