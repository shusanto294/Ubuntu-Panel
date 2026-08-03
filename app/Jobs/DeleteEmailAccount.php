<?php

namespace App\Jobs;

use App\Models\EmailAccount;
use App\Services\Mail\MailManager;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

class DeleteEmailAccount implements ShouldQueue
{
    use Queueable;

    public int $timeout = 300;

    public int $tries = 1;

    public function __construct(public EmailAccount $account, public bool $deleteMail = true) {}

    public function handle(MailManager $manager): void
    {
        $manager->deleteAccount($this->account, $this->deleteMail);
    }
}
