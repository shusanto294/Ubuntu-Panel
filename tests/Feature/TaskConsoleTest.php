<?php

namespace Tests\Feature;

use App\Models\ActivityLog;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class TaskConsoleTest extends TestCase
{
    use RefreshDatabase;

    protected function task(User $user, array $attributes = []): ActivityLog
    {
        return ActivityLog::create(array_merge([
            'user_id' => $user->id,
            'type' => 'provision',
            'action' => 'server.provision',
            'status' => 'running',
            'output' => "line one\nline two\n",
            'steps' => [
                ['name' => 'Install nginx', 'status' => 'success'],
                ['name' => 'Install PHP', 'status' => 'running'],
            ],
            'current_step' => 'Install PHP',
            'progress' => 50,
        ], $attributes));
    }

    public function test_a_task_reports_its_progress_and_steps(): void
    {
        $user = User::factory()->create();
        $task = $this->task($user);

        $this->actingAs($user)
            ->getJson(route('tasks.show', $task))
            ->assertOk()
            ->assertJsonPath('status', 'running')
            ->assertJsonPath('progress', 50)
            ->assertJsonPath('current_step', 'Install PHP')
            ->assertJsonPath('running', true)
            ->assertJsonPath('steps.0.name', 'Install nginx')
            ->assertJsonPath('output', "line one\nline two\n");
    }

    public function test_polling_with_an_offset_only_returns_new_output(): void
    {
        $user = User::factory()->create();
        $task = $this->task($user);

        $this->actingAs($user)
            ->getJson(route('tasks.show', $task).'?offset=9')
            ->assertOk()
            ->assertJsonPath('output', "line two\n")
            ->assertJsonPath('offset', strlen("line one\nline two\n"));
    }

    public function test_another_user_cannot_read_a_task(): void
    {
        $owner = User::factory()->create();
        $intruder = User::factory()->create();
        $task = $this->task($owner);

        $this->actingAs($intruder)->getJson(route('tasks.show', $task))->assertForbidden();
    }

    public function test_the_latest_task_can_be_looked_up_per_server(): void
    {
        $user = User::factory()->create();

        $this->task($user, ['server_id' => null, 'action' => 'first']);
        $this->task($user, ['server_id' => null, 'action' => 'second']);

        $this->actingAs($user)
            ->getJson(route('tasks.latest', ['server' => null]))
            ->assertOk()
            ->assertJsonPath('action', 'second');
    }

    public function test_output_is_trimmed_once_it_exceeds_the_cap(): void
    {
        $user = User::factory()->create();
        $task = $this->task($user, ['output' => '']);

        $task->appendOutput(str_repeat('x', ActivityLog::MAX_OUTPUT + 500));

        $this->assertLessThanOrEqual(ActivityLog::MAX_OUTPUT + 30, strlen($task->output));
        $this->assertStringStartsWith('…output truncated…', $task->output);
    }
}
