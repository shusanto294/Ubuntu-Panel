<script setup>
import { computed } from 'vue';
import { Link } from '@inertiajs/vue3';

/*
 * The period's totals from the access logs, each a link to the page with the
 * detail. Filled in after the page is up: reading the logs is the slow part,
 * and the list below should not wait for it.
 */
const props = defineProps({
    stats: { type: Object, default: null },
    window: { type: String, default: '1h' },
});

const number = (n) => (n ?? 0).toLocaleString();

const bytes = (n) => {
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
    return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
};

const href = (section) => `${route('logs.stats', section)}?window=${props.window}`;

const tiles = computed(() => {
    const s = props.stats;
    return [
        { key: 'requests', label: 'Requests', value: s && number(s.requests), detail: s ? `${bytes(s.bytes)} sent` : '' },
        { key: 'php', label: 'To PHP / the app', value: s && number(s.dynamic), detail: 'the ones that cost CPU' },
        { key: 'addresses', label: 'Addresses', value: s && number(s.ips), detail: 'distinct visitors' },
        { key: 'not-found', label: 'Not found / refused', value: s && number(s.client_errors), detail: '4xx responses' },
        {
            key: 'server-errors',
            label: 'Server errors',
            value: s && number(s.server_errors),
            detail: '5xx responses',
            alarm: s?.server_errors > 0,
        },
        {
            key: 'suspicious',
            label: 'Suspicious',
            value: s && number(s.suspicious),
            detail: s?.attackers ? `${s.attackers} address${s.attackers === 1 ? '' : 'es'} attacking` : 'logins, scans, XML-RPC',
            alarm: s?.attackers > 0,
        },
    ];
});
</script>

<template>
    <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
        <Link
            v-for="tile in tiles"
            :key="tile.key"
            :href="href(tile.key)"
            class="group rounded-2xl bg-white p-4 shadow-sm ring-1 ring-slate-900/5 transition hover:shadow-md hover:ring-brand-200"
        >
            <p class="flex items-center justify-between text-xs text-slate-500">
                {{ tile.label }}
                <span class="text-slate-300 transition group-hover:text-brand-500">→</span>
            </p>
            <p class="mt-1 text-2xl font-semibold" :class="tile.alarm ? 'text-rose-600' : 'text-slate-900'">
                <span v-if="tile.value !== null && tile.value !== undefined">{{ tile.value }}</span>
                <span v-else class="inline-block h-7 w-16 animate-pulse rounded bg-slate-100 align-middle" />
            </p>
            <p class="text-xs text-slate-400">{{ tile.detail }}</p>
        </Link>
    </div>
</template>
