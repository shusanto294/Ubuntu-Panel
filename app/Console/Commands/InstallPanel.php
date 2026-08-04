<?php

namespace App\Console\Commands;

use App\Models\User;
use App\Services\System\HostInfo;
use App\Services\System\ServiceInstaller;
use App\Support\Settings;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Hash;
use Illuminate\Validation\Rules\Password;
use Throwable;

use function Laravel\Prompts\password;
use function Laravel\Prompts\text;

/**
 * Finishes setting the panel up on the machine it was just installed on:
 * creates the administrator, records what is already present, and stores the
 * defaults new sites inherit.
 *
 * install.sh calls this at the end; it is safe to run again.
 */
class InstallPanel extends Command
{
    protected $signature = 'panel:install
                            {--email= : Administrator email}
                            {--password= : Administrator password}
                            {--name=Administrator : Administrator name}
                            {--php-version= : PHP version new sites inherit (defaults to the one the panel runs on)}
                            {--no-detect : Skip reading what is already installed}';

    protected $description = 'Create the administrator account and take inventory of this machine';

    public function handle(ServiceInstaller $installer, Settings $settings, HostInfo $host): int
    {
        $this->components->info('Setting up Ubuntu Panel on '.$host->hostname());

        // Defaults new sites inherit, and what this machine is.
        $settings->set('os', $host->os());

        if (! $settings->get('php_version')) {
            // The version the panel itself runs on, unless told otherwise.
            //
            // Taking the newest the catalogue knows about would install a
            // second PHP alongside the one the installer just set up — two
            // stacks to keep patched, and extensions landing on the wrong one:
            // the panel would be running 8.3 while its own redis extension
            // went to 8.5, and then be unable to use the Redis it installed.
            $settings->set('php_version', $this->phpVersion());
        }

        if (! $settings->get('node_version')) {
            $settings->set('node_version', config('panel.node_versions')[0]);
        }

        $installer->syncRows();

        if (! $this->option('no-detect')) {
            $this->components->task('Reading what is already installed', function () use ($installer) {
                try {
                    $installer->detect();

                    return true;
                } catch (Throwable $e) {
                    $this->components->warn('Could not take inventory: '.$e->getMessage());

                    return false;
                }
            });
        }

        $this->createAdministrator();

        $this->newLine();
        $this->components->info('Done. Open the panel and sign in.');

        return self::SUCCESS;
    }

    /**
     * What new sites should inherit: whatever was asked for, else the version
     * running this command, else the newest the catalogue offers.
     */
    protected function phpVersion(): string
    {
        $requested = (string) $this->option('php-version');
        $known = config('panel.php_versions');

        if ($requested !== '' && in_array($requested, $known, true)) {
            return $requested;
        }

        $running = PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;

        return in_array($running, $known, true) ? $running : $known[0];
    }

    /**
     * The first account is the administrator. Running this again with an email
     * that already exists resets that account's password instead of failing,
     * which is the usual reason someone runs it twice.
     */
    protected function createAdministrator(): void
    {
        $email = $this->option('email');
        $password = $this->option('password');

        if (! $email && User::exists()) {
            $this->components->info('An account already exists; skipping administrator creation.');

            return;
        }

        $email ??= text(
            label: 'Administrator email',
            required: true,
            validate: fn (string $value) => filter_var($value, FILTER_VALIDATE_EMAIL) ? null : 'Enter a valid email address.'
        );

        $password ??= password(
            label: 'Administrator password',
            required: true,
            validate: fn (string $value) => strlen($value) >= 8 ? null : 'Use at least 8 characters.'
        );

        $validator = validator(
            ['email' => $email, 'password' => $password],
            ['email' => ['required', 'email'], 'password' => ['required', Password::min(8)]]
        );

        if ($validator->fails()) {
            foreach ($validator->errors()->all() as $error) {
                $this->components->error($error);
            }

            return;
        }

        $user = User::updateOrCreate(
            ['email' => $email],
            [
                'name' => $this->option('name') ?: 'Administrator',
                'password' => Hash::make($password),
                'email_verified_at' => now(),
            ]
        );

        $this->components->info(
            $user->wasRecentlyCreated
                ? "Administrator created: {$email}"
                : "Password reset for {$email}"
        );
    }
}
