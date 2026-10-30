<script setup>
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { confirmDanger } from '@/dialog';
import { ago, browser, card, smallButton } from './ui';
import { router } from '@inertiajs/vue3';

const props = defineProps({
    sessions: Array,
});

const { page, pages, total, perPage, visible: visibleSessions } = usePagination(() => props.sessions, 10);

const revoke = (key) => router.delete(route('security.sessions.destroy', key), { preserveScroll: true });

const revokeOthers = async () => {
    if (!(await confirmDanger('Sign out everywhere else?', 'Every other browser signed in to your account will have to sign in again.', 'Sign out others')))
        return;
    router.post(route('security.sessions.others'), {}, { preserveScroll: true });
};
</script>

<template>
    <div :class="card" class="!p-0">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-6 py-4">
            <div>
                <h3 class="font-semibold text-slate-900">Signed-in browsers</h3>
                <p class="text-sm text-slate-500">Every browser signed in to your panel account.</p>
            </div>
            <button
                v-if="sessions.length > 1"
                type="button"
                class="rounded-xl px-4 py-2 text-sm font-medium text-rose-600 ring-1 ring-rose-200 hover:bg-rose-50"
                @click="revokeOthers"
            >
                Sign out all others
            </button>
        </div>
        <ul class="divide-y divide-slate-100">
            <li v-for="entry in visibleSessions" :key="entry.key" class="flex items-center gap-4 px-6 py-4">
                <div class="min-w-0 flex-1">
                    <p class="text-sm font-medium text-slate-900">
                        {{ browser(entry.user_agent) }}
                        <span v-if="entry.current" class="ml-1 rounded-full bg-emerald-50 px-2 py-0.5 text-xs text-emerald-700">this browser</span>
                    </p>
                    <p class="text-xs text-slate-500">
                        <IpAddress v-if="entry.ip" :ip="entry.ip" /><span v-else>unknown address</span> · active {{ ago(entry.last_activity) }}
                    </p>
                </div>
                <button v-if="!entry.current" type="button" :class="smallButton" class="text-rose-600" @click="revoke(entry.key)">
                    Sign out
                </button>
            </li>
        </ul>
        <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
    </div>
</template>
