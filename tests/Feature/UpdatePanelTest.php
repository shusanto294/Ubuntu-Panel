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
}
