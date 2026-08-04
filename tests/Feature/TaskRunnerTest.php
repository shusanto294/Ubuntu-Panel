<?php

namespace Tests\Feature;

use App\Models\ActivityLog;
use App\Services\Shell\LocalConnection;
use App\Models\User;
use App\Services\Tasks\Step;
use App\Services\Tasks\TaskRunner;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Support\FakeLocalConnection;
use Tests\TestCase;

class TaskRunnerTest extends TestCase
{
    use RefreshDatabase;

    protected function makeLog(): ActivityLog
    {
        return ActivityLog::create([
            'type' => 'provision',
            'action' => 'test.run',
            'status' => 'running',
        ]);
    }

    public function test_it_records_progress_after_every_step(): void
    {
        $log = $this->makeLog();
        $ssh = new FakeLocalConnection();

        $ok = TaskRunner::for($log, $ssh)->run([
            Step::make('First', ['echo one']),
            Step::make('Second', ['echo two', 'echo three']),
        ]);

        $log->refresh();

        $this->assertTrue($ok);
        $this->assertSame('success', $log->status);
        $this->assertSame(100, (int) $log->progress);
        $this->assertNull($log->current_step);
        $this->assertNotNull($log->finished_at);
        $this->assertSame(['success', 'success'], array_column($log->steps, 'status'));
        $this->assertSame(['echo one', 'echo two', 'echo three'], $ssh->ran);
        $this->assertStringContainsString('== First', $log->output);
        $this->assertStringContainsString('$ echo two', $log->output);
    }

    public function test_a_failing_step_stops_the_task_and_is_marked_failed(): void
    {
        $log = $this->makeLog();
        $ssh = new FakeLocalConnection(['bad-command' => ['permission denied', 1]]);

        $ok = TaskRunner::for($log, $ssh)->run([
            Step::make('First', ['echo one']),
            Step::make('Second', ['bad-command']),
            Step::make('Third', ['echo never']),
        ]);

        $log->refresh();

        $this->assertFalse($ok);
        $this->assertSame('failed', $log->status);
        $this->assertSame(['success', 'failed', 'pending'], array_column($log->steps, 'status'));
        $this->assertNotContains('echo never', $ssh->ran);
        $this->assertStringContainsString('permission denied', $log->output);
    }

    public function test_an_optional_step_is_skipped_rather_than_fatal(): void
    {
        $log = $this->makeLog();
        $ssh = new FakeLocalConnection(['ufw' => ['ufw not available', 1]]);

        $ok = TaskRunner::for($log, $ssh)->run([
            Step::make('Firewall', ['ufw enable'], optional: true),
            Step::make('Finish', ['echo done']),
        ]);

        $log->refresh();

        $this->assertTrue($ok);
        $this->assertSame(['skipped', 'success'], array_column($log->steps, 'status'));
    }

    public function test_callback_steps_can_write_files_and_return_output(): void
    {
        $log = $this->makeLog();
        $ssh = new FakeLocalConnection();

        TaskRunner::for($log, $ssh)->run([
            Step::call('Write config', function (LocalConnection $connection) {
                $connection->putFile('/etc/nginx/sites-available/app', 'server {}');

                return 'config written';
            }),
        ]);

        $this->assertSame('server {}', $ssh->files['/etc/nginx/sites-available/app']);
        $this->assertStringContainsString('config written', $log->refresh()->output);
    }
}
