<?php

namespace App\Http\Controllers;

use App\Jobs\CreateDatabase;
use App\Jobs\DeleteDatabase;
use App\Models\Database;
use App\Models\Service;
use App\Services\System\ServiceInstaller;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Inertia\Inertia;

class DatabaseController extends Controller
{
    public function __construct(protected ServiceInstaller $installer) {}

    public function index(Request $request)
    {
        $databases = Database::query()
            ->latest()
            ->get()
            ->map(fn (Database $database) => $this->summary($database));

        // Only engines that are actually installed can take a database, and
        // this page is exactly where a stale `failed` row from a half-finished
        // install turns into "you cannot create a database" — on a server that
        // is running MariaDB well enough to be storing this page's session. So
        // ask the machine rather than the record: three `command -v` calls,
        // every time, current by construction.
        $this->installer->refresh(Service::ENGINE_KEYS);

        $engines = Service::availableEngines();

        return Inertia::render('Databases/Index', [
            'databases' => $databases,
            'availableEngines' => $engines,
            'engines' => config('panel.database_engines'),
        ]);
    }

    public function store(Request $request)
    {
        $data = $request->validate([
            'engine' => ['required', Rule::in(array_keys(config('panel.database_engines')))],
            'name' => ['required', 'string', 'max:60', 'regex:/^[a-zA-Z0-9_]+$/'],
            'username' => ['nullable', 'string', 'max:32', 'regex:/^[a-zA-Z0-9_]+$/'],
            'password' => ['nullable', 'string', 'min:8', 'max:100'],
        ]);

        $data += ['username' => null, 'password' => null];

        if (! Service::installed($data['engine'])) {
            return back()->withErrors([
                'engine' => config('panel.database_engines')[$data['engine']].' is not installed on this machine.',
            ]);
        }

        if (Database::where('engine', $data['engine'])->where('name', $data['name'])->exists()) {
            return back()->withErrors(['name' => 'A database with that name already exists.']);
        }

        $database = Database::create([
            'user_id' => $request->user()->id,
            'engine' => $data['engine'],
            'name' => $data['name'],
            'username' => $data['username'] ?: Str::limit($data['name'], 26, '').'_u',
            'password' => $data['password'] ?: Str::password(24, symbols: false),
            'charset' => $data['engine'] === 'mysql' ? 'utf8mb4' : null,
            'status' => 'pending',
        ]);

        CreateDatabase::dispatch($database);

        return back()->with('success', 'Database queued for creation.');
    }

    public function destroy(Request $request, Database $database)
    {
        $this->authorize('delete', $database);

        DeleteDatabase::dispatch($database);

        return back()->with('success', 'Database queued for deletion.');
    }

    /** Reveal the stored password for a database the user owns. */
    public function credentials(Request $request, Database $database)
    {
        $this->authorize('view', $database);

        return response()->json([
            'host' => '127.0.0.1',
            'port' => $database->defaultPort(),
            'database' => $database->name,
            'username' => $database->username,
            'password' => $database->password,
        ]);
    }

    protected function summary(Database $database): array
    {
        return [
            'id' => $database->id,
            'engine' => $database->engine,
            'engine_label' => $database->engineLabel(),
            'name' => $database->name,
            'username' => $database->username,
            'status' => $database->status,
            'port' => $database->defaultPort(),
            'managed_by_site' => $database->managed_by_site,
            'last_error' => $database->last_error,
            'created_at' => $database->created_at->toDateTimeString(),
        ];
    }
}
