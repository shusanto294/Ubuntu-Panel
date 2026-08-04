<?php

namespace Tests\Feature;

use App\Services\System\TerminalProxy;
use ReflectionMethod;
use Tests\TestCase;

class TerminalProxyTest extends TestCase
{
    protected function addInclude(string $vhost): ?string
    {
        $method = new ReflectionMethod(TerminalProxy::class, 'withInclude');

        return $method->invoke(app(TerminalProxy::class), $vhost);
    }

    public function test_the_snippet_proxies_the_configured_path_and_port(): void
    {
        config(['panel.terminal.path' => '/shell-socket', 'panel.terminal.port' => 6100]);

        $snippet = app(TerminalProxy::class)->snippet();

        $this->assertStringContainsString('location /shell-socket {', $snippet);
        $this->assertStringContainsString('proxy_pass http://127.0.0.1:6100;', $snippet);
        // Without these the handshake never upgrades and the socket 400s.
        $this->assertStringContainsString('proxy_set_header Upgrade $http_upgrade;', $snippet);
        $this->assertStringContainsString('proxy_set_header Connection "upgrade";', $snippet);
    }

    public function test_the_include_goes_into_every_server_block(): void
    {
        // What certbot leaves behind: the original block plus a TLS one.
        $vhost = <<<'NGINX'
        server {
            listen 80;
            server_name panel.example.com;
            return 301 https://$host$request_uri;
        }

        server {
            listen 443 ssl;
            server_name panel.example.com;

            root /opt/ubuntu-panel/public;
        }
        NGINX;

        $patched = $this->addInclude($vhost);

        $this->assertSame(2, substr_count($patched, TerminalProxy::INCLUDE_LINE));
        // Indented to match the block it landed in, not flattened to column one.
        $this->assertStringContainsString('    '.TerminalProxy::INCLUDE_LINE, $patched);
    }

    public function test_a_vhost_with_no_server_block_is_refused_rather_than_mangled(): void
    {
        $this->assertNull($this->addInclude("# nothing useful here\n"));
    }
}
