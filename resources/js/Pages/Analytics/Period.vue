<script setup>
import { computed, onMounted, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Pagination from '@/Components/Pagination.vue';
import RecentRequests from '@/Components/Analytics/RecentRequests.vue';
import UrlTable from '@/Components/Analytics/UrlTable.vue';
import { number, periodHref, spanLabel } from '@/Components/Analytics/format';
import { Head, Link, router } from '@inertiajs/vue3';

/*
 * A part of the window on a page of its own — one bar of the chart, or all
 * of it: the URLs asked for most and the newest requests, twenty of each,
 * and each in full a page at a time. Everything is in the address, so a
 * spike can be reloaded, bookmarked or sent on. The logs are read once the
 * page is up, and kept a while so turning pages is quick.
 */
const props = defineProps({
    window: { type: String, default: '24h' },
    site: { type: String, default: '' },
    from: { type: Number, default: 0 },
    to: { type: Number, default: 0 },
    view: { type: String, default: '' },
    status: { type: String, default: '' },
    page: { type: Number, default: 1 },
    unmatched: { type: String, default: '' },
    period: { type: Object, default: null },
});

const loading = ref(true);

onMounted(() => {
    router.reload({
        only: ['period'],
        preserveScroll: true,
        preserveState: true,
        onFinish: () => (loading.value = false),
    });
});

const here = (changes = {}) =>
    periodHref({
        window: props.window,
        site: props.site,
        from: props.from,
        to: props.to,
        view: props.view,
        status: props.status,
        page: 1,
        ...changes,
    });

// A page turn, a filter or the other list: the same period, read again from
// what was kept.
const visit = (changes) => {
    router.get(here(changes), {}, {
        only: ['view', 'status', 'page', 'period'],
        preserveState: true,
        preserveScroll: changes.page === undefined,
        onStart: () => (loading.value = true),
        onFinish: () => (loading.value = false),
    });
};

const label = computed(() => spanLabel(props.from, props.to, props.window));
const p = computed(() => props.period);

const back = computed(() => {
    const query = new URLSearchParams({ window: props.window });
    if (props.site) query.set('site', props.site);
    if (props.from && props.to) {
        query.set('from', props.from);
        query.set('to', props.to);
    }
    return `${route('analytics.index')}?${query}`;
});

const siteLabel = (name) => (name === props.unmatched ? 'Server IP' : name);

const TABS = [
    ['', 'Overview'],
    ['urls', 'Most requested URLs'],
    ['requests', 'All requests'],
];

const STATUSES = [
    ['', 'All'],
    ['ok', 'Answered'],
    ['errors', 'Blocked or failed'],
];

const tabCount = (key) => {
    if (!p.value) return '';
    if (key === 'urls') return number(p.value.urls_total);
    if (key === 'requests') return number(p.value.matching);
    return '';
};

const requestsNote = computed(() => {
    if (!p.value?.recent_capped) return '';
    return `Only the newest ${number(p.value.recent_total)} of the ${number(p.value.matching)} requests are listed. Pick a shorter time on the chart to see the rest.`;
});
</script>

<template>
    <Head :title="`Requests · ${label}`" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center gap-3">
                <Link :href="back" class="text-sm font-medium text-slate-500 hover:text-slate-900">← Analytics</Link>
                <h2 class="text-lg font-semibold text-slate-900">{{ label }}</h2>
                <span v-if="site" class="rounded-lg bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-600">{{ siteLabel(site) }}</span>
            </div>
        </template>

        <div class="space-y-5">
            <p v-if="p" class="text-sm text-slate-600">
                <span class="font-semibold text-slate-900">{{ number(p.requests) }}</span> requests from
                {{ number(p.ips) }} {{ p.ips === 1 ? 'address' : 'addresses' }}
                <template v-if="p.client_errors"> · <span class="text-amber-700">{{ number(p.client_errors) }} blocked or not found</span></template>
                <template v-if="p.server_errors"> · <span class="text-rose-700">{{ number(p.server_errors) }} server errors</span></template>
            </p>
            <div v-else class="h-5 w-72 animate-pulse rounded bg-slate-100" />

            <nav class="-mb-px flex gap-1 overflow-x-auto border-b border-slate-200" aria-label="Lists">
                <Link
                    v-for="[key, name] in TABS"
                    :key="key"
                    :href="here({ view: key, status: key === 'requests' ? status : '' })"
                    :only="['view', 'status', 'page', 'period']"
                    preserve-state
                    preserve-scroll
                    class="whitespace-nowrap border-b-2 px-3 py-2.5 text-sm font-medium"
                    :class="view === key ? 'border-brand-600 text-slate-900' : 'border-transparent text-slate-500 hover:text-slate-900'"
                    @start="loading = true"
                    @finish="loading = false"
                >
                    {{ name }}
                    <span v-if="tabCount(key)" class="ml-1 tabular-nums text-slate-400">{{ tabCount(key) }}</span>
                </Link>
            </nav>

            <div class="transition-opacity" :class="loading && p ? 'opacity-60' : ''">
                <!-- Both lists, the first twenty of each -->
                <div v-if="!view" class="grid gap-5 xl:grid-cols-2">
                    <section class="flex flex-col overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                        <div class="border-b border-slate-100 px-5 py-4">
                            <h3 class="font-semibold text-slate-900">Most requested URLs</h3>
                            <p class="text-sm text-slate-500">Every URL, pages and files alike, the most asked for first.</p>
                        </div>
                        <div class="flex-1"><UrlTable :rows="p?.urls ?? null" :unmatched="unmatched" /></div>
                        <div v-if="p" class="flex items-center justify-between gap-3 border-t border-slate-100 px-5 py-3 text-sm text-slate-600">
                            <span class="tabular-nums">{{ number(p.urls.length) }} of {{ number(p.urls_total) }} URLs</span>
                            <Link v-if="p.urls_total > p.urls.length" :href="here({ view: 'urls' })" class="font-medium text-brand-600 hover:underline">
                                View all →
                            </Link>
                        </div>
                    </section>

                    <RecentRequests
                        :rows="p?.recent ?? null"
                        :total="p?.requests ?? 0"
                        :unmatched="unmatched"
                        :filters="false"
                        :all-href="here({ view: 'requests' })"
                        exact
                        subtitle="Every request, newest first."
                    />
                </div>

                <!-- Every URL, a page at a time -->
                <section v-if="view === 'urls'" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                    <div class="border-b border-slate-100 px-5 py-4">
                        <h3 class="font-semibold text-slate-900">Most requested URLs</h3>
                        <p class="text-sm text-slate-500">Every URL, pages and files alike, the most asked for first.</p>
                    </div>
                    <UrlTable
                        :rows="p?.urls ?? null"
                        :unmatched="unmatched"
                        :offset="p ? (p.urls_page - 1) * p.per_page : 0"
                        :skeleton="20"
                    />
                    <Pagination
                        v-if="p"
                        :model-value="p.urls_page"
                        :pages="p.urls_pages"
                        :total="p.urls_total"
                        :per-page="p.per_page"
                        @update:model-value="(n) => visit({ page: n })"
                    />
                </section>

                <!-- Every request, a page at a time -->
                <div v-if="view === 'requests'" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                    <RecentRequests
                        flat
                        title="All requests"
                        subtitle="Every request, newest first — pages, files, and the ones that were blocked."
                        :rows="p?.recent ?? null"
                        :total="p?.matching ?? 0"
                        :unmatched="unmatched"
                        :filters="false"
                        :footer="false"
                        :limit="20"
                    >
                        <template #actions>
                            <div role="group" aria-label="Filter requests" class="flex gap-1.5">
                                <button
                                    v-for="[key, name] in STATUSES"
                                    :key="key"
                                    type="button"
                                    class="h-8 rounded-lg px-3 text-sm font-medium ring-1 transition"
                                    :class="status === key ? 'bg-slate-900 text-white ring-slate-900' : 'bg-white text-slate-700 ring-slate-200 hover:bg-slate-50'"
                                    :aria-pressed="status === key"
                                    @click="visit({ status: key })"
                                >
                                    {{ name }}
                                </button>
                            </div>
                        </template>
                    </RecentRequests>
                    <Pagination
                        v-if="p"
                        :model-value="p.recent_page"
                        :pages="p.recent_pages"
                        :total="p.recent_total"
                        :per-page="p.per_page"
                        @update:model-value="(n) => visit({ page: n })"
                    />
                    <p v-if="requestsNote" class="border-t border-slate-100 px-5 py-3 text-xs text-slate-500">{{ requestsNote }}</p>
                </div>

                <p v-if="p?.partial?.length" class="mt-4 text-xs text-slate-500">
                    Only the newest part of {{ p.partial.map(siteLabel).join(', ') }}'s log is read for this period, so some of this
                    time may be missing from it.
                </p>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
