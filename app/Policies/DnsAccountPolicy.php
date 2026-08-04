<?php

namespace App\Policies;

use App\Models\DnsAccount;
use App\Models\User;

class DnsAccountPolicy
{
    public function view(User $user, DnsAccount $account): bool
    {
        return $account->user_id === $user->id;
    }

    public function update(User $user, DnsAccount $account): bool
    {
        return $account->user_id === $user->id;
    }

    public function delete(User $user, DnsAccount $account): bool
    {
        return $account->user_id === $user->id;
    }
}
