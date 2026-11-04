<script setup>
import { computed, ref, watch } from 'vue';
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

// Its own page marks it seen, and has what it is about.
const openOne = (n) => router.visit(route('notifications.show', n.id));

const markOne = (n) => router.post(route('notifications.read'), { ids: String(n.id) }, { preserveScroll: true });

const deleteOne = (n) =>
    router.post(route('notifications.delete'), { ids: String(n.id) }, { preserveScroll: true });

// ---------------------------------------------------------- choosing several

const selected = ref(new Set());

// A new page of the list is a new set to choose from.
watch(
    () => props.notifications,
    () => (selected.value = new Set()),
);

const allChosen = computed(
    () => props.notifications.length > 0 && props.notifications.every((n) => selected.value.has(n.id)),
);

const toggleChoice = (id) => {
    const next = new Set(selected.value);
    next.has(id) ? next.delete(id) : next.add(id);
    selected.value = next;
};

const toggleAll = () => {
    selected.value = allChosen.value ? new Set() : new Set(props.notifications.map((n) => n.id));
};

const chosenIds = () => [...selected.value].join(',');

const markChosen = () =>
    router.post(route('notifications.read'), { ids: chosenIds() }, { preserveScroll: true });

const deleteChosen = async () => {
    const count = selected.value.size;
    if (!(await confirmDanger(`Delete ${count} notification${count === 1 ? '' : 's'}?`, 'It cannot be undone.', 'Delete')))
        return;

    router.post(route('notifications.delete'), { ids: chosenIds() }, { preserveScroll: true });
};

const clear = async (which) => {
    const text =
        which === 'all'
            ? 'Every notification is deleted, seen or not. It cannot be undone.'
            : 'The notifications you have seen are deleted. It cannot be undone.';
    if (!(await confirmDanger(which === 'all' ? 'Delete every notification?' : 'Delete the seen notifications?', text, 'Delete')))
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
                    <span v-if="unreadTotal" class="ml-1 text-sm font-normal text-slate-500">· {{ unreadTotal }} not seen yet</span>
                </h2>
                <div class="flex flex-wrap gap-2">
                    <button
                        v-if="unreadTotal"
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="markAll"
                    >
                        Mark all as seen
                    </button>
                    <button
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                        @click="clear('read')"
                    >
                        Delete seen
                    </button>
                    <button
                        type="button"
                        class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-rose-600 ring-1 ring-slate-900/10 transition hover:bg-rose-50"
                        @click="clear('all')"
                    >
                        Delete all
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
                        <option value="unread">Not seen yet</option>
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
                <!-- Choosing several: the page's own, then seen or deleted at once. -->
                <div
                    v-if="notifications.length"
                    class="flex flex-wrap items-center gap-3 border-b border-slate-100 bg-slate-50 px-5 py-2.5 text-sm"
                >
                    <label class="flex items-center gap-2 text-slate-600">
                        <input
                            type="checkbox"
                            class="rounded border-slate-300 text-brand-600"
                            :checked="allChosen"
                            @change="toggleAll"
                        />
                        Select all on this page
                    </label>
                    <template v-if="selected.size">
                        <span class="text-slate-400">·</span>
                        <span class="text-slate-600">{{ selected.size }} selected</span>
                        <button type="button" class="font-medium text-brand-600 hover:underline" @click="markChosen">
                            Mark as seen
                        </button>
                        <button type="button" class="font-medium text-rose-600 hover:underline" @click="deleteChosen">
                            Delete
                        </button>
                    </template>
                </div>

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
                        <input
                            type="checkbox"
                            class="mt-1 shrink-0 rounded border-slate-300 text-brand-600"
                            :checked="selected.has(n.id)"
                            :aria-label="`Select: ${n.title}`"
                            @change="toggleChoice(n.id)"
                        />
                        <span class="mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full" :class="LEVEL[n.level]?.dot ?? LEVEL.info.dot" />
                        <div class="min-w-0 flex-1">
                            <div class="flex flex-wrap items-center gap-2">
                                <Link
                                    :href="route('notifications.show', n.id)"
                                    class="text-sm text-slate-900 hover:text-brand-700 hover:underline"
                                    :class="n.read_at ? '' : 'font-semibold'"
                                    >{{ n.title }}</Link
                                >
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
                                <button type="button" class="font-medium text-brand-600 hover:underline" @click="openOne(n)">
                                    Details
                                </button>
                                <button v-if="!n.read_at" type="button" class="font-medium text-slate-500 hover:underline" @click="markOne(n)">
                                    Mark as seen
                                </button>
                                <button type="button" class="font-medium text-rose-600 hover:underline" @click="deleteOne(n)">
                                    Delete
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
