<script setup>
import { computed, ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { ago, browser, card, field, smallButton, when } from './ui';
import { router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    logins: Object,
});

const form = useForm({
    max_attempts: props.logins.max_attempts,
    lockout_minutes: props.logins.lockout_minutes,
});

const save = () => form.post(route('security.login-protection'), { preserveScroll: true });

const unlock = (ip) => router.post(route('security.logins.unlock'), { ip }, { preserveScroll: true });

const filter = ref('all');

const attempts = computed(() =>
    props.logins.attempts.filter(
        (a) => filter.value === 'all' || (filter.value === 'failed' ? !a.success : a.success),
    ),
);

const reasonLabel = {
    password: 'Password',
    two_factor: 'Two-factor code',
    recovery: 'Recovery code',
    locked: 'Refused: locked out',
};
</script>

<template>
    <div class="space-y-6">
        <div class="grid gap-6 lg:grid-cols-3">
            <div :class="card">
                <h3 class="font-semibold text-slate-900">Last 24 hours</h3>
                <dl class="mt-4 grid grid-cols-2 gap-4 text-center">
                    <div>
                        <dt class="text-xs text-slate-500">Signed in</dt>
                        <dd class="text-2xl font-semibold text-emerald-600">{{ logins.succeeded_24h }}</dd>
                    </div>
                    <div>
                        <dt class="text-xs text-slate-500">Failed</dt>
                        <dd class="text-2xl font-semibold text-rose-600">{{ logins.failed_24h }}</dd>
                    </div>
                </dl>
            </div>

            <div :class="card">
                <h3 class="font-semibold text-slate-900">Lockout</h3>
                <form class="mt-3 space-y-3" @submit.prevent="save">
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Failed sign-ins</label>
                            <input v-model.number="form.max_attempts" type="number" min="1" max="50" :class="field" />
                            <InputError class="mt-1" :message="form.errors.max_attempts" />
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Lock for (minutes)</label>
                            <input v-model.number="form.lockout_minutes" type="number" min="1" :class="field" />
                            <InputError class="mt-1" :message="form.errors.lockout_minutes" />
                        </div>
                    </div>
                    <PrimaryButton :disabled="form.processing">Save</PrimaryButton>
                </form>
            </div>

            <div :class="card">
                <h3 class="font-semibold text-slate-900">Locked out now</h3>
                <ul class="mt-3 divide-y divide-slate-100">
                    <li v-for="entry in logins.locked" :key="entry.ip" class="flex items-center justify-between py-2">
                        <span>
                            <span class="font-mono text-sm">{{ entry.ip }}</span>
                            <span class="block text-xs text-slate-500">until {{ when(entry.until) }}</span>
                        </span>
                        <button type="button" :class="smallButton" class="text-slate-700" @click="unlock(entry.ip)">Unlock</button>
                    </li>
                    <li v-if="!logins.locked.length" class="py-4 text-sm text-slate-400">Nobody is locked out.</li>
                </ul>
            </div>
        </div>

        <div :class="card" class="!p-0">
            <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-6 py-4">
                <div>
                    <h3 class="font-semibold text-slate-900">Panel sign-in history</h3>
                    <p class="text-sm text-slate-500">Every attempt to sign in to this panel, kept for 90 days.</p>
                </div>
                <select v-model="filter" class="rounded-lg border-slate-300 text-sm shadow-sm">
                    <option value="all">All</option>
                    <option value="failed">Failures</option>
                    <option value="success">Successful</option>
                </select>
            </div>
            <div class="max-h-[32rem] overflow-y-auto">
                <table class="w-full text-sm">
                    <thead class="sticky top-0 bg-white text-left text-xs uppercase text-slate-400">
                        <tr>
                            <th class="px-6 py-2">Time</th>
                            <th class="px-2 py-2">Result</th>
                            <th class="px-2 py-2">Email typed</th>
                            <th class="px-2 py-2">Address</th>
                            <th class="hidden px-2 py-2 md:table-cell">Browser</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="attempt in attempts" :key="attempt.id">
                            <td class="whitespace-nowrap px-6 py-2 text-slate-500" :title="when(attempt.created_at)">{{ ago(attempt.created_at) }}</td>
                            <td class="px-2 py-2">
                                <span
                                    class="rounded-full px-2 py-0.5 text-xs font-medium"
                                    :class="attempt.success ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'"
                                >
                                    {{ attempt.success ? 'Signed in' : 'Failed' }}
                                </span>
                                <span class="ml-1 text-xs text-slate-400">{{ reasonLabel[attempt.reason] ?? attempt.reason }}</span>
                            </td>
                            <td class="px-2 py-2">{{ attempt.email || '—' }}</td>
                            <td class="px-2 py-2 font-mono">{{ attempt.ip }}</td>
                            <td class="hidden px-2 py-2 text-slate-500 md:table-cell" :title="attempt.user_agent">{{ browser(attempt.user_agent) }}</td>
                        </tr>
                        <tr v-if="!attempts.length">
                            <td colspan="5" class="px-6 py-8 text-center text-slate-400">No sign-in attempts recorded yet.</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>
</template>
