<?php

namespace App\Policies;

use App\Models\CloudflareAccount;
use App\Models\User;

class CloudflareAccountPolicy
{
    public function view(User $user, CloudflareAccount $account): bool
    {
        return $account->user_id === $user->id;
    }

    public function update(User $user, CloudflareAccount $account): bool
    {
        return $account->user_id === $user->id;
    }

    public function delete(User $user, CloudflareAccount $account): bool
    {
        return $account->user_id === $user->id;
    }
}
