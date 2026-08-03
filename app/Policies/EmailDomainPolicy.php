<?php

namespace App\Policies;

use App\Models\EmailDomain;
use App\Models\User;

class EmailDomainPolicy
{
    public function view(User $user, EmailDomain $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function update(User $user, EmailDomain $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function delete(User $user, EmailDomain $model): bool
    {
        return $model->user_id === $user->id;
    }
}
