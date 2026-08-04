<?php

namespace App\Services\System;

use App\Services\Shell\LocalConnection;
use RuntimeException;

/**
 * Puts the terminal websocket behind nginx, on the panel's own origin.
 *
 * The daemon binds to loopback so nothing but nginx can reach it. That leaves
 * the browser with no way in on its own — 127.0.0.1 in a browser is the user's
 * laptop, and the panel is served over TLS, which refuses a plain ws:// socket
 * regardless. So nginx proxies one path through to the daemon and the terminal
 * travels inside the connection the page is already using: same host, same
 * port, same certificate, no extra hole in the firewall.
 *
 * The location lives in a snippet rather than in the vhost itself because the
 * vhost has three authors — the installer, {@see PanelDomain}, and certbot —
 * and a snippet is the one thing all three leave alone.
 */
class TerminalProxy
{
    public const SNIPPET = '/etc/nginx/snippets/ubuntu-panel-terminal.conf';

    public const INCLUDE_LINE = 'include snippets/ubuntu-panel-terminal.conf;';

    public function __construct(
        protected LocalConnection $shell,
        protected PhpMyAdmin $phpMyAdmin,
    ) {}

    /**
     * Write the snippet and make sure the panel vhost pulls it in.
     *
     * Safe to run repeatedly: the snippet is rewritten from config every time
     * and the include is only added where it is missing.
     *
     * @return array<int, string> log lines
     */
    public function sync(): array
    {
        $lines = [];

        $this->writeSnippet();
        $lines[] = 'Wrote '.self::SNIPPET.'.';

        $vhost = PanelDomain::VHOST;

        if (! $this->shell->exists($vhost)) {
            $lines[] = "No panel vhost at {$vhost} — nothing to include it from.";

            return $lines;
        }

        $current = $this->shell->readFile($vhost);

        if (str_contains($current, self::INCLUDE_LINE)) {
            $lines[] = 'The panel vhost already includes it.';
        } else {
            $patched = $this->withInclude($current);

            if ($patched === null) {
                throw new RuntimeException(
                    "Could not find a server block in {$vhost} to add the terminal proxy to. ".
                    'Add this line inside it by hand:  '.self::INCLUDE_LINE
                );
            }

            $this->shell->putFile($vhost.'.panel-backup', $current);
            $this->shell->putFile($vhost, $patched);
            $lines[] = 'Added the terminal proxy to the panel vhost.';
        }

        [$output, $code] = $this->shell->run('sudo nginx -t');

        if ($code !== 0) {
            throw new RuntimeException(
                "nginx rejected the configuration, so it was left alone:\n".$output.
                "\nThe previous vhost is at {$vhost}.panel-backup."
            );
        }

        $this->shell->mustRun('sudo systemctl reload nginx');
        $lines[] = 'nginx reloaded.';

        return $lines;
    }

    /**
     * Put the snippet on disk without touching any vhost.
     *
     * Anything that writes a vhost containing the include has to call this
     * first — nginx refuses to start on an include that points at nothing.
     */
    public function writeSnippet(): void
    {
        $this->shell->putFile(self::SNIPPET, $this->snippet());
    }

    /** Is the proxy in place right now? */
    public function isConfigured(): bool
    {
        if (! $this->shell->exists(self::SNIPPET) || ! $this->shell->exists(PanelDomain::VHOST)) {
            return false;
        }

        return str_contains($this->shell->readFile(PanelDomain::VHOST), self::INCLUDE_LINE);
    }

    /** The location block, as its own file. */
    public function snippet(): string
    {
        $host = (string) config('panel.terminal.host', '127.0.0.1');
        $port = (int) config('panel.terminal.port', 6001);
        $path = '/'.ltrim((string) config('panel.terminal.path', '/terminal-ws'), '/');

        $phpMyAdmin = $this->phpMyAdmin->nginxLocation();

        return <<<NGINX
        # Managed by Ubuntu Panel. Included by the panel vhost; edit the panel's
        # configuration, not this file.

        {$phpMyAdmin}

        # The browser terminal's websocket.
        location {$path} {
            proxy_pass http://{$host}:{$port};
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$http_upgrade;
            proxy_set_header Connection "upgrade";
            proxy_set_header Host \$host;
            proxy_set_header X-Real-IP \$remote_addr;
            proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$scheme;

            # A shell can sit idle for hours; the default minute would drop it.
            proxy_read_timeout 86400s;
            proxy_send_timeout 86400s;
            proxy_buffering off;
        }

        NGINX;
    }

    /**
     * Add the include to every server block in the vhost.
     *
     * `server_name` is the anchor because it is the one directive every author
     * of this file writes, at a known nesting depth, on its own line — certbot's
     * rewritten 443 block included.
     */
    protected function withInclude(string $vhost): ?string
    {
        $added = 0;

        $patched = preg_replace_callback(
            '/^([ \t]*)(server_name\s[^\n]*;)[ \t]*$/m',
            function (array $match) use (&$added): string {
                $added++;

                return $match[1].$match[2]."\n\n".$match[1].self::INCLUDE_LINE;
            },
            $vhost
        );

        return $added > 0 ? $patched : null;
    }
}
