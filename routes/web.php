<?php

use App\Http\Controllers\CloudflareAccountController;
use App\Http\Controllers\DatabaseController;
use App\Http\Controllers\EmailController;
use App\Http\Controllers\ProfileController;
use App\Http\Controllers\SiteController;
use App\Http\Controllers\SystemController;
use App\Http\Controllers\TaskController;
use App\Http\Controllers\TerminalController;
use Illuminate\Support\Facades\Route;
use Inertia\Inertia;

Route::get('/', function () {
    return Inertia::render('Welcome', [
        'canLogin' => Route::has('login'),
        'canRegister' => Route::has('register'),
    ]);
})->name('home');

Route::middleware(['auth'])->group(function () {
    // This machine
    Route::get('/dashboard', [SystemController::class, 'overview'])->name('dashboard');
    Route::get('system/metrics', [SystemController::class, 'metrics'])->name('system.metrics');
    Route::get('system/credentials', [SystemController::class, 'credentials'])->name('system.credentials');
    Route::patch('system/settings', [SystemController::class, 'updateSettings'])->name('system.settings');
    Route::get('system/updates', [SystemController::class, 'checkForUpdates'])->name('system.updates');

    // Software
    Route::get('services', [SystemController::class, 'services'])->name('services.index');
    Route::post('services/detect', [SystemController::class, 'detectServices'])->name('services.detect');
    Route::post('services/install', [SystemController::class, 'installServices'])->name('services.install-many');
    Route::post('services/{service}/install', [SystemController::class, 'installService'])->name('services.install');
    Route::post('services/{service}/retry', [SystemController::class, 'retryService'])->name('services.retry');

    // Terminal
    Route::post('terminal/ticket', [TerminalController::class, 'ticket'])->name('terminal.ticket');
    Route::get('terminal', fn () => Inertia::render('System/Terminal'))->name('terminal');

    // Live task console
    Route::get('tasks/latest', [TaskController::class, 'latest'])->name('tasks.latest');
    Route::get('tasks/{task}', [TaskController::class, 'show'])->name('tasks.show');

    // Sites
    Route::resource('sites', SiteController::class)->except(['edit', 'update']);
    Route::post('sites/{site}/dns-sync', [SiteController::class, 'syncDns'])->name('sites.dns-sync');
    Route::post('sites/{site}/redeploy', [SiteController::class, 'redeploy'])->name('sites.redeploy');
    Route::post('sites/{site}/pull', [SiteController::class, 'pull'])->name('sites.pull');
    Route::post('sites/{site}/restart', [SiteController::class, 'restart'])->name('sites.restart');

    // Databases
    Route::get('databases', [DatabaseController::class, 'index'])->name('databases.index');
    Route::post('databases', [DatabaseController::class, 'store'])->name('databases.store');
    Route::delete('databases/{database}', [DatabaseController::class, 'destroy'])->name('databases.destroy');
    Route::get('databases/{database}/credentials', [DatabaseController::class, 'credentials'])->name('databases.credentials');

    // Email
    Route::get('email', [EmailController::class, 'index'])->name('email.index');
    Route::post('email/domains', [EmailController::class, 'storeDomain'])->name('email.domains.store');
    Route::delete('email/domains/{domain}', [EmailController::class, 'destroyDomain'])->name('email.domains.destroy');
    Route::post('email/domains/{domain}/dns', [EmailController::class, 'syncDomainDns'])->name('email.domains.dns');
    Route::post('email/domains/{domain}/accounts', [EmailController::class, 'storeAccount'])->name('email.accounts.store');
    Route::delete('email/accounts/{account}', [EmailController::class, 'destroyAccount'])->name('email.accounts.destroy');

    // Cloudflare
    Route::get('cloudflare', [CloudflareAccountController::class, 'index'])->name('cloudflare.index');
    Route::post('cloudflare', [CloudflareAccountController::class, 'store'])->name('cloudflare.store');
    Route::patch('cloudflare/{account}', [CloudflareAccountController::class, 'update'])->name('cloudflare.update');
    Route::delete('cloudflare/{account}', [CloudflareAccountController::class, 'destroy'])->name('cloudflare.destroy');
    Route::post('cloudflare/{account}/verify', [CloudflareAccountController::class, 'verify'])->name('cloudflare.verify');
    Route::get('cloudflare/{account}/zones', [CloudflareAccountController::class, 'zones'])->name('cloudflare.zones');

    // Profile
    Route::get('/profile', [ProfileController::class, 'edit'])->name('profile.edit');
    Route::patch('/profile', [ProfileController::class, 'update'])->name('profile.update');
    Route::delete('/profile', [ProfileController::class, 'destroy'])->name('profile.destroy');
});

require __DIR__.'/auth.php';
