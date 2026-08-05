<?php

namespace Tests\Feature;

use App\Console\Commands\UpdatePanel;
use App\Models\User;
use App\Services\System\UpdateChecker;
use Illuminate\Foundation\Testing\RefreshDatabase;
use ReflectionMethod;
use Tests\TestCase;

class UpdatePanelTest extends TestCase
{
    use RefreshDatabase;

    /**
     * The copy button on the version card hands you a whole command. Without
     * the `cd` it lands you in whatever directory you happen to be in, where
     * artisan is not, and answers "Could not open input file: artisan".
     */
    public function test_the_update_command_takes_you_to_the_panel_first(): void
    {
        $command = app(UpdateChecker::class)->updateCommand();

        $this->assertStringStartsWith('cd ', $command);
        $this->assertStringContainsString(base_path(), $command);
        $this->assertStringContainsString('php artisan panel:update', $command);
    }

    public function test_a_path_that_needs_quoting_gets_it(): void
    {
        $checker = app(UpdateChecker::class);

        // A --dir install can land somewhere with a space in it, and an
        // unquoted `cd` would stop at the space.
        $quoted = str_contains(base_path(), ' ');

        $this->assertSame(
            $quoted,
            str_contains($checker->updateCommand(), "'".base_path()."'"),
            'the path is quoted when, and only when, it needs to be'
        );
    }

    public function test_the_version_card_is_given_that_command(): void
    {
        $this->actingAs(User::factory()->create())
            ->get(route('settings'))
            ->assertOk()
            ->assertInertia(fn ($page) => $page->where(
                'update.command',
                app(UpdateChecker::class)->updateCommand()
            ));
    }

    /**
     * Code is re-read per request; the compiled caches are not. Without this,
     * a setting that changed in a release is served at its old value until
     * something restarts the pool.
     */
    public function test_the_restart_takes_php_fpm_with_it(): void
    {
        $method = new ReflectionMethod(UpdatePanel::class, 'restartScript');
        $script = $method->invoke($this->app->make(UpdatePanel::class));

        $this->assertStringContainsString('ubuntu-panel-queue.service', $script);
        $this->assertStringContainsString('ubuntu-panel-terminal.service', $script);
        $this->assertStringContainsString(
            sprintf('php%d.%d-fpm', PHP_MAJOR_VERSION, PHP_MINOR_VERSION),
            $script
        );
        // Machines that run the panel some other way have no such unit.
        $this->assertStringContainsString('|| true', $script);
    }

    public function test_the_version_is_re_read_from_disk_after_an_update(): void
    {
        // What the running process is holding: the value it booted with.
        config(['panel.version' => '0.0.1-stale']);

        $method = new ReflectionMethod(UpdatePanel::class, 'reloadConfig');
        $method->invoke($this->app->make(UpdatePanel::class));

        $onDisk = (require base_path('config/panel.php'))['version'];

        $this->assertSame($onDisk, config('panel.version'));
        $this->assertNotSame('0.0.1-stale', config('panel.version'));
    }

    /**
     * The terminal daemon is the whole of the browser shell, so an update that
     * leaves it on the old code — or does not bring it back at all — reads as
     * "the terminal broke in this release".
     */
    public function test_the_restart_covers_every_daemon_and_reloads_their_units(): void
    {
        $command = new \ReflectionClass(\App\Console\Commands\UpdatePanel::class);
        $method = $command->getMethod('restartScript');
        $method->setAccessible(true);

        $script = $method->invoke($command->newInstanceWithoutConstructor());

        foreach ([
            'ubuntu-panel-queue.service',
            'ubuntu-panel-terminal.service',
            'ubuntu-panel-scheduler.timer',
        ] as $unit) {
            $this->assertStringContainsString($unit, $script);
        }

        // Units are written by install.sh, and re-running the installer is a
        // documented way to update — restarting without reloading starts the
        // old definition of a unit that has since changed.
        $this->assertStringContainsString('systemctl daemon-reload', $script);

        // A unit that ended up disabled has to come back, not just restart.
        $this->assertStringContainsString('enable --now', $script);

        // FPM holds the compiled config in opcache; the code alone is not enough.
        $this->assertStringContainsString('-fpm', $script);

        // It runs detached, so without this nobody can find out what happened.
        $this->assertStringContainsString(\App\Console\Commands\UpdatePanel::RESTART_LOG, $script);
    }
}
