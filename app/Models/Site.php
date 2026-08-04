<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

class Site extends Model
{
    use HasFactory;

    /** Site types the panel knows how to deploy. */
    public const TYPES = ['php', 'laravel', 'wordpress', 'nodejs', 'nextjs', 'static'];

    /** Types served by nginx reverse proxy to a long-running process. */
    public const PROXIED_TYPES = ['nodejs', 'nextjs'];

    /**
     * What the create form gates on. Probed against the machine when the form
     * is opened, so a stale row cannot refuse a site the box can host.
     */
    public const REQUIRED_SERVICES = ['node', 'mysql', 'wpcli', 'php'];

    /** Types that get an application database created for them. */
    public const DATABASE_TYPES = ['wordpress', 'laravel'];

    protected $fillable = [
        'user_id', 'dns_account_id', 'database_id', 'domain', 'type', 'aliases',
        'root_path', 'web_directory', 'php_version', 'node_version', 'status', 'ssl', 'last_error',
        'app_port', 'start_command', 'build_command', 'repository', 'branch',
        'wp_admin_user', 'wp_admin_password', 'wp_admin_email', 'wp_title',
        'manage_dns', 'dns_zone_id', 'dns_record_ids',
        'dns_type', 'dns_content', 'dns_proxied',
    ];

    protected $hidden = ['wp_admin_password'];

    protected function casts(): array
    {
        return [
            'aliases' => 'array',
            'dns_record_ids' => 'array',
            'wp_admin_password' => 'encrypted',
            'ssl' => 'boolean',
            'manage_dns' => 'boolean',
            'dns_proxied' => 'boolean',
        ];
    }


    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function dnsAccount(): BelongsTo
    {
        return $this->belongsTo(DnsAccount::class);
    }

    public function database(): BelongsTo
    {
        return $this->belongsTo(Database::class, 'database_id');
    }

    public function activityLogs(): HasMany
    {
        return $this->hasMany(ActivityLog::class)->latest();
    }

    /** All hostnames this site answers on. */
    public function hostnames(): array
    {
        return array_values(array_unique(array_merge([$this->domain], $this->aliases ?? [])));
    }

    public function documentRoot(): string
    {
        return rtrim($this->root_path, '/').$this->web_directory;
    }

    public function isProxied(): bool
    {
        return in_array($this->type, self::PROXIED_TYPES, true);
    }

    public function needsDatabase(): bool
    {
        return in_array($this->type, self::DATABASE_TYPES, true);
    }

    public function isPhp(): bool
    {
        return in_array($this->type, ['php', 'laravel', 'wordpress'], true);
    }

    /** systemd unit name for node-family apps. */
    public function serviceName(): string
    {
        return 'ubuntu-panel-'.str_replace('.', '-', $this->domain);
    }

    /**
     * The nginx directives that actually serve this site, shared by the HTTP
     * and HTTPS forms of the vhost so the two can never drift apart.
     */
    public function bodyBlock(): string
    {
        return match (true) {
            $this->isProxied() => $this->proxyBody(),
            $this->type === 'static' => $this->staticBody(),
            default => $this->phpBody(),
        };
    }

    /** Only the proxied types need an upstream, and it must sit outside server {}. */
    public function upstreamBlock(): string
    {
        if (! $this->isProxied()) {
            return '';
        }

        return <<<NGINX

        upstream {$this->serviceName()}_upstream {
            server 127.0.0.1:{$this->app_port};
            keepalive 32;
        }

        NGINX;
    }

    /** Certbot's HTTP-01 challenge has to stay reachable over plain HTTP. */
    public function acmeBlock(): string
    {
        $root = \App\Services\Sites\NginxVhost::ACME_WEBROOT;

        return <<<NGINX
            location ^~ /.well-known/acme-challenge/ {
                root {$root};
                default_type "text/plain";
            }
        NGINX;
    }

    protected function phpBody(): string
    {
        $root = $this->documentRoot();
        $php = $this->php_version;
        $extra = $this->type === 'wordpress' ? $this->wordpressHardening() : '';

        return <<<NGINX
            root {$root};

            index index.php index.html;
            charset utf-8;
            client_max_body_size 128M;

            location / {
                try_files \$uri \$uri/ /index.php?\$query_string;
            }

            location = /favicon.ico { access_log off; log_not_found off; }
            location = /robots.txt  { access_log off; log_not_found off; }
        {$extra}
            location ~ \.php\$ {
                include snippets/fastcgi-php.conf;
                fastcgi_pass unix:/run/php/php{$php}-fpm.sock;
                fastcgi_param SCRIPT_FILENAME \$realpath_root\$fastcgi_script_name;
                fastcgi_param HTTPS \$panel_https;
                fastcgi_read_timeout 300;
                include fastcgi_params;
            }

            location ~ /\.(?!well-known).* {
                deny all;
            }
        NGINX;
    }

    protected function staticBody(): string
    {
        $root = $this->documentRoot();

        return <<<NGINX
            root {$root};

            index index.html index.htm;
            charset utf-8;

            location / {
                try_files \$uri \$uri/ \$uri.html /index.html;
            }

            location ~* \.(?:css|js|jpg|jpeg|png|gif|ico|svg|woff2?)\$ {
                expires 30d;
                access_log off;
            }

            location ~ /\.(?!well-known).* {
                deny all;
            }
        NGINX;
    }

    protected function proxyBody(): string
    {
        $upstream = $this->serviceName().'_upstream';
        $root = rtrim($this->root_path, '/');

        $static = $this->type === 'nextjs' ? <<<NGINX

            location /_next/static/ {
                alias {$root}/.next/static/;
                expires 365d;
                access_log off;
            }

        NGINX : '';

        return <<<NGINX
            charset utf-8;
            client_max_body_size 128M;
        {$static}
            location / {
                proxy_pass http://{$upstream};
                proxy_http_version 1.1;
                proxy_set_header Upgrade \$http_upgrade;
                proxy_set_header Connection "upgrade";
                proxy_set_header Host \$host;
                proxy_set_header X-Real-IP \$remote_addr;
                proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
                proxy_set_header X-Forwarded-Proto \$scheme;
                proxy_cache_bypass \$http_upgrade;
                proxy_read_timeout 300;
            }
        NGINX;
    }

    protected function wordpressHardening(): string
    {
        return <<<'NGINX'

            # WordPress hardening
            location = /xmlrpc.php { deny all; access_log off; log_not_found off; }
            location ~* /(?:uploads|files)/.*\.php$ { deny all; }
            location ~* \.(?:css|js|jpg|jpeg|png|gif|ico|svg|webp|woff2?)$ {
                expires 30d;
                access_log off;
            }

        NGINX;
    }

    public function typeLabel(): string
    {
        return match ($this->type) {
            'php' => 'PHP',
            'laravel' => 'Laravel',
            'wordpress' => 'WordPress',
            'nodejs' => 'Node.js',
            'nextjs' => 'Next.js',
            'static' => 'Static HTML',
            default => $this->type,
        };
    }
}
