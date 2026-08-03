<?php

namespace App\Policies;

use App\Models\ActivityLog;
use App\Models\User;

class ActivityLogPolicy
{
    public function view(User $user, ActivityLog $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function update(User $user, ActivityLog $model): bool
    {
        return $model->user_id === $user->id;
    }

    public function delete(User $user, ActivityLog $model): bool
    {
        return $model->user_id === $user->id;
    }
}
