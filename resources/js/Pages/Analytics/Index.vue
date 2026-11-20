<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import RecentRequests from '@/Components/Analytics/RecentRequests.vue';
import {
    CLASS_BAR,
    CODE_LABEL,
    PERIOD_LABEL,
    PERIOD_MINUTES,
    PERIOD_SHORT,
    THREAT_LABEL,
    ago,
    number,
    periodHref,
    spanLabel,
    statusTone,
    useNow,
} from '@/Components/Analytics/format';
import { confirmAction, notify } from '@/dialog';
import { Head, Link, router } from '@inertiajs/vue3';

/*
 * Analytics: what the sites on this server were asked for over a period. The
 * headline numbers, when the requests came, the latest of them, the domains
 * and addresses seen most recently, and the pages, answers and referrers
 * behind them. All of it is nginx's access logs added up; the page is up at
 * once and the logs are read after, because on a busy machine that takes
 * seconds. Live reads them again every half minute.
 */
const props = defineProps({
    window: { type: String, default: '1h' },
    windows: { type: Array, default: () => ['1h', '24h', '7d', '30d'] },
    site: { type: String, default: '' },
    sites: { type: Array, default: () => [] },
    unmatched: { type: String, default: '' },
    edgeAccounts: { type: Array, default: () => [] },
    siteDomains: { type: Array, default: () => [] },
    from: { type: Number, default: 0 }, // the bar that is open, from the address
    to: { type: Number, default: 0 },

    traffic: { type: Object, default: null },
    trustedIps: { type: Object, default: () => ({}) },
    stats: { type: Object, default: null },
});

const RELOADS = ['window', 'site', 'traffic', 'trustedIps', 'stats'];
const LAZY = ['traffic', 'trustedIps', 'stats'];

const now = useNow();
const loading = ref(true);
const switching = ref(false);
const refreshing = ref(false);

onMounted(() => {
    router.reload({
        only: LAZY,
        preserveScroll: true,
        preserveState: true,
        onFinish: () => (loading.value = false),
    });
});

const go = (changes) => {
    const query = { window: props.window, ...(props.site ? { site: props.site } : {}), ...changes };
    if (!query.site) delete query.site;

    router.get(route('analytics.index'), query, {
        only: RELOADS,
        preserveScroll: true,
        preserveState: true,
        onStart: () => (switching.value = true),
        onFinish: () => (switching.value = false),
    });
};

// ------------------------------------------------------------------ live

const live = ref(true);
let timer = null;

const refresh = () => {
    if (refreshing.value || switching.value || loading.value || document.hidden) return;

    router.reload({
        only: LAZY,
        preserveScroll: true,
        preserveState: true,
        onStart: () => (refreshing.value = true),
        onFinish: () => (refreshing.value = false),
    });
};

watch(
    live,
    (on) => {
        clearInterval(timer);
        timer = on ? setInterval(refresh, 30000) : null;
    },
    { immediate: true },
);

onBeforeUnmount(() => clearInterval(timer));

// ------------------------------------------------------------------ links

const statsHref = (section) => {
    const query = new URLSearchParams({ window: props.window });
    if (props.site) query.set('site', props.site);
    return `${route('analytics.stats', section)}?${query}`;
};

const siteLabel = (domain) => (domain === props.unmatched ? 'Server IP' : domain);

// --------------------------------------------------------- headline numbers

const s = computed(() => props.stats);
const t = computed(() => props.traffic);

const statusCount = (cls) => t.value?.statuses?.[cls] ?? 0;

const perMinute = computed(() => {
    const rate = (s.value?.requests ?? 0) / PERIOD_MINUTES[props.window];
    return rate >= 10 ? Math.round(rate).toLocaleString() : rate.toFixed(rate >= 1 ? 1 : 2);
});

const answered = computed(() => statusCount('2xx') + statusCount('3xx'));
const answeredShare = computed(() => (s.value?.requests ? Math.round((answered.value / s.value.requests) * 100) : 0));

const refusedBreakdown = computed(() => {
    const top = (t.value?.codes ?? []).filter((c) => c.name.startsWith('4')).slice(0, 2);
    const parts = top.map((c) => `${number(c.count)} × ${c.name}`);
    const fives = statusCount('5xx');
    if (fives) parts.push(`${number(fives)} × 5xx`);
    return parts.join(' · ') || 'nothing refused';
});

// ------------------------------------------------------------------ chart

const bars = computed(() => {
    const timeline = t.value?.timeline ?? [];
    const peak = Math.max(1, ...timeline.map((b) => b.requests));
    return timeline.map((b) => {
        const fail = b.server_errors ?? 0;
        const warn = b.client_errors ?? 0;
        const ok = Math.max(0, b.requests - warn - fail);
        return {
            at: b.at,
            ok: (ok / peak) * 100,
            warn: (warn / peak) * 100,
            fail: (fail / peak) * 100,
            title: `${new Date(b.at * 1000).toLocaleString()} — ${number(b.requests)} requests: ${number(ok)} answered, ${number(warn)} blocked or not found, ${number(fail)} server errors`,
        };
    });
});

// One bar opened: the requests in its slice of time, read when it is
// clicked. The slice is kept by its time, so a live refresh that moves the
// bars on keeps showing the same minutes.
const barWidth = computed(() => {
    const timeline = t.value?.timeline ?? [];
    if (timeline.length > 1) return timeline[1].at - timeline[0].at;
    return Math.round((PERIOD_MINUTES[props.window] * 60) / Math.max(1, timeline.length));
});

const picked = ref(null); // { from, to, data, loading, error }
let pickSeq = 0;

const isPicked = (b) => {
    if (!picked.value) return false;
    const middle = (picked.value.from + picked.value.to) / 2;
    return b.at <= middle && middle < b.at + barWidth.value;
};

const open = async (from, to) => {
    const seq = ++pickSeq;
    picked.value = { from, to, data: null, loading: true, error: '' };

    try {
        const { data } = await window.axios.get(route('analytics.slice'), {
            params: { window: props.window, from, to, ...(props.site ? { site: props.site } : {}) },
        });
        if (seq === pickSeq) picked.value = { from, to, data, loading: false, error: '' };
    } catch (e) {
        if (seq === pickSeq) {
            picked.value = { from, to, data: null, loading: false, error: e?.response?.data?.error ?? 'The logs could not be read.' };
        }
    }
};

// The open bar is kept in the address — ?from=…&to=… — so a reload, a
// bookmark or a link sent on opens the same minutes.
const remember = (from, to) => {
    const query = new URLSearchParams({ window: props.window });
    if (props.site) query.set('site', props.site);
    if (from && to) {
        query.set('from', from);
        query.set('to', to);
    }
    router.replace({
        url: `${route('analytics.index')}?${query}`,
        props: (current) => ({ ...current, from, to }),
        preserveScroll: true,
        preserveState: true,
    });
};

const pick = (b) => {
    if (isPicked(b)) {
        close();
        return;
    }

    const from = b.at;
    const to = b.at + barWidth.value;
    remember(from, to);
    open(from, to);
};

const close = () => {
    pickSeq++;
    picked.value = null;
    remember(0, 0);
};

onMounted(() => {
    if (props.from && props.to) open(props.from, props.to);
});

// A new period or site is a new chart.
watch(() => [props.window, props.site], () => {
    pickSeq++;
    picked.value = null;
});

const pickedLabel = computed(() => (picked.value ? spanLabel(picked.value.from, picked.value.to, props.window) : ''));

const pickedHref = (view = '') =>
    picked.value ? periodHref({ window: props.window, site: props.site, from: picked.value.from, to: picked.value.to, view }) : '';

// The URL and request cards show the bar picked, or the whole period, and
// say which.
const ROWS = 10;

const spanText = computed(() => (picked.value ? pickedLabel.value : `The ${PERIOD_LABEL[props.window]}`));
const spanTone = computed(() => (picked.value ? 'bg-brand-50 text-brand-700' : 'bg-slate-100 text-slate-600'));

const spanHref = (view) => (picked.value ? pickedHref(view) : periodHref({ window: props.window, site: props.site, view }));

const urlRows = computed(() => {
    if (picked.value) return picked.value.data ? picked.value.data.urls : picked.value.error ? [] : null;
    return s.value ? s.value.urls ?? [] : null;
});
const urlTotal = computed(() => (picked.value ? picked.value.data?.urls_total ?? 0 : s.value?.urls_total ?? 0));

const requestRows = computed(() => {
    if (picked.value) return picked.value.data ? picked.value.data.recent : picked.value.error ? [] : null;
    return s.value?.recent ?? null;
});
const requestTotal = computed(() => (picked.value ? picked.value.data?.requests ?? 0 : s.value?.requests ?? 0));

const AXIS = {
    '1h': ['60 min ago', '30 min ago'],
    '24h': ['24 h ago', '12 h ago'],
    '7d': ['7 days ago', '3½ days ago'],
    '30d': ['30 days ago', '15 days ago'],
};

// --------------------------------------------------------------- domains

const LIMIT = 5;

const sitesSort = ref('recent');
const domains = computed(() => {
    const list = [...(s.value?.sites ?? [])];
    list.sort((a, b) => (sitesSort.value === 'recent' ? b.last - a.last : b.requests - a.requests));
    return list;
});
const busiestSite = computed(() => Math.max(1, ...domains.value.map((d) => d.requests)));

const errorsText = (d) => {
    const parts = [];
    if (d.client_errors) parts.push(`${number(d.client_errors)} refused or not found`);
    if (d.server_errors) parts.push(`${number(d.server_errors)} server errors`);
    return parts.join(' · ') || 'no errors';
};

// ------------------------------------------------------------- addresses

const ipsSort = ref('recent');
const addresses = computed(() => {
    const list = (t.value?.top_ips ?? []).filter((ip) => !ip.ip.startsWith('('));
    list.sort((a, b) => (ipsSort.value === 'recent' ? b.last - a.last : b.requests - a.requests));
    return list;
});

const ipTag = (ip) => {
    if (ip.danger) return { label: THREAT_LABEL[ip.attack] ?? 'Attack', tone: 'bg-rose-50 text-rose-700' };
    if (ip.trusted) return { label: 'Trusted', tone: 'bg-emerald-50 text-emerald-700', title: ip.trusted };
    if (ip.good_bot) return { label: ip.good_bot.name, tone: 'bg-slate-100 text-slate-600' };
    if (ip.bot) return { label: 'Bot', tone: 'bg-slate-100 text-slate-600' };
    return null;
};

const ipWhere = (ip) => {
    const where = (ip.sites ?? []).map((x) => siteLabel(x.name)).slice(0, 2).join(', ');
    if (!ip.danger) return where;
    const paths = (ip.paths ?? []).slice(0, 2).map((p) => p.name).join(', ');
    return paths ? `${where} · ${paths}` : where;
};

// ---------------------------------------------------- blocking an address

// Blocking is a Cloudflare IP access rule on every zone with a site on this
// server: the panel itself does not ban addresses.
const blocking = ref('');

const block = async (ip) => {
    if (
        !(await confirmAction(
            `Block ${ip.ip}?`,
            'Cloudflare will refuse it on every zone that has a site on this server, before its requests reach the server. You can remove the rule under Security → Cloudflare.',
            'Block it',
        ))
    )
        return;

    blocking.value = ip.ip;
    try {
        const lists = await Promise.all(
            props.edgeAccounts.map(async (account) => {
                try {
                    const { data } = await window.axios.get(route('dns.zones', account.id));
                    return (data.zones ?? [])
                        .filter((z) => props.siteDomains.some((d) => d === z.name || d.endsWith(`.${z.name}`)))
                        .map((z) => ({ account: account.id, zone_id: z.id, zone_name: z.name }));
                } catch {
                    return [];
                }
            }),
        );
        const targets = lists.flat();
        if (!targets.length) {
            notify('Nothing to block it on', 'None of the Cloudflare zones connected here has a site on this server.', 'warning');
            return;
        }

        const { data } = await window.axios.post(route('edge.bulk.access'), {
            target: ip.ip.includes(':') ? 'ip6' : 'ip',
            value: ip.ip,
            mode: 'block',
            notes: `Blocked from Analytics — ${ip.verdict || 'flagged as an attack'}`,
            targets,
        });
        const failed = (data.outcomes ?? []).filter((o) => o.status === 'failed');
        notify(failed.length ? 'Blocked on some zones' : 'Blocked', data.message, failed.length ? 'warning' : 'success');
    } catch (e) {
        notify('Not blocked', e?.response?.data?.error ?? 'Cloudflare did not take the rule.', 'error');
    } finally {
        blocking.value = '';
    }
};

// ----------------------------------------------------- supporting detail


const allCodes = ref(false);
const codes = computed(() => t.value?.codes ?? []);
const classes = computed(() => {
    const total = Math.max(1, s.value?.requests ?? 0);
    return ['2xx', '3xx', '4xx', '5xx']
        .map((key) => ({ key, share: (statusCount(key) / total) * 100 }))
        .filter((c) => c.share > 0);
});

const allReferrers = ref(false);
const referrers = computed(() => t.value?.referrers ?? []);

const botShare = computed(() => (s.value?.requests ? Math.round(((s.value.bots ?? 0) / s.value.requests) * 100) : 0));

// --------------------------------------------------------------- attention

const attention = computed(() => {
    if (!s.value || !t.value) return null;

    const unmatched = (s.value.sites ?? []).find((x) => x.domain === props.unmatched);
    const parts = [];

    if (s.value.attackers) {
        parts.push(
            `${number(s.value.attackers)} address${s.value.attackers === 1 ? ' was' : 'es were'} flagged as attacking, with ${number(s.value.suspicious)} suspicious requests`,
        );
    } else if (s.value.suspicious) {
        parts.push(`${number(s.value.suspicious)} requests looked like attacks`);
    }

    if (unmatched?.requests && !props.site) {
        parts.push(`${number(unmatched.requests)} went to the server's IP address instead of a site`);
    }

    if (!parts.length) return null;
    return parts.join('; ') + '.';
});
</script>

<template>
    <Head title="Analytics" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-end justify-between gap-4">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Analytics</h2>
                    <p class="text-sm text-slate-500">Traffic to every site on this server, from nginx's access logs.</p>
                </div>
                <div class="flex flex-wrap items-center gap-2.5">
                    <label class="flex items-center gap-2 text-sm text-slate-600">
                        <span>Site</span>
                        <select
                            :value="site"
                            :disabled="switching"
                            class="h-10 rounded-xl border-slate-200 text-sm shadow-sm disabled:opacity-50"
                            @change="go({ site: $event.target.value })"
                        >
                            <option value="">All sites</option>
                            <option v-for="domain in sites" :key="domain" :value="domain">{{ domain }}</option>
                            <option v-if="unmatched" :value="unmatched">Server IP and unknown hostnames</option>
                        </select>
                    </label>
                    <div role="group" aria-label="Period" class="flex gap-0.5 rounded-xl bg-white p-[3px] ring-1 ring-slate-200">
                        <button
                            v-for="key in windows"
                            :key="key"
                            type="button"
                            :disabled="switching"
                            :aria-pressed="window === key"
                            class="h-8 rounded-lg px-3 text-[13px] font-medium transition"
                            :class="window === key ? 'bg-slate-900 text-white' : 'text-slate-600 hover:text-slate-900'"
                            @click="go({ window: key })"
                        >
                            {{ PERIOD_SHORT[key] ?? key }}
                        </button>
                    </div>
                    <button
                        type="button"
                        :aria-pressed="live"
                        class="inline-flex h-10 items-center gap-2 rounded-xl bg-white px-3.5 text-sm font-medium text-slate-800 ring-1 ring-slate-200 hover:bg-slate-50"
                        :title="live ? 'Reading the logs again every 30 seconds — click to pause' : 'Paused — click to read the logs every 30 seconds'"
                        @click="live = !live"
                    >
                        <span class="relative flex h-2 w-2">
                            <span v-if="live" class="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
                            <span class="relative inline-flex h-2 w-2 rounded-full" :class="live ? 'bg-emerald-500' : 'bg-slate-300'" />
                        </span>
                        {{ live ? 'Live' : 'Paused' }}
                    </button>
                </div>
            </div>
        </template>

        <div class="space-y-5 transition-opacity" :class="switching ? 'opacity-60' : ''">
            <!-- Headline numbers -->
            <section class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                <Link :href="statsHref('requests')" class="rounded-2xl bg-white px-5 py-4 shadow-sm ring-1 ring-slate-900/5 transition hover:ring-slate-900/15">
                    <p class="text-[13px] text-slate-600">Requests</p>
                    <p v-if="s" class="mt-1.5 text-3xl font-bold tracking-tight tabular-nums text-slate-900">{{ number(s.requests) }}</p>
                    <div v-else class="mt-2.5 h-7 w-24 animate-pulse rounded bg-slate-100" />
                    <p class="mt-1 text-[13px] text-slate-500">in the {{ PERIOD_LABEL[window] }}<template v-if="s"> · {{ perMinute }} a minute</template></p>
                </Link>
                <Link :href="statsHref('addresses')" class="rounded-2xl bg-white px-5 py-4 shadow-sm ring-1 ring-slate-900/5 transition hover:ring-slate-900/15">
                    <p class="text-[13px] text-slate-600">Visitors</p>
                    <p v-if="s" class="mt-1.5 text-3xl font-bold tracking-tight tabular-nums text-slate-900">{{ number(s.ips) }}</p>
                    <div v-else class="mt-2.5 h-7 w-20 animate-pulse rounded bg-slate-100" />
                    <p class="mt-1 text-[13px] text-slate-500">unique IP addresses</p>
                </Link>
                <Link :href="statsHref('requests')" class="rounded-2xl bg-white px-5 py-4 shadow-sm ring-1 ring-slate-900/5 transition hover:ring-slate-900/15">
                    <p class="text-[13px] text-slate-600">Successful</p>
                    <p v-if="s" class="mt-1.5 text-3xl font-bold tracking-tight tabular-nums text-slate-900">{{ answeredShare }}%</p>
                    <div v-else class="mt-2.5 h-7 w-16 animate-pulse rounded bg-slate-100" />
                    <p class="mt-1 text-[13px] text-slate-500">{{ s ? `${number(answered)} answered 2xx or 3xx` : 'answered 2xx or 3xx' }}</p>
                </Link>
                <Link :href="statsHref('not-found')" class="rounded-2xl bg-white px-5 py-4 shadow-sm ring-1 ring-slate-900/5 transition hover:ring-slate-900/15">
                    <p class="text-[13px] text-slate-600">Blocked or not found</p>
                    <p v-if="s" class="mt-1.5 text-3xl font-bold tracking-tight tabular-nums" :class="s.client_errors ? 'text-amber-700' : 'text-slate-900'">
                        {{ number(s.client_errors) }}
                    </p>
                    <div v-else class="mt-2.5 h-7 w-20 animate-pulse rounded bg-slate-100" />
                    <p class="mt-1 truncate text-[13px] text-slate-500" :title="s ? refusedBreakdown : ''">{{ s ? refusedBreakdown : 'answered 4xx' }}</p>
                </Link>
            </section>

            <!-- Requests over time -->
            <section class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <div class="flex flex-wrap items-center justify-between gap-3">
                    <h3 class="font-semibold text-slate-900">Requests over time</h3>
                    <div class="flex flex-wrap gap-4 text-xs text-slate-600">
                        <span class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-[3px] bg-brand-600" />Answered</span>
                        <span class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-[3px] bg-amber-400" />Blocked / not found</span>
                        <span class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-[3px] bg-rose-700" />Server error</span>
                    </div>
                </div>
                <div v-if="!t" class="mt-4 h-[168px] animate-pulse rounded-lg bg-slate-50" />
                <template v-else>
                    <div class="mt-4 flex h-[168px] items-end gap-1 border-b border-slate-200">
                        <button
                            v-for="b in bars"
                            :key="b.at"
                            type="button"
                            :title="`${b.title} — click to see them`"
                            :aria-pressed="isPicked(b)"
                            class="flex h-full flex-1 cursor-pointer flex-col justify-end rounded-t-[3px] transition-opacity focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
                            :class="[
                                picked && !isPicked(b) ? 'opacity-35 hover:opacity-70' : 'hover:opacity-80',
                                isPicked(b) ? 'bg-slate-100' : '',
                            ]"
                            @click="pick(b)"
                        >
                            <div v-if="b.fail" class="w-full rounded-t-[3px] bg-rose-700" :style="{ height: `${b.fail}%` }" />
                            <div v-if="b.warn" class="w-full bg-amber-400" :class="b.fail ? '' : 'rounded-t-[3px]'" :style="{ height: `${b.warn}%` }" />
                            <div v-if="b.ok" class="w-full bg-brand-600" :class="b.fail || b.warn ? '' : 'rounded-t-[3px]'" :style="{ height: `${b.ok}%` }" />
                        </button>
                    </div>
                    <div class="mt-2 flex justify-between text-xs text-slate-500">
                        <span>{{ AXIS[window]?.[0] }}</span>
                        <span v-if="!picked" class="hidden text-slate-400 sm:inline">Click a bar to see its URLs and requests below</span>
                        <span v-else class="hidden font-medium text-brand-700 sm:inline">
                            {{ pickedLabel }}<template v-if="picked.data"> · {{ number(picked.data.requests) }} requests</template> — shown below
                        </span>
                        <span>{{ AXIS[window]?.[1] }}</span><span>Now</span>
                    </div>
                </template>
            </section>

            <!-- The URLs and requests of the bar picked, or of the whole period -->
            <RecentRequests
                urls
                title="Most requested URLs"
                :subtitle="picked ? 'Every URL in this time, pages and files alike. The most asked for first.' : 'Every URL, pages and files alike. The most asked for first.'"
                :rows="urlRows"
                :total="urlTotal"
                :limit="ROWS"
                :unmatched="unmatched"
                :badge="spanText"
                :badge-tone="spanTone"
                :error="picked?.error ?? ''"
                :all-href="spanHref('urls')"
            >
                <template v-if="picked" #actions>
                    <button
                        type="button"
                        class="h-8 rounded-lg bg-white px-3 text-sm font-medium text-slate-700 ring-1 ring-slate-200 hover:bg-slate-50"
                        @click="close"
                    >
                        Show the {{ PERIOD_LABEL[window] }}
                    </button>
                </template>
            </RecentRequests>

            <RecentRequests
                :rows="requestRows"
                :total="requestTotal"
                :limit="ROWS"
                :unmatched="unmatched"
                :badge="spanText"
                :badge-tone="spanTone"
                :error="picked?.error ?? ''"
                :subtitle="picked ? 'Every request in this time, newest first.' : 'Newest first. Images, scripts and styles are left out.'"
                :all-href="spanHref('requests')"
            />

            <p v-if="picked?.data?.partial?.length" class="text-xs text-slate-500">
                Only the newest part of {{ picked.data.partial.map(siteLabel).join(', ') }}'s log is read for this period, so this time may be missing from it.
            </p>

            <!-- Domains and addresses, latest first -->
            <section class="grid gap-5 lg:grid-cols-2">
                <div class="flex flex-col overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                    <div class="flex items-center justify-between gap-3 border-b border-slate-100 px-5 py-4">
                        <h3 class="font-semibold text-slate-900">Domains</h3>
                        <div role="group" aria-label="Sort domains" class="flex rounded-lg bg-slate-100 p-[3px]">
                            <button
                                v-for="[key, label] in [['recent', 'Latest'], ['busy', 'Busiest']]"
                                :key="key"
                                type="button"
                                :aria-pressed="sitesSort === key"
                                class="h-7 rounded-md px-2.5 text-xs font-medium"
                                :class="sitesSort === key ? 'bg-white text-slate-900 shadow-sm' : 'text-slate-600'"
                                @click="sitesSort = key"
                            >
                                {{ label }}
                            </button>
                        </div>
                    </div>
                    <div v-if="!s" class="space-y-4 p-5">
                        <div v-for="i in LIMIT" :key="i" class="h-10 animate-pulse rounded bg-slate-100" />
                    </div>
                    <p v-else-if="!domains.length" class="flex-1 px-5 py-8 text-center text-sm text-slate-500">No requests in this period.</p>
                    <ul v-else class="flex-1">
                        <li v-for="d in domains.slice(0, LIMIT)" :key="d.domain" class="border-t border-slate-100 px-5 py-3 first:border-t-0">
                            <div class="flex items-baseline justify-between gap-3">
                                <button
                                    type="button"
                                    class="truncate text-left font-medium text-slate-900 hover:text-brand-700 hover:underline"
                                    :title="`Show only ${siteLabel(d.domain)}`"
                                    @click="go({ site: d.domain })"
                                >
                                    {{ siteLabel(d.domain) }}
                                </button>
                                <span class="font-semibold tabular-nums text-slate-900">{{ number(d.requests) }}</span>
                            </div>
                            <div class="mt-2 h-1 overflow-hidden rounded bg-slate-100">
                                <div class="h-full rounded bg-brand-600" :style="{ width: `${Math.max(2, (d.requests / busiestSite) * 100)}%` }" />
                            </div>
                            <div class="mt-1.5 flex flex-wrap gap-x-3 text-xs text-slate-500">
                                <span>Last request {{ ago(d.last, now) }}</span>
                                <span>{{ number(d.ips) }} visitors</span>
                                <span :class="d.client_errors || d.server_errors ? 'text-amber-700' : ''">{{ errorsText(d) }}</span>
                            </div>
                        </li>
                    </ul>
                    <div v-if="s" class="flex items-center justify-between gap-3 border-t border-slate-100 px-5 py-2.5 text-sm text-slate-600">
                        <span class="tabular-nums">{{ Math.min(LIMIT, domains.length) }} of {{ number(domains.length) }} domains</span>
                        <Link :href="statsHref('requests')" class="font-medium text-brand-600 hover:underline">View all →</Link>
                    </div>
                </div>

                <div class="flex flex-col overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                    <div class="flex items-center justify-between gap-3 border-b border-slate-100 px-5 py-4">
                        <h3 class="font-semibold text-slate-900">IP addresses</h3>
                        <div role="group" aria-label="Sort addresses" class="flex rounded-lg bg-slate-100 p-[3px]">
                            <button
                                v-for="[key, label] in [['recent', 'Latest'], ['busy', 'Busiest']]"
                                :key="key"
                                type="button"
                                :aria-pressed="ipsSort === key"
                                class="h-7 rounded-md px-2.5 text-xs font-medium"
                                :class="ipsSort === key ? 'bg-white text-slate-900 shadow-sm' : 'text-slate-600'"
                                @click="ipsSort = key"
                            >
                                {{ label }}
                            </button>
                        </div>
                    </div>
                    <div v-if="!t" class="space-y-4 p-5">
                        <div v-for="i in LIMIT" :key="i" class="h-10 animate-pulse rounded bg-slate-100" />
                    </div>
                    <p v-else-if="!addresses.length" class="flex-1 px-5 py-8 text-center text-sm text-slate-500">No requests in this period.</p>
                    <ul v-else class="flex-1">
                        <li
                            v-for="ip in addresses.slice(0, LIMIT)"
                            :key="ip.ip"
                            class="flex items-center justify-between gap-3 border-t border-slate-100 px-5 py-3 first:border-t-0"
                        >
                            <div class="min-w-0">
                                <div class="flex flex-wrap items-center gap-2">
                                    <Link :href="route('analytics.ip', ip.ip)" class="font-mono text-[13px] font-medium text-slate-900 hover:text-brand-700 hover:underline">
                                        {{ ip.ip }}
                                    </Link>
                                    <span
                                        v-if="ipTag(ip)"
                                        class="rounded-md px-1.5 py-0.5 text-[11px] font-semibold"
                                        :class="ipTag(ip).tone"
                                        :title="ipTag(ip).title ?? ip.verdict"
                                    >
                                        {{ ipTag(ip).label }}
                                    </span>
                                </div>
                                <p class="mt-1 truncate text-xs text-slate-500" :title="ipWhere(ip)">{{ ago(ip.last, now) }} · {{ ipWhere(ip) }}</p>
                            </div>
                            <div class="flex shrink-0 items-center gap-3">
                                <span class="font-semibold tabular-nums text-slate-900">{{ number(ip.requests) }}</span>
                                <button
                                    v-if="ip.danger && edgeAccounts.length"
                                    type="button"
                                    :disabled="Boolean(blocking)"
                                    class="h-8 rounded-lg bg-white px-2.5 text-xs font-medium text-rose-700 ring-1 ring-rose-200 hover:bg-rose-50 disabled:opacity-50"
                                    @click="block(ip)"
                                >
                                    {{ blocking === ip.ip ? 'Blocking…' : 'Block' }}
                                </button>
                            </div>
                        </li>
                    </ul>
                    <div v-if="t" class="flex items-center justify-between gap-3 border-t border-slate-100 px-5 py-2.5 text-sm text-slate-600">
                        <span class="tabular-nums">{{ Math.min(LIMIT, addresses.length) }} of {{ number(s?.ips ?? addresses.length) }} addresses</span>
                        <Link :href="statsHref('addresses')" class="font-medium text-brand-600 hover:underline">View all →</Link>
                    </div>
                </div>
            </section>

            <!-- Supporting detail -->
            <section class="grid gap-5 md:grid-cols-2">
                <div class="flex flex-col rounded-2xl bg-white px-5 py-4 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-900">Responses</h3>
                    <div v-if="!t" class="mt-3 space-y-3">
                        <div v-for="i in LIMIT" :key="i" class="h-5 animate-pulse rounded bg-slate-100" />
                    </div>
                    <template v-else>
                        <div class="mt-3.5 flex h-2 overflow-hidden rounded-full bg-slate-100">
                            <div v-for="c in classes" :key="c.key" :class="CLASS_BAR[c.key]" :style="{ width: `${c.share}%` }" :title="`${c.key}: ${c.share.toFixed(1)}%`" />
                        </div>
                        <p v-if="!codes.length" class="mt-3 flex-1 text-sm text-slate-500">No requests in this period.</p>
                        <ul v-else class="mt-3.5 flex-1 space-y-2 text-sm">
                            <li v-for="c in allCodes ? codes : codes.slice(0, LIMIT)" :key="c.name" class="flex justify-between gap-3">
                                <span>
                                    <span class="inline-block rounded-md px-1.5 py-0.5 font-mono text-xs font-medium" :class="statusTone(c.name)">{{ c.name }}</span>
                                    <span class="ml-1.5 text-slate-600">{{ CODE_LABEL[c.name] ?? '' }}</span>
                                </span>
                                <span class="tabular-nums text-slate-700">{{ number(c.count) }}</span>
                            </li>
                        </ul>
                        <div class="mt-4 flex items-center justify-between border-t border-slate-100 pt-3 text-sm text-slate-600">
                            <span class="tabular-nums">{{ allCodes ? codes.length : Math.min(LIMIT, codes.length) }} of {{ codes.length }} codes</span>
                            <button
                                v-if="codes.length > LIMIT"
                                type="button"
                                class="font-medium text-brand-600 hover:underline"
                                @click="allCodes = !allCodes"
                            >
                                {{ allCodes ? 'Show fewer' : 'View all →' }}
                            </button>
                        </div>
                    </template>
                </div>

                <div class="flex flex-col rounded-2xl bg-white px-5 py-4 shadow-sm ring-1 ring-slate-900/5">
                    <h3 class="font-semibold text-slate-900">Came from</h3>
                    <p class="text-[13px] text-slate-500">Sites that linked visitors here.</p>
                    <div v-if="!t" class="mt-3 space-y-3">
                        <div v-for="i in 4" :key="i" class="h-5 animate-pulse rounded bg-slate-100" />
                    </div>
                    <template v-else>
                        <p v-if="!referrers.length" class="mt-3 flex-1 text-sm text-slate-500">No visitor arrived from another site in this period.</p>
                        <ol v-else class="mt-3 flex-1 space-y-2.5">
                            <li v-for="r in allReferrers ? referrers : referrers.slice(0, LIMIT)" :key="r.name" class="flex justify-between gap-3 text-sm">
                                <a
                                    :href="`https://${r.name}`"
                                    target="_blank"
                                    rel="noopener noreferrer nofollow"
                                    class="min-w-0 truncate text-slate-900 hover:text-brand-700 hover:underline"
                                >
                                    {{ r.name }}
                                </a>
                                <span class="tabular-nums text-slate-700">{{ number(r.count) }}</span>
                            </li>
                        </ol>
                        <div v-if="referrers.length" class="mt-3.5 flex items-center justify-between text-sm text-slate-600">
                            <span class="tabular-nums">{{ allReferrers ? referrers.length : Math.min(LIMIT, referrers.length) }} of {{ number(referrers.length) }} sites</span>
                            <button
                                v-if="referrers.length > LIMIT"
                                type="button"
                                class="font-medium text-brand-600 hover:underline"
                                @click="allReferrers = !allReferrers"
                            >
                                {{ allReferrers ? 'Show fewer' : 'View all →' }}
                            </button>
                        </div>
                        <div class="mt-4 flex justify-between border-t border-slate-100 pt-3.5 text-sm">
                            <span class="text-slate-600">Bots and crawlers</span>
                            <span class="font-semibold tabular-nums text-slate-900">{{ botShare }}%</span>
                        </div>
                    </template>
                </div>
            </section>

            <!-- Attention, only when there is something -->
            <section
                v-if="attention"
                class="flex flex-wrap items-center justify-between gap-3 rounded-2xl bg-amber-50 px-5 py-4 ring-1 ring-amber-200"
            >
                <div class="flex min-w-0 items-start gap-3">
                    <svg class="mt-0.5 h-5 w-5 shrink-0 text-amber-700" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M12 9v4" /><path d="M12 17h.01" /><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
                    </svg>
                    <p class="text-sm text-amber-900">{{ attention }}</p>
                </div>
                <Link :href="statsHref('suspicious')" class="text-sm font-medium text-amber-800 hover:underline">Review attacks →</Link>
            </section>

            <p v-if="s?.partial?.length" class="text-xs text-slate-500">
                Only the newest part of {{ s.partial.map(siteLabel).join(', ') }}'s log was read for this period — the counts for
                {{ s.partial.length === 1 ? 'it' : 'them' }} are the most recent requests.
            </p>
        </div>
    </AuthenticatedLayout>
</template>
