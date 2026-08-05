<?php

namespace App\Http\Controllers;

use App\Models\ActivityLog;
use App\Jobs\CreateDatabase;
use App\Jobs\DeleteDatabase;
use App\Models\Database;
use App\Models\Service;
use App\Services\System\PhpMyAdmin;
use App\Services\Shell\LocalConnection;
use App\Services\System\ServiceInstaller;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Throwable;

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
            'phpMyAdmin' => app(PhpMyAdmin::class)->isInstalled(),
            // Whatever the queue is working on right now, so the page can show
            // the output instead of a badge that never changes.
            'activeTask' => ActivityLog::whereIn('type', ['database'])
                ->where('status', 'running')
                ->latest('id')
                ->first()?->toConsolePayload(),
        ]);
    }

    /**
     * The create form on a page of its own.
     *
     * It used to sit beside the list, taking a third of the width whether or
     * not you were creating anything — and the list, which is the reason you
     * came, got what was left.
     */
    public function create()
    {
        $this->installer->refresh(Service::ENGINE_KEYS);

        return Inertia::render('Databases/Create', [
            'availableEngines' => Service::availableEngines(),
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

        return redirect()->route('databases.index')->with('success', 'Database queued for creation.');
    }

    public function destroy(Request $request, Database $database)
    {
        $this->authorize('delete', $database);

        DeleteDatabase::dispatch($database);

        return back()->with('success', 'Database queued for deletion.');
    }

    /**
     * Open phpMyAdmin signed in over every database on the server.
     *
     * The credentials go into a session phpMyAdmin reads and are never in the
     * URL. Anyone who has not come through here gets the ordinary login form
     * instead, where a database's own username and password reach that
     * database and nothing else.
     */
    public function phpMyAdmin(Request $request, PhpMyAdmin $phpMyAdmin)
    {
        if (! $phpMyAdmin->isInstalled()) {
            return back()->with('error', 'phpMyAdmin is not installed — add it from the Services page.');
        }

        if (! Service::installed('mysql')) {
            return back()->with('error', 'MariaDB is not installed on this machine.');
        }

        // Asserted here rather than only at install time, so a machine that
        // installed phpMyAdmin before this account existed does not need the
        // service reinstalling to get it.
        try {
            $phpMyAdmin->ensureAdminUser(app(LocalConnection::class));
        } catch (Throwable $e) {
            return back()->with('error', 'Could not prepare the phpMyAdmin account: '.$e->getMessage());
        }

        return redirect()->away($phpMyAdmin->signOnAsAdmin());
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
