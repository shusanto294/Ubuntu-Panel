<script setup>
import { ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Pagination from '@/Components/Pagination.vue';
import { confirmDanger } from '@/dialog';
import { formatRelative, formatStamp } from '@/time';
import { Head, Link, router } from '@inertiajs/vue3';

/*
 * Everything the panel told its owner, newest first: attacks, sign-ins, SSH,
 * server problems, failed tasks. Kept whether or not email is set up.
 */
const props = defineProps({
    notifications: { type: Array, default: () => [] },
    filters: { type: Object, default: () => ({ filter: 'all', level: '' }) },
    pagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0, per_page: 20 }) },
    unreadTotal: { type: Number, default: 0 },
    smtpReady: { type: Boolean, default: false },
});

const filter = ref(props.filters.filter ?? 'all');
const level = ref(props.filters.level ?? '');

const visit = (page = 1) => {
    const query = {};
    if (filter.value !== 'all') query.filter = filter.value;
    if (level.value) query.level = level.value;
    if (page > 1) query.page = page;

    router.get(route('notifications.index'), query, { preserveScroll: true, preserveState: true });
};

const markAll = () => router.post(route('notifications.read'), { all: '1' }, { preserveScroll: true });

const openOne = (n) => {
    if (!n.read_at) {
        router.post(route('notifications.read'), { ids: String(n.id) }, {
            preserveScroll: true,
            onSuccess: () => n.link && router.visit(n.link),
        });
        return;
    }
    if (n.link) router.visit(n.link);
};

const markOne = (n) => router.post(route('notifications.read'), { ids: String(n.id) }, { preserveScroll: true });

const clear = async (which) => {
    const text =
        which === 'all'
            ? 'Every notification is deleted, read or not. It cannot be undone.'
            : 'The notifications you have read are deleted. It cannot be undone.';
    if (!(await confirmDanger(which === 'all' ? 'Clear every notification?' : 'Clear read notifications?', text, 'Clear')))
        return;

    router.post(route('notifications.clear'), { which }, { preserveScroll: true });
};

const LEVEL = {
    critical: { label: 'Critical', badge: 'bg-rose-600 text-white', dot: 'bg-rose-500' },
    warning: { label: 'Warning', badge: 'bg-amber-50 text-amber-700 ring-1 ring-amber-200', dot: 'bg-amber-500' },
    info: { label: 'Info', badge: 'bg-sky-50 text-sky-700 ring-1 ring-sky-200', dot: 'bg-sky-500' },
};

const expanded = ref(new Set());
const toggle = (id) => {
    const next = new Set(expanded.value);
    next.has(id) ? next.delete(id) : next.add(id);
    expanded.value = next;
};
</script>

<template>
    <Head title="Notifications" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <h2 class="text-lg font-semibold text-slate-900">
                    Notifications
                    <span v-if="unreadTotal" class="ml-1 text-sm font-normal text-slate-500">· {{ unreadTotal }} unread</span>
                </h2>
                <div class="flex flex-wrap gap-2">
                    <button
                        v-if="unreadTotal"
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="markAll"
                    >
                        Mark all as read
                    </button>
                    <button
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="clear('read')"
                    >
                        Clear read
                    </button>
                    <button
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-rose-600 ring-1 ring-slate-900/10 transition hover:bg-rose-50"
                        @click="clear('all')"
                    >
                        Clear all
                    </button>
                </div>
            </div>
        </template>

        <div class="space-y-4">
            <div class="flex flex-wrap items-end gap-3 rounded-2xl bg-white p-4 shadow-sm ring-1 ring-slate-900/5">
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">Show</span>
                    <select v-model="filter" class="mt-1 block rounded-lg border-slate-300 text-sm shadow-sm" @change="visit()">
                        <option value="all">All notifications</option>
                        <option value="unread">Unread only</option>
                    </select>
                </label>
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">Level</span>
                    <select v-model="level" class="mt-1 block rounded-lg border-slate-300 text-sm shadow-sm" @change="visit()">
                        <option value="">Every level</option>
                        <option value="critical">Critical</option>
                        <option value="warning">Warning</option>
                        <option value="info">Info</option>
                    </select>
                </label>
                <p class="ml-auto text-xs text-slate-500">
                    <template v-if="smtpReady">Each one is emailed too.</template>
                    <template v-else>
                        Not emailed: no SMTP server is connected.
                        <Link :href="route('settings')" class="font-medium text-brand-600 hover:underline">Connect one</Link>
                    </template>
                    · <Link :href="`${route('security.index')}?tab=alerts`" class="font-medium text-brand-600 hover:underline">Choose what you are told about</Link>
                </p>
            </div>

            <div class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <p v-if="!notifications.length" class="px-5 py-12 text-center text-sm text-slate-500">
                    {{ filter === 'unread' ? 'Nothing unread.' : 'Nothing yet. Attacks, new sign-ins, server problems and failed tasks show up here.' }}
                </p>

                <ul v-else class="divide-y divide-slate-100">
                    <li
                        v-for="n in notifications"
                        :key="n.id"
                        class="flex gap-4 px-5 py-4"
                        :class="n.read_at ? '' : 'bg-brand-50/40'"
                    >
                        <span class="mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full" :class="LEVEL[n.level]?.dot ?? LEVEL.info.dot" />
                        <div class="min-w-0 flex-1">
                            <div class="flex flex-wrap items-center gap-2">
                                <p class="text-sm text-slate-900" :class="n.read_at ? '' : 'font-semibold'">{{ n.title }}</p>
                                <span class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase" :class="LEVEL[n.level]?.badge ?? LEVEL.info.badge">
                                    {{ LEVEL[n.level]?.label ?? n.level }}
                                </span>
                            </div>
                            <p
                                v-if="n.body"
                                class="mt-1 whitespace-pre-line break-words text-sm text-slate-600"
                                :class="expanded.has(n.id) ? '' : 'line-clamp-3'"
                            >{{ n.body }}</p>
                            <div class="mt-1.5 flex flex-wrap items-center gap-3 text-xs">
                                <span class="text-slate-400" :title="formatStamp(n.created_at)">{{ formatRelative(n.created_at) }}</span>
                                <button
                                    v-if="n.body && (n.body.length > 200 || n.body.split('\n').length > 3)"
                                    type="button"
                                    class="font-medium text-brand-600 hover:underline"
                                    @click="toggle(n.id)"
                                >
                                    {{ expanded.has(n.id) ? 'Less' : 'More' }}
                                </button>
                                <button v-if="n.link" type="button" class="font-medium text-brand-600 hover:underline" @click="openOne(n)">
                                    Open
                                </button>
                                <button v-if="!n.read_at" type="button" class="font-medium text-slate-500 hover:underline" @click="markOne(n)">
                                    Mark as read
                                </button>
                            </div>
                        </div>
                    </li>
                </ul>

                <Pagination
                    :model-value="pagination.page"
                    :pages="pagination.pages"
                    :total="pagination.total"
                    :per-page="pagination.per_page"
                    @update:model-value="visit"
                />
            </div>
        </div>
    </AuthenticatedLayout>
</template>
