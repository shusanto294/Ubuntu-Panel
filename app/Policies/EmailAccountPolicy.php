<?php

namespace App\Policies;

use App\Models\EmailAccount;
use App\Models\User;

class EmailAccountPolicy
{
    public function view(User $user, EmailAccount $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function update(User $user, EmailAccount $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function delete(User $user, EmailAccount $model): bool
    {
        return $model->user_id === $user->id;
    }
}
