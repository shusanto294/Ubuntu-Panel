<script setup>
import { computed, ref } from 'vue';
import { Link } from '@inertiajs/vue3';
import IpAddress from '@/Components/IpAddress.vue';
import { THREAT_LABEL, ago, number, statusTone, useNow } from './format';

/*
 * A card of requests, one look for both of Analytics' lists: the newest
 * requests, newest first, and the URLs asked for most, the most first. Each
 * row is when, what the server answered, the address asked for and who
 * asked — for a URL, its newest request's — and a URL says how many times it
 * was asked for too. The filter narrows the rows to the answered or the
 * refused and failed, and View all opens them in full with the same filter.
 */
const props = defineProps({
    rows: { type: Array, default: null }, // null while the logs are read
    urls: { type: Boolean, default: false }, // the URLs asked for most, not single requests
    total: { type: Number, default: 0 },
    limit: { type: Number, default: 0 }, // 0 is every row
    unmatched: { type: String, default: '' },
    allHref: { type: String, default: '' },
    title: { type: String, default: 'Latest requests' },
    subtitle: { type: String, default: 'Newest first. Images, scripts and styles are left out.' },
    flat: { type: Boolean, default: false }, // inside another card
    filters: { type: Boolean, default: true }, // false when the page filters them itself
    footer: { type: Boolean, default: true },
    badge: { type: String, default: '' }, // the time the rows are from
    badgeTone: { type: String, default: 'bg-slate-100 text-slate-600' },
    error: { type: String, default: '' },
});

const now = useNow();
const filter = ref('all');

const FILTERS = [
    ['all', 'All'],
    ['ok', 'Answered'],
    ['errors', 'Errors'],
];

// A URL is answered when some of its requests were, and an error when some
// were refused or failed.
const matches = (r) => {
    if (filter.value === 'all') return true;
    if (props.urls) return filter.value === 'ok' ? r.failed < r.requests : r.failed > 0;
    return filter.value === 'errors' ? r.status >= 400 : r.status < 400;
};

const filtered = computed(() => (props.rows ?? []).filter(matches));
const visible = computed(() => (props.limit ? filtered.value.slice(0, props.limit) : filtered.value));

const href = computed(() => {
    if (!props.allHref || filter.value === 'all') return props.allHref;
    return `${props.allHref}${props.allHref.includes('?') ? '&' : '?'}status=${filter.value}`;
});

const noun = computed(() => (props.urls ? 'URLs' : 'requests'));

const host = (site) => (site === props.unmatched ? 'server IP' : site);

const tag = (r) => {
    if (r.threat) return { label: THREAT_LABEL[r.threat] ?? 'Attack', tone: 'bg-rose-50 text-rose-700' };
    if (r.bot) return { label: r.bot, tone: 'bg-slate-100 text-slate-600' };
    return null;
};
</script>

<template>
    <section :class="flat ? '' : 'flex flex-col overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5'">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-5 py-4">
            <div>
                <div class="flex flex-wrap items-center gap-2">
                    <h3 class="font-semibold text-slate-900">{{ title }}</h3>
                    <span v-if="badge" class="rounded-md px-2 py-0.5 text-xs font-medium" :class="badgeTone">{{ badge }}</span>
                </div>
                <p v-if="subtitle" class="text-sm text-slate-500">{{ subtitle }}</p>
            </div>
            <div class="flex flex-wrap items-center gap-2">
                <slot name="actions" />
                <div v-if="filters" role="group" aria-label="Filter" class="flex gap-1.5">
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
        </div>

        <p v-if="error" class="px-5 py-8 text-center text-sm text-rose-700">{{ error }}</p>
        <div v-else-if="!rows" class="space-y-3 p-5">
            <div v-for="i in limit || 8" :key="i" class="h-5 animate-pulse rounded bg-slate-100" />
        </div>
        <p v-else-if="!visible.length" class="flex-1 px-5 py-8 text-center text-sm text-slate-500">
            {{ rows.length ? 'None of these match.' : 'Nothing was requested in this time.' }}
        </p>
        <div v-else class="flex-1 overflow-x-auto">
            <table class="w-full min-w-[760px] text-sm">
                <thead class="text-left text-xs text-slate-500">
                    <tr>
                        <th class="w-28 px-5 py-2.5 font-medium">{{ urls ? 'Last asked' : 'When' }}</th>
                        <th class="w-16 px-2 py-2.5 font-medium">Status</th>
                        <th class="px-2 py-2.5 font-medium">URL</th>
                        <th class="w-60 px-5 py-2.5 font-medium">{{ urls ? 'Last visitor' : 'Visitor' }}</th>
                        <th v-if="urls" class="w-24 px-5 py-2.5 text-right font-medium">Requests</th>
                    </tr>
                </thead>
                <tbody>
                    <tr v-for="(r, i) in visible" :key="`${r.at}-${r.ip}-${r.site}-${r.path}-${i}`" class="border-t border-slate-100">
                        <td class="whitespace-nowrap px-5 py-2.5 tabular-nums text-slate-500" :title="r.at ? new Date(r.at * 1000).toLocaleString() : ''">
                            {{ ago(r.at, now) }}
                        </td>
                        <td class="px-2 py-2.5">
                            <span
                                v-if="r.status"
                                class="inline-block rounded-md px-1.5 py-0.5 font-mono text-xs font-medium"
                                :class="statusTone(r.status)"
                                :title="urls && r.failed ? `${number(r.failed)} of ${number(r.requests)} answered 4xx or 5xx` : ''"
                            >
                                {{ r.status }}
                            </span>
                        </td>
                        <td class="max-w-0 px-2 py-2.5">
                            <div class="flex min-w-0 items-baseline gap-2">
                                <span v-if="r.method" class="shrink-0 font-mono text-xs text-slate-500">{{ r.method }}</span>
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
                            <div v-if="r.ip" class="flex items-center gap-1.5">
                                <Link :href="route('analytics.ip', r.ip)" class="text-[13px] text-slate-900 hover:text-brand-700 hover:underline">
                                    <IpAddress :ip="r.ip" />
                                </Link>
                                <span v-if="tag(r)" class="rounded-md px-1.5 py-0.5 text-[11px] font-semibold" :class="tag(r).tone">
                                    {{ tag(r).label }}
                                </span>
                            </div>
                        </td>
                        <td v-if="urls" class="px-5 py-2.5 text-right font-semibold tabular-nums text-slate-900">{{ number(r.requests) }}</td>
                    </tr>
                </tbody>
            </table>
        </div>

        <div
            v-if="footer && rows && allHref && !error"
            class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-100 px-5 py-3 text-sm text-slate-600"
        >
            <span class="tabular-nums">
                <template v-if="filter === 'all'">{{ number(visible.length) }} of {{ number(total) }} {{ noun }}</template>
                <template v-else>{{ number(visible.length) }} shown</template>
            </span>
            <Link :href="href" class="font-medium text-brand-600 hover:underline">View all {{ noun }} →</Link>
        </div>
    </section>
</template>
