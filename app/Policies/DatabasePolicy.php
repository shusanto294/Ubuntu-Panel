<?php

namespace App\Policies;

use App\Models\Database;
use App\Models\User;

class DatabasePolicy
{
    public function view(User $user, Database $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function update(User $user, Database $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function delete(User $user, Database $model): bool
    {
        return $model->user_id === $user->id;
    }
}
