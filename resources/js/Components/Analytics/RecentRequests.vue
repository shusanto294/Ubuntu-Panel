<script setup>
import { computed, ref } from 'vue';
import { Link } from '@inertiajs/vue3';
import { THREAT_LABEL, ago, number, statusTone, useNow } from './format';

/*
 * The newest requests for pages, newest first: when, what the server said,
 * the address asked for and who asked. Stylesheets, scripts and images are
 * left out — a page pulls in dozens — and so they are not in the counts here.
 */
const props = defineProps({
    rows: { type: Array, default: null }, // null while the logs are read
    total: { type: Number, default: 0 },
    limit: { type: Number, default: 0 }, // 0 is every row
    unmatched: { type: String, default: '' },
    allHref: { type: String, default: '' },
    title: { type: String, default: 'Latest requests' },
    flat: { type: Boolean, default: false }, // inside another card
    subtitle: { type: String, default: 'Newest first. Images, scripts and styles are left out.' },
    filters: { type: Boolean, default: true }, // false when the page filters them itself
    footer: { type: Boolean, default: true },
    exact: { type: Boolean, default: false }, // the time of day rather than "3 min ago"
});

const now = useNow();
const filter = ref('all');

const FILTERS = [
    ['all', 'All'],
    ['ok', 'Answered'],
    ['errors', 'Errors'],
];

const filtered = computed(() =>
    (props.rows ?? []).filter((r) => filter.value === 'all' || (filter.value === 'errors' ? r.status >= 400 : r.status < 400)),
);

const visible = computed(() => (props.limit ? filtered.value.slice(0, props.limit) : filtered.value));

// Inside one bar of the chart the rows are minutes apart, so they say when.
// Another day's say which day too.
const clock = (at) => {
    const d = new Date(at * 1000);
    const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    return d.toDateString() === new Date().toDateString() ? time : `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}, ${time}`;
};

const host = (site) => (site === props.unmatched ? 'server IP' : site);

const tag = (r) => {
    if (r.threat) return { label: THREAT_LABEL[r.threat] ?? 'Attack', tone: 'bg-rose-50 text-rose-700' };
    if (r.bot) return { label: r.bot, tone: 'bg-slate-100 text-slate-600' };
    return null;
};
</script>

<template>
    <section :class="flat ? '' : 'overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5'">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-5 py-4">
            <div>
                <h3 class="font-semibold text-slate-900">{{ title }}</h3>
                <p v-if="subtitle" class="text-sm text-slate-500">{{ subtitle }}</p>
            </div>
            <slot name="actions" />
            <div v-if="filters" role="group" aria-label="Filter requests" class="flex gap-1.5">
                <button
                    v-for="[key, label] in FILTERS"
                    :key="key"
                    type="button"
                    class="h-8 rounded-lg px-3 text-sm font-medium ring-1 transition"
                    :class="filter === key ? 'bg-slate-900 text-white ring-slate-900' : 'bg-white text-slate-700 ring-slate-200 hover:bg-slate-50'"
                    :aria-pressed="filter === key"
                    @click="filter = key"
                >
                    {{ label }}
                </button>
            </div>
        </div>

        <div v-if="!rows" class="space-y-3 p-5">
            <div v-for="i in limit || 8" :key="i" class="h-5 animate-pulse rounded bg-slate-100" />
        </div>
        <p v-else-if="!visible.length" class="px-5 py-8 text-center text-sm text-slate-500">
            {{ rows.length ? 'None of these requests match.' : 'No pages were requested in this period.' }}
        </p>
        <div v-else class="overflow-x-auto">
            <table class="w-full min-w-[720px] text-sm">
                <thead class="text-left text-xs text-slate-500">
                    <tr>
                        <th class="w-24 px-5 py-2.5 font-medium">When</th>
                        <th class="w-16 px-2 py-2.5 font-medium">Status</th>
                        <th class="px-2 py-2.5 font-medium">URL</th>
                        <th class="w-56 px-5 py-2.5 font-medium">Visitor</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-for="(r, i) in visible" :key="`${r.at}-${r.ip}-${r.path}-${i}`" class="border-t border-slate-100">
                        <td class="whitespace-nowrap px-5 py-2.5 tabular-nums text-slate-500" :title="new Date(r.at * 1000).toLocaleString()">
                            {{ flat || exact ? clock(r.at) : ago(r.at, now) }}
                        </td>
                        <td class="px-2 py-2.5">
                            <span class="inline-block rounded-md px-1.5 py-0.5 font-mono text-xs font-medium" :class="statusTone(r.status)">
                                {{ r.status }}
                            </span>
                        </td>
                        <td class="max-w-0 px-2 py-2.5">
                            <div class="flex min-w-0 items-baseline gap-2">
                                <span class="shrink-0 font-mono text-xs text-slate-500">{{ r.method }}</span>
                                <a
                                    v-if="r.url"
                                    :href="r.url"
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    class="truncate text-slate-900 hover:text-brand-700 hover:underline"
                                    :title="`${host(r.site)}${r.path}`"
                                >
                                    <span class="text-slate-500">{{ host(r.site) }}</span>{{ r.path }}
                                </a>
                                <span v-else class="truncate text-slate-900" :title="`${host(r.site)}${r.path}`">
                                    <span class="text-slate-500">{{ host(r.site) }}</span>{{ r.path }}
                                </span>
                            </div>
                        </td>
                        <td class="whitespace-nowrap px-5 py-2.5">
                            <Link :href="route('analytics.ip', r.ip)" class="font-mono text-[13px] text-slate-900 hover:text-brand-700 hover:underline">
                                {{ r.ip }}
                            </Link>
                            <span v-if="tag(r)" class="ml-1.5 rounded-md px-1.5 py-0.5 text-[11px] font-semibold" :class="tag(r).tone">
                                {{ tag(r).label }}
                            </span>
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>

        <div
            v-if="footer && rows && allHref"
            class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-100 px-5 py-3 text-sm text-slate-600"
        >
            <span class="tabular-nums">{{ number(visible.length) }} of {{ number(total) }} requests</span>
            <Link :href="allHref" class="font-medium text-brand-600 hover:underline">View all requests →</Link>
        </div>
        <p v-else-if="footer && rows && rows.length" class="border-t border-slate-100 px-5 py-3 text-xs text-slate-500">
            The {{ number(rows.length) }} newest page requests of {{ number(total) }} in the period. Every request is in the
            site's access log under Logs → Server logs.
        </p>
    </section>
</template>
