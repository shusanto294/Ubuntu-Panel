<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import { useLiveRefresh } from '@/Composables/useLiveRefresh';
import { formatDuration, formatRelative, formatStamp } from '@/time';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    logs: { type: Array, default: () => [] },
    pagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0 }) },
    type: { type: String, default: null },
});

const FILTERS = [
    { key: null, label: 'Everything' },
    { key: 'provision', label: 'System' },
    { key: 'site', label: 'Sites' },
    { key: 'database', label: 'Databases' },
    { key: 'mail', label: 'Email' },
    { key: 'mcp', label: 'AI' },
];

const anyRunning = computed(() => props.logs.some((log) => log.status === 'running'));

useLiveRefresh(anyRunning, ['logs', 'pagination']);

const statusClass = (status) => {
    switch (status) {
        case 'success':
            return 'bg-emerald-50 text-emerald-700 ring-emerald-200';
        case 'failed':
            return 'bg-rose-50 text-rose-700 ring-rose-200';
        case 'running':
            return 'bg-brand-50 text-brand-700 ring-brand-200';
        default:
            return 'bg-slate-100 text-slate-600 ring-slate-200';
    }
};

const filterHref = (key) =>
    key ? `${route('logs.index')}?type=${key}` : route('logs.index');

const pageHref = (page) => {
    const query = new URLSearchParams();

    if (props.type) query.set('type', props.type);
    if (page > 1) query.set('page', page);

    const string = query.toString();

    return string ? `${route('logs.index')}?${string}` : route('logs.index');
};

const previous = computed(() => Math.max(1, props.pagination.page - 1));
const next = computed(() => Math.min(props.pagination.pages, props.pagination.page + 1));
</script>

<template>
    <Head title="Logs" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">Logs</h2>
                <p class="text-sm text-slate-500">
                    Everything the panel has run, newest first
                    <span v-if="pagination.total">· {{ pagination.total }} in total</span>
                </p>
            </div>
        </template>

        <div class="mb-6 flex flex-wrap gap-2">
            <Link
                v-for="filter in FILTERS"
                :key="filter.label"
                :href="filterHref(filter.key)"
                preserve-scroll
                class="rounded-lg px-3 py-1.5 text-xs font-medium ring-1 transition"
                :class="
                    (type ?? null) === filter.key
                        ? 'bg-brand-600 text-white ring-brand-600'
                        : 'bg-white text-slate-600 ring-slate-200 hover:bg-slate-50'
                "
            >
                {{ filter.label }}
            </Link>
        </div>

        <div class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <div v-if="!logs.length" class="px-5 py-12 text-center text-sm text-slate-500">
                Nothing has run yet.
            </div>

            <div v-else class="overflow-x-auto">
                <table class="min-w-full divide-y divide-slate-200">
                    <thead class="bg-slate-50">
                        <tr
                            class="text-left text-xs font-semibold uppercase tracking-wider text-slate-500"
                        >
                            <th class="px-5 py-3">When</th>
                            <th class="px-5 py-3">What</th>
                            <th class="px-5 py-3">Result</th>
                            <th class="px-5 py-3">Took</th>
                            <th class="px-5 py-3"></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr
                            v-for="log in logs"
                            :key="log.id"
                            class="text-sm transition hover:bg-slate-50"
                        >
                            <td class="whitespace-nowrap px-5 py-3 align-top">
                                <p class="text-slate-900">{{ formatStamp(log.created_at) }}</p>
                                <p class="text-xs text-slate-400">
                                    {{ formatRelative(log.created_at) }}
                                </p>
                            </td>
                            <td class="px-5 py-3 align-top">
                                <p class="font-medium text-slate-900">{{ log.action }}</p>
                                <p class="max-w-md truncate text-xs text-slate-500">
                                    {{ log.message }}
                                </p>
                            </td>
                            <td class="whitespace-nowrap px-5 py-3 align-top">
                                <span
                                    class="rounded-full px-2.5 py-0.5 text-xs font-medium ring-1"
                                    :class="statusClass(log.status)"
                                >
                                    {{ log.status }}
                                </span>
                                <span
                                    v-if="log.status === 'running'"
                                    class="ml-2 text-xs text-slate-500"
                                    >{{ log.progress }}%</span
                                >
                            </td>
                            <td
                                class="whitespace-nowrap px-5 py-3 align-top text-xs text-slate-500"
                            >
                                {{ formatDuration(log.started_at, log.finished_at) || '—' }}
                            </td>
                            <td class="whitespace-nowrap px-5 py-3 text-right align-top">
                                <Link
                                    :href="route('logs.show', log.id)"
                                    class="text-xs font-medium text-brand-600 hover:underline"
                                >
                                    Open
                                </Link>
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>

            <div
                v-if="pagination.pages > 1"
                class="flex items-center justify-between border-t border-slate-200 px-5 py-3 text-xs text-slate-500"
            >
                <span>Page {{ pagination.page }} of {{ pagination.pages }}</span>
                <div class="flex gap-2">
                    <Link
                        v-if="pagination.page > 1"
                        :href="pageHref(previous)"
                        class="rounded-lg bg-white px-3 py-1.5 font-medium text-slate-600 ring-1 ring-slate-200 hover:bg-slate-50"
                    >
                        Newer
                    </Link>
                    <Link
                        v-if="pagination.page < pagination.pages"
                        :href="pageHref(next)"
                        class="rounded-lg bg-white px-3 py-1.5 font-medium text-slate-600 ring-1 ring-slate-200 hover:bg-slate-50"
                    >
                        Older
                    </Link>
                </div>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
