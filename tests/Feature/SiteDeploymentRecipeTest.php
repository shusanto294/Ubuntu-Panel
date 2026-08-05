<?php

namespace Tests\Feature;

use App\Models\Site;
use App\Models\User;
use App\Services\Databases\DatabaseManager;
use App\Services\Sites\NginxVhost;
use App\Services\Sites\SiteRecipe;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Tests\TestCase;

class SiteDeploymentRecipeTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    protected function site(array $attributes): Site
    {
        $user = User::factory()->create();


        $this->markInstalled(['mysql', 'node', 'wpcli']);

        return Site::create(array_merge([
            'user_id' => $user->id,
            'domain' => 'app.example.com',
            'root_path' => '/var/www/app.example.com',
            'web_directory' => '',
            'php_version' => '8.3',
        ], $attributes));
    }

    protected function stepNames(Site $site): array
    {
        return array_map(
            fn ($step) => $step->name,
            (new SiteRecipe($site, app(DatabaseManager::class)))->steps()
        );
    }

    public function test_a_wordpress_deployment_creates_a_database_and_installs_core(): void
    {
        $site = $this->site(['type' => 'wordpress', 'wp_admin_user' => 'admin']);

        $names = $this->stepNames($site);

        $this->assertContains('Create the application database', $names);
        $this->assertContains('Download WordPress core', $names);
        $this->assertContains('Write wp-config.php', $names);
        $this->assertContains('Install WordPress', $names);
        $this->assertContains('Reload nginx', $names);

        // The database record is created up front so credentials can be written in.
        $site->refresh();
        $this->assertNotNull($site->database);
        $this->assertTrue($site->database->managed_by_site);
        $this->assertSame('mysql', $site->database->engine);
    }

    public function test_a_laravel_deployment_writes_an_env_and_migrates(): void
    {
        $site = $this->site(['type' => 'laravel', 'web_directory' => '/public']);

        $names = $this->stepNames($site);

        $this->assertContains('Install a fresh Laravel application', $names);
        $this->assertContains('Write the .env file', $names);
        $this->assertContains('Prepare the application', $names);
        $this->assertSame('/var/www/app.example.com/public', $site->documentRoot());
    }

    public function test_a_nextjs_deployment_builds_and_installs_a_service(): void
    {
        $site = $this->site([
            'type' => 'nextjs',
            'app_port' => 3000,
            'build_command' => 'npm run build',
            'start_command' => 'npm run start',
            'repository' => 'https://github.com/example/app.git',
        ]);

        $names = $this->stepNames($site);

        $this->assertContains('Clone the repository', $names);
        $this->assertContains('Install dependencies', $names);
        $this->assertContains('Build the application', $names);
        $this->assertContains('Create the systemd service', $names);
        $this->assertContains('Start the application', $names);
        $this->assertNotContains('Create the application database', $names);
    }

    public function test_https_is_on_by_default_and_http_redirects_to_it(): void
    {
        $site = $this->site(['type' => 'php', 'ssl' => true]);

        $names = $this->stepNames($site);

        $this->assertContains('Request the TLS certificate', $names);
        $this->assertContains('Serve the site over HTTPS', $names);

        $tls = NginxVhost::render($site, tls: true);

        // Port 80 keeps only the ACME challenge and a redirect.
        $this->assertStringContainsString('return 301 https://$host$request_uri;', $tls);
        $this->assertStringContainsString('listen 443 ssl;', $tls);
        $this->assertStringContainsString(
            'ssl_certificate     /etc/letsencrypt/live/app.example.com/fullchain.pem;',
            $tls
        );
        $this->assertStringContainsString('Strict-Transport-Security', $tls);
        $this->assertStringContainsString('/.well-known/acme-challenge/', $tls);
    }

    public function test_the_plain_vhost_never_references_a_certificate(): void
    {
        // nginx refuses to start if ssl_certificate points at a missing file, so
        // the first deploy has to be HTTP-only.
        $plain = NginxVhost::render($this->site(['type' => 'php', 'ssl' => true]), tls: false);

        $this->assertStringNotContainsString('ssl_certificate', $plain);
        $this->assertStringNotContainsString('listen 443', $plain);
        $this->assertStringContainsString('/.well-known/acme-challenge/', $plain);
    }

    public function test_the_https_and_http_vhosts_serve_the_same_site(): void
    {
        $site = $this->site(['type' => 'wordpress', 'ssl' => true]);

        $plain = NginxVhost::render($site, tls: false);
        $tls = NginxVhost::render($site, tls: true);

        // The body is shared, so hardening and fastcgi cannot drift apart.
        foreach (['xmlrpc.php', 'fastcgi_pass unix:/run/php/php8.3-fpm.sock;', 'root /var/www/app.example.com;'] as $needle) {
            $this->assertStringContainsString($needle, $plain);
            $this->assertStringContainsString($needle, $tls);
        }
    }

    public function test_php_is_told_when_the_request_arrived_over_https(): void
    {
        $site = $this->site(['type' => 'php']);

        // Set by the map in the panel's nginx helper config, so WordPress sees
        // is_ssl() correctly whether TLS ends here or at Cloudflare.
        $this->assertStringContainsString(
            'fastcgi_param HTTPS $panel_https;',
            NginxVhost::render($site)
        );
        $this->assertStringContainsString('$panel_https', NginxVhost::helperConfig());
    }

    /**
     * The overrides have to be the last word on those two parameters.
     *
     * `snippets/fastcgi-php.conf` pulls in `fastcgi.conf`, which sets the whole
     * standard set — including `HTTPS $https if_not_empty`. A second
     * `include fastcgi_params;` after our own lines sent every parameter twice
     * and put the stock `HTTPS` back on top of `$panel_https`, which is the one
     * that tells WordPress the request was secure when TLS ended at Cloudflare.
     */
    public function test_the_fastcgi_overrides_are_not_undone_by_a_later_include(): void
    {
        $vhost = NginxVhost::render($this->site(['type' => 'php']));

        $this->assertSame(1, substr_count($vhost, 'include snippets/fastcgi-php.conf;'));
        $this->assertStringNotContainsString('include fastcgi_params;', $vhost);

        $this->assertGreaterThan(
            strpos($vhost, 'include snippets/fastcgi-php.conf;'),
            strpos($vhost, 'fastcgi_param HTTPS $panel_https;'),
            'the HTTPS override must come after the snippet that sets the stock value'
        );
    }

    /**
     * `http2 on;` is an nginx 1.25.1 directive. Ubuntu 22.04 ships 1.18 and
     * 24.04 ships 1.24, where an unknown directive is fatal rather than
     * ignored: `nginx -t` fails, the reload is refused, and every site on the
     * box stays frozen on the last configuration that loaded.
     */
    public function test_http2_is_only_declared_where_nginx_understands_it(): void
    {
        $site = $this->site(['type' => 'php', 'ssl' => true]);

        $this->assertStringContainsString('http2 on;', NginxVhost::render($site, tls: true, http2: true));
        $this->assertStringNotContainsString('http2', NginxVhost::render($site, tls: true, http2: false));

        $this->assertFalse(NginxVhost::supportsHttp2Directive('nginx version: nginx/1.18.0 (Ubuntu)'));
        $this->assertFalse(NginxVhost::supportsHttp2Directive('nginx version: nginx/1.24.0 (Ubuntu)'));
        $this->assertTrue(NginxVhost::supportsHttp2Directive('nginx version: nginx/1.25.1'));
        $this->assertTrue(NginxVhost::supportsHttp2Directive('nginx version: nginx/1.28.0 (Ubuntu)'));

        // Unreadable version: losing HTTP/2 costs a little speed, guessing the
        // other way costs the whole configuration.
        $this->assertFalse(NginxVhost::supportsHttp2Directive('command not found'));
    }

    public function test_wordpress_urls_follow_the_final_scheme(): void
    {
        $site = $this->site(['type' => 'wordpress', 'ssl' => true]);

        $this->assertContains('Point WordPress at the final URL', $this->stepNames($site));
    }

    /**
     * nginx serves the first block on a port when nothing matches the Host, so
     * without a catch-all a site whose certificate has not been issued yet has
     * its HTTPS traffic answered by whichever site happens to be first — which
     * then redirects the visitor to *its* login page.
     */
    public function test_unknown_hostnames_hit_a_dead_end_not_another_site(): void
    {
        $config = NginxVhost::defaultServerConfig();

        $this->assertStringContainsString('listen 80 default_server;', $config);
        $this->assertStringContainsString('listen 443 ssl default_server;', $config);
        $this->assertStringContainsString('server_name _;', $config);
        $this->assertStringContainsString('return 404', $config);
        // It still has to answer ACME challenges for domains being set up.
        $this->assertStringContainsString('/.well-known/acme-challenge/', $config);
    }

    public function test_a_site_vhost_never_claims_the_default_server_slot(): void
    {
        $site = $this->site(['type' => 'php', 'ssl' => true]);

        foreach ([NginxVhost::render($site), NginxVhost::render($site, tls: true)] as $vhost) {
            $this->assertStringNotContainsString('default_server', $vhost);
        }
    }

    public function test_deploying_installs_the_catch_all(): void
    {
        $this->assertContains('Write the nginx vhost', $this->stepNames($this->site(['type' => 'php'])));
        $this->assertStringContainsString(
            '000-panel-default.conf',
            NginxVhost::defaultServerPath()
        );
    }

    public function test_the_php_vhost_uses_fastcgi(): void
    {
        $site = $this->site(['type' => 'php', 'web_directory' => '/public', 'aliases' => ['www.app.example.com']]);

        $vhost = NginxVhost::render($site);

        $this->assertStringContainsString('server_name app.example.com www.app.example.com;', $vhost);
        $this->assertStringContainsString('root /var/www/app.example.com/public;', $vhost);
        $this->assertStringContainsString('fastcgi_pass unix:/run/php/php8.3-fpm.sock;', $vhost);
        $this->assertStringNotContainsString('proxy_pass', $vhost);
    }

    public function test_the_node_vhost_reverse_proxies_to_the_app_port(): void
    {
        $site = $this->site(['type' => 'nodejs', 'app_port' => 3007]);

        $vhost = NginxVhost::render($site);

        $this->assertStringContainsString('server 127.0.0.1:3007;', $vhost);
        $this->assertStringContainsString('upstream ubuntu-panel-app-example-com_upstream {', $vhost);
        $this->assertStringContainsString('proxy_pass http://ubuntu-panel-app-example-com_upstream;', $vhost);
        $this->assertStringNotContainsString('fastcgi_pass', $vhost);
    }

    public function test_the_systemd_unit_runs_the_start_command_on_the_assigned_port(): void
    {
        $site = $this->site([
            'type' => 'nodejs',
            'app_port' => 3007,
            'start_command' => 'node server.js',
        ]);

        $unit = NginxVhost::systemdUnit($site);

        $this->assertStringContainsString('Environment=PORT=3007', $unit);
        $this->assertStringContainsString("ExecStart=/bin/bash -lc 'node server.js'", $unit);
        $this->assertStringContainsString('WorkingDirectory=/var/www/app.example.com', $unit);
        $this->assertStringContainsString('Restart=always', $unit);
    }

    public function test_the_static_vhost_serves_files_without_php(): void
    {
        $site = $this->site(['type' => 'static']);

        $vhost = NginxVhost::render($site);

        $this->assertStringContainsString('index index.html index.htm;', $vhost);
        $this->assertStringNotContainsString('fastcgi_pass', $vhost);
        $this->assertStringNotContainsString('proxy_pass', $vhost);
    }

    /**
     * nginx loads a vhost pointing at a PHP-FPM socket that does not exist,
     * `nginx -t` passes, the deployment reports success — and every request is
     * a 502 from an origin where, as far as the panel is concerned, nothing
     * went wrong. A site can be created against any version the catalogue
     * knows, and the machine only has the ones somebody installed.
     */
    public function test_a_php_site_checks_its_runtime_is_actually_on_the_machine(): void
    {
        foreach (['wordpress', 'laravel', 'php'] as $type) {
            $site = $this->site(['type' => $type, 'php_version' => '8.3']);

            $this->assertContains(
                'Make sure PHP 8.3 is on this machine',
                $this->stepNames($site),
                $type.' sites run on PHP-FPM and must check for it'
            );

            $site->delete();
        }

        // Static and Node sites never touch PHP-FPM.
        $static = $this->site(['type' => 'static', 'domain' => 'static.example.com']);

        $this->assertEmpty(array_filter(
            $this->stepNames($static),
            fn (string $name) => str_starts_with($name, 'Make sure PHP')
        ));
    }
}
