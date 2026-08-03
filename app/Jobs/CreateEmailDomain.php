<?php

namespace App\Jobs;

use App\Models\EmailDomain;
use App\Services\Mail\MailManager;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

class CreateEmailDomain implements ShouldQueue
{
    use Queueable;

    public int $timeout = 600;

    public int $tries = 1;

    public function __construct(public EmailDomain $domain) {}

    public function handle(MailManager $manager): void
    {
        $manager->createDomain($this->domain);
    }
}
