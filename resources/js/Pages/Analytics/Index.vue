<script setup>
import { computed, onMounted, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Pagination from '@/Components/Pagination.vue';
import SiteTraffic from '@/Components/Logs/SiteTraffic.vue';
import StatTiles from '@/Components/Logs/StatTiles.vue';
import Traffic from '@/Components/Logs/Traffic.vue';
import { usePagination } from '@/Composables/usePagination';
import { Head, router } from '@inertiajs/vue3';

/*
 * Analytics: the sites' traffic over a period, for every site or one. The
 * totals, the timeline, the sites and the pages visited most, what the server
 * answered, where visitors came from, who sent the requests and which of them
 * attacked. All of it is nginx's access logs added up; the page is up at once
 * and the logs are read after, because on a busy machine that takes seconds.
 */
const props = defineProps({
    window: { type: String, default: '1h' },
    windows: { type: Array, default: () => ['1h', '24h', '7d', '30d'] },
    site: { type: String, default: '' },
    sites: { type: Array, default: () => [] },
    unmatched: { type: String, default: '' },

    traffic: { type: Object, default: null },
    trustedIps: { type: Object, default: () => ({}) },
    stats: { type: Object, default: null },
});

const RELOADS = ['window', 'site', 'traffic', 'trustedIps', 'stats'];

const loading = ref(true);
const switching = ref(false);

onMounted(() => {
    router.reload({
        only: ['traffic', 'trustedIps', 'stats'],
        preserveScroll: true,
        preserveState: true,
        onFinish: () => (loading.value = false),
    });
});

const pickSite = (site) => {
    const query = { window: props.window };
    if (site) query.site = site;

    router.get(route('analytics.index'), query, {
        only: RELOADS,
        preserveScroll: true,
        preserveState: true,
        onStart: () => (switching.value = true),
        onFinish: () => (switching.value = false),
    });
};

const number = (n) => (n ?? 0).toLocaleString();

// ------------------------------------------------------------ status codes

const CODE_LABEL = {
    200: 'OK',
    201: 'Created',
    204: 'No content',
    206: 'Partial content',
    301: 'Moved permanently',
    302: 'Found (redirect)',
    303: 'See other',
    304: 'Not modified (cached)',
    307: 'Temporary redirect',
    308: 'Permanent redirect',
    400: 'Bad request',
    401: 'Unauthorised',
    403: 'Forbidden',
    404: 'Not found',
    405: 'Method not allowed',
    408: 'Request timeout',
    413: 'Too large',
    429: 'Too many requests',
    444: 'Dropped by nginx',
    499: 'Client closed the connection',
    500: 'Internal server error',
    502: 'Bad gateway',
    503: 'Unavailable',
    504: 'Gateway timeout',
};

const CODE_TONE = {
    2: 'bg-emerald-500',
    3: 'bg-sky-500',
    4: 'bg-amber-500',
    5: 'bg-rose-500',
};

const codes = computed(() => props.traffic?.codes ?? []);
const busiestCode = computed(() => Math.max(1, ...codes.value.map((c) => c.count)));

const classes = computed(() => {
    const s = props.traffic?.statuses ?? {};
    const total = Math.max(1, props.traffic?.requests ?? 0);
    return ['2xx', '3xx', '4xx', '5xx'].map((key) => ({
        key,
        count: s[key] ?? 0,
        share: ((s[key] ?? 0) / total) * 100,
        tone: CODE_TONE[key[0]],
    }));
});

// -------------------------------------------------------------- referrers

const referrers = computed(() => props.traffic?.referrers ?? []);
const referrerPages = usePagination(referrers, 15);
const busiestReferrer = computed(() => Math.max(1, ...referrers.value.map((r) => r.count)));

const methods = computed(() => props.traffic?.methods ?? []);

const siteLabel = (domain) => (domain === props.unmatched ? 'Server IP and unknown hostnames' : domain);
</script>

<template>
    <Head title="Analytics" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Analytics</h2>
                    <p class="text-sm text-slate-500">
                        What the sites on this server were asked for, read from nginx's access logs.
                    </p>
                </div>
                <label class="flex items-center gap-2 text-sm">
                    <span class="text-xs font-medium text-slate-700">Site</span>
                    <select
                        :value="site"
                        :disabled="switching"
                        class="rounded-lg border-slate-300 text-sm shadow-sm disabled:opacity-50"
                        @change="pickSite($event.target.value)"
                    >
                        <option value="">Every site</option>
                        <option v-for="domain in sites" :key="domain" :value="domain">{{ domain }}</option>
                        <option v-if="unmatched" :value="unmatched">{{ siteLabel(unmatched) }}</option>
                    </select>
                </label>
            </div>
        </template>

        <div class="space-y-6" :class="switching ? 'opacity-60 transition-opacity' : ''">
            <StatTiles :stats="stats" :window="window" :site="site" />

            <Traffic
                :window="window"
                :windows="windows"
                :traffic="traffic"
                :trusted-ips="trustedIps"
                :sections="['findings', 'timeline']"
                :loading="loading"
                :reloads="RELOADS"
            />

            <SiteTraffic :stats="stats" />

            <div class="grid gap-6 xl:grid-cols-3">
                <!-- What the server answered -->
                <section class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-900">Responses</h3>
                    <p class="text-xs text-slate-500">What the server answered, by status code.</p>

                    <div v-if="!traffic" class="mt-4 space-y-3">
                        <div v-for="i in 5" :key="i" class="h-5 animate-pulse rounded bg-slate-100" />
                    </div>
                    <template v-else>
                        <div class="mt-4 flex h-2.5 overflow-hidden rounded-full bg-slate-100">
                            <span
                                v-for="c in classes"
                                :key="c.key"
                                :class="c.tone"
                                :style="{ width: `${c.share}%` }"
                                :title="`${c.key}: ${number(c.count)}`"
                            />
                        </div>
                        <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
                            <span v-for="c in classes" :key="c.key" class="inline-flex items-center gap-1.5">
                                <span class="h-2 w-2 rounded-sm" :class="c.tone" />
                                {{ c.key }} · {{ number(c.count) }}
                            </span>
                        </div>

                        <p v-if="!codes.length" class="mt-4 text-sm text-slate-500">No requests in this period.</p>
                        <ul v-else class="mt-4 space-y-2">
                            <li v-for="c in codes.slice(0, 12)" :key="c.name" class="text-sm">
                                <div class="flex items-baseline justify-between gap-3">
                                    <span class="min-w-0 truncate">
                                        <span class="font-mono font-semibold text-slate-900">{{ c.name }}</span>
                                        <span class="ml-2 text-xs text-slate-500">{{ CODE_LABEL[c.name] ?? '' }}</span>
                                    </span>
                                    <span class="shrink-0 tabular-nums text-slate-700">{{ number(c.count) }}</span>
                                </div>
                                <span class="mt-1 block h-1 overflow-hidden rounded-full bg-slate-100">
                                    <span
                                        class="block h-full rounded-full"
                                        :class="CODE_TONE[c.name[0]] ?? 'bg-slate-400'"
                                        :style="{ width: `${Math.max(2, (c.count / busiestCode) * 100)}%` }"
                                    />
                                </span>
                            </li>
                        </ul>

                        <div v-if="methods.length" class="mt-5 border-t border-slate-100 pt-4">
                            <p class="text-xs font-medium text-slate-700">Methods</p>
                            <div class="mt-2 flex flex-wrap gap-2">
                                <span
                                    v-for="m in methods"
                                    :key="m.name"
                                    class="rounded-lg bg-slate-50 px-2 py-1 text-xs text-slate-600 ring-1 ring-slate-200"
                                >
                                    <span class="font-mono font-semibold text-slate-800">{{ m.name }}</span>
                                    {{ number(m.count) }}
                                </span>
                            </div>
                        </div>
                    </template>
                </section>

                <!-- Where visitors came from -->
                <section class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-2">
                    <div class="px-5 pt-5">
                        <h3 class="font-semibold text-slate-900">Where visitors came from</h3>
                        <p class="text-xs text-slate-500">
                            Other websites that sent visitors to a page — search engines, social networks, links.
                            A site linking to its own pages is not counted, and nor are attacks.
                        </p>
                    </div>

                    <div v-if="!traffic" class="space-y-3 p-5">
                        <div v-for="i in 6" :key="i" class="h-6 animate-pulse rounded bg-slate-100" />
                    </div>
                    <p v-else-if="!referrers.length" class="p-5 text-sm text-slate-500">
                        No visitor arrived from another site in this period — or the browsers did not say where from.
                    </p>
                    <template v-else>
                        <ul class="mt-3 divide-y divide-slate-100">
                            <li v-for="r in referrerPages.visible.value" :key="r.name" class="px-5 py-2.5">
                                <div class="flex items-baseline justify-between gap-3 text-sm">
                                    <a
                                        :href="`https://${r.name}`"
                                        target="_blank"
                                        rel="noopener noreferrer nofollow"
                                        class="min-w-0 truncate font-medium text-brand-600 hover:underline"
                                    >
                                        {{ r.name }}
                                    </a>
                                    <span class="shrink-0 font-semibold tabular-nums text-slate-900">{{ number(r.count) }}</span>
                                </div>
                                <span class="mt-1 block h-1 overflow-hidden rounded-full bg-slate-100">
                                    <span
                                        class="block h-full rounded-full bg-brand-500"
                                        :style="{ width: `${Math.max(2, (r.count / busiestReferrer) * 100)}%` }"
                                    />
                                </span>
                            </li>
                        </ul>
                        <Pagination
                            v-model="referrerPages.page.value"
                            :pages="referrerPages.pages.value"
                            :total="referrerPages.total.value"
                            :per-page="referrerPages.perPage"
                        />
                    </template>
                </section>
            </div>

            <Traffic
                :window="window"
                :windows="windows"
                :traffic="traffic"
                :trusted-ips="trustedIps"
                :sections="['ips', 'attacks', 'agents', 'urls']"
                :controls="false"
                :reloads="RELOADS"
            />
        </div>
    </AuthenticatedLayout>
</template>
