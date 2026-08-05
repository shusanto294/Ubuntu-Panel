<?php

namespace App\Http\Controllers;

use App\Models\DnsAccount;
use App\Jobs\CreateEmailAccount;
use App\Jobs\CreateEmailDomain;
use App\Jobs\DeleteEmailAccount;
use App\Jobs\DeleteEmailDomain;
use App\Models\EmailAccount;
use App\Models\EmailDomain;
use App\Support\Settings;
use App\Services\Mail\MailManager;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Inertia\Inertia;
use Throwable;

class EmailController extends Controller
{
    public function __construct(protected Settings $settings) {}

    public function index(Request $request)
    {
        $domains = EmailDomain::query()
            ->with('accounts')
            ->latest()
            ->get()
            ->map(fn (EmailDomain $domain) => $this->summary($domain));

        return Inertia::render('Email/Index', [
            'domains' => $domains,
            // Mail only works once Postfix/Dovecot are installed here.
            'mailConfigured' => $this->settings->boolean('mail_configured'),
            'mailHostname' => $this->settings->get('mail_hostname'),
            'dnsAccounts' => $request->user()->dnsAccounts()->get()->map(fn (DnsAccount $account) => $account->toPanelArray()),
        ]);
    }

    /**
     * Adding a domain, and adding a mailbox to one, are pages rather than
     * forms wedged beside the list. Both ask enough questions to deserve the
     * room, and neither is what you came to this section to look at.
     */
    public function createDomain(Request $request)
    {
        return Inertia::render('Email/CreateDomain', [
            'mailConfigured' => $this->settings->boolean('mail_configured'),
            'dnsAccounts' => $request->user()->dnsAccounts()->get()
                ->map(fn (DnsAccount $account) => $account->toPanelArray()),
        ]);
    }

    public function createAccount(Request $request, EmailDomain $domain)
    {
        $this->authorize('update', $domain);

        return Inertia::render('Email/CreateAccount', [
            'domain' => $this->summary($domain->load('accounts')),
        ]);
    }

    public function storeDomain(Request $request)
    {
        $data = $request->validate([
            'domain' => ['required', 'string', 'max:255', 'regex:/^[a-z0-9.-]+\.[a-z]{2,}$/i'],
            'dkim_selector' => ['nullable', 'string', 'max:32', 'regex:/^[a-z0-9]+$/i'],
            'manage_dns' => ['boolean'],
            'dns_account_id' => [
                'nullable', 'integer',
                Rule::exists('dns_accounts', 'id')->where('user_id', $request->user()->id),
                Rule::requiredIf(fn () => $request->boolean('manage_dns')),
            ],
        ]);

        // Nothing to authorise against: a mail domain has no owner until this
        // creates one, and the panel has a single account behind `auth`.
        if (! $this->settings->boolean('mail_configured')) {
            return back()->withErrors(['domain' => 'The mail server is not installed yet — install it from the Software page.']);
        }

        $domain = strtolower($data['domain']);

        if (EmailDomain::where('domain', $domain)->exists()) {
            return back()->withErrors(['domain' => 'That domain is already set up on this server.']);
        }

        $record = EmailDomain::create([
            'user_id' => $request->user()->id,
            'dns_account_id' => ($data['manage_dns'] ?? false) ? ($data['dns_account_id'] ?? null) : null,
            'domain' => $domain,
            'dkim_selector' => $data['dkim_selector'] ?: 'mail',
            'manage_dns' => $data['manage_dns'] ?? false,
            'status' => 'pending',
        ]);

        CreateEmailDomain::dispatch($record);

        return redirect()->route('email.index')
            ->with('success', 'Mail domain queued. DKIM keys and DNS records are being set up.');
    }

    public function destroyDomain(Request $request, EmailDomain $domain)
    {
        $this->authorize('delete', $domain);

        DeleteEmailDomain::dispatch($domain);

        return back()->with('success', 'Mail domain queued for removal.');
    }

    public function syncDomainDns(Request $request, EmailDomain $domain, MailManager $mail)
    {
        $this->authorize('update', $domain);

        if (! $domain->manage_dns) {
            return back()->with('error', 'DNS management is not enabled for this mail domain.');
        }

        try {
            $mail->publishDns($domain);

            return back()->with('success', 'Mail DNS records published to Cloudflare.');
        } catch (Throwable $e) {
            return back()->with('error', 'DNS publish failed: '.$e->getMessage());
        }
    }

    public function storeAccount(Request $request, EmailDomain $domain)
    {
        $this->authorize('update', $domain);

        $data = $request->validate([
            'local_part' => ['required', 'string', 'max:64', 'regex:/^[a-z0-9._-]+$/i'],
            'password' => ['required', 'string', 'min:10', 'max:100'],
            'quota_mb' => ['required', 'integer', 'min:64', 'max:102400'],
        ]);

        $localPart = strtolower($data['local_part']);

        if ($domain->accounts()->where('local_part', $localPart)->exists()) {
            return back()->withErrors(['local_part' => 'That address already exists on this domain.']);
        }

        $account = $domain->accounts()->create([
            'user_id' => $request->user()->id,
            'local_part' => $localPart,
            'password' => $data['password'],
            'quota_mb' => $data['quota_mb'],
            'status' => 'pending',
        ]);

        CreateEmailAccount::dispatch($account, $data['password']);

        return redirect()->route('email.index')->with('success', 'Mailbox queued for creation.');
    }

    public function destroyAccount(Request $request, EmailAccount $account)
    {
        $this->authorize('delete', $account);

        DeleteEmailAccount::dispatch($account, $request->boolean('delete_mail', true));

        return back()->with('success', 'Mailbox queued for deletion.');
    }

    protected function summary(EmailDomain $domain): array
    {
        $mailHost = $this->settings->get('mail_hostname') ?: 'mail.'.$domain->domain;

        return [
            'id' => $domain->id,
            'domain' => $domain->domain,
            'status' => $domain->status,
            'manage_dns' => $domain->manage_dns,
            'dkim_selector' => $domain->dkim_selector,
            'dkim_public_key' => $domain->dkim_public_key,
            'last_error' => $domain->last_error,
            'mail_hostname' => $mailHost,
            'accounts' => $domain->accounts->map(fn (EmailAccount $account) => [
                'id' => $account->id,
                'address' => $account->local_part.'@'.$domain->domain,
                'local_part' => $account->local_part,
                'quota_mb' => $account->quota_mb,
                'status' => $account->status,
                'last_error' => $account->last_error,
            ])->values(),
            // What the user needs to plug into a mail client.
            'client_settings' => [
                'imap' => ['host' => $mailHost, 'port' => 993, 'security' => 'SSL/TLS'],
                'smtp' => ['host' => $mailHost, 'port' => 465, 'security' => 'SSL/TLS'],
                'username' => 'the full email address',
            ],
            'created_at' => $domain->created_at->toDateTimeString(),
        ];
    }
}
