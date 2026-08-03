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
}
