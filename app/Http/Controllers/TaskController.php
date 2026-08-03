<?php

namespace App\Http\Controllers;

use App\Models\ActivityLog;
use Illuminate\Http\Request;

/**
 * Polling endpoint behind the live console. Returns the current status, step
 * list and accumulated output for a task.
 */
class TaskController extends Controller
{
    public function show(Request $request, ActivityLog $task)
    {
        $this->authorize('view', $task);

        $payload = $task->toConsolePayload();

        // The console already holds everything up to `offset`; only send the tail.
        $offset = max(0, $request->integer('offset'));
        $payload['offset'] = strlen($payload['output']);
        $payload['output'] = $offset > 0 ? substr($payload['output'], $offset) : $payload['output'];

        return response()->json($payload);
    }

    /** The task currently running for a resource, if any. */
    public function latest(Request $request)
    {
        $query = ActivityLog::where('user_id', $request->user()->id);

        if ($request->filled('server')) {
            $query->where('server_id', $request->integer('server'));
        }

        if ($request->filled('site')) {
            $query->where('site_id', $request->integer('site'));
        }

        $task = $query->latest('id')->first();

        return response()->json($task ? $task->toConsolePayload() : null);
    }
}
