<?php

namespace App\Console\Commands;

use App\Services\System\PanelDomain;
use Illuminate\Console\Command;
use Throwable;

/**
 * Serve the panel on a hostname you own, with a real certificate.
 */
class SetPanelDomain extends Command
{
    protected $signature = 'panel:domain
                            {domain? : The hostname to serve the panel on}
                            {--email= : Contact address for Let\'s Encrypt}
                            {--clear : Go back to the IP address}';

    protected $description = "Point the panel at your own domain and secure it with Let's Encrypt";

    public function handle(PanelDomain $panel): int
    {
        if ($this->option('clear')) {
            $panel->clear();
            $this->components->info('Cleared. Re-run the installer to rebuild the IP vhost.');

            return self::SUCCESS;
        }

        $domain = $this->argument('domain') ?: $this->ask('Which hostname should the panel answer on?');

        if (blank($domain)) {
            $this->components->error('No hostname given.');

            return self::FAILURE;
        }

        $this->components->info("Setting the panel up on {$domain}");

        try {
            foreach ($panel->apply($domain, $this->option('email')) as $line) {
                $this->line('   '.$line);
            }
        } catch (Throwable $e) {
            $this->components->error($e->getMessage());

            return self::FAILURE;
        }

        $this->newLine();
        $this->components->info("Log in at https://{$domain}");

        return self::SUCCESS;
    }
}
