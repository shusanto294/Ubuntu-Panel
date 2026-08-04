<?php

namespace Tests\Feature;

use App\Console\Commands\UpdatePanel;
use Illuminate\Foundation\Testing\RefreshDatabase;
use ReflectionMethod;
use Tests\TestCase;

class UpdatePanelTest extends TestCase
{
    use RefreshDatabase;

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
