<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { Link, router } from '@inertiajs/vue3';

/*
 * Every site's access log, added up over a window, beside what the processor
 * is doing right now. Built for the moment the CPU is pinned: which program
 * is using it, which site the requests are going to, who is sending them and
 * whether that is an attack. Nothing here bans anybody; the firewall closes
 * ports, and Cloudflare's Under Attack mode (Security → Hardening) stops a
 * flood before it reaches the server.
 */
const props = defineProps({
    window: { type: String, default: '1h' },
    windows: { type: Array, default: () => ['1h', '24h', '7d', '30d'] },
    traffic: { type: Object, default: null },
    processes: { type: Object, default: null },
    fpm: { type: Array, default: () => [] },
    // Why each trusted address is trusted.
    trustedIps: { type: Object, default: () => ({}) },

    // Which parts to show: findings, timeline, sites, cpu, ips, attacks,
    // agents, urls. Each stats page shows its own few.
    sections: { type: Array, default: () => ['findings', 'timeline', 'sites', 'cpu', 'ips', 'attacks', 'agents', 'urls'] },

    // Narrowing for a page about one kind of response: the addresses and URLs
    // with the most 404s, or 5xx, or the flagged addresses only.
    focus: { type: String, default: '' }, // '' | 'not-found' | 'server-errors' | 'suspicious'

    // Still being read.
    loading: { type: Boolean, default: false },
});

const show = (section) => props.sections.includes(section);

const WINDOW_LABEL = { '1h': 'Last hour', '24h': 'Last 24 hours', '7d': 'Last 7 days', '30d': 'Last 30 days' };

const THREAT_LABEL = {
    'wp-login': 'WordPress login',
    xmlrpc: 'XML-RPC',
    login: 'Login form',
    probe: 'Scanner',
};

const number = (n) => (n ?? 0).toLocaleString();

const bytes = (n) => {
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
    return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}`;
};

// A different window reloads only the summary; the list below keeps its filters.
const setWindow = (key) =>
    router.get(
        window.location.pathname,
        { ...Object.fromEntries(new URLSearchParams(window.location.search)), window: key },
        {
            preserveScroll: true,
            preserveState: true,
            only: ['window', 'traffic', 'trustedIps', 'processes', 'fpm'],
            onStart: () => (refreshing.value = true),
            onFinish: () => (refreshing.value = false),
        },
    );

const logHref = (site, q) => {
    const source = site.startsWith('(') ? 'nginx-access' : `site:${site}:access`;
    const query = new URLSearchParams({ view: 'files', source });
    if (q) query.set('q', q);
    return `${route('logs.index')}?${query}`;
};

// ------------------------------------------------------------- refreshing

const refreshing = ref(false);
const auto = ref(false);
let timer = null;

const refresh = () => {
    if (refreshing.value || document.hidden) return;

    router.reload({
        only: ['traffic', 'trustedIps', 'processes', 'fpm'],
        preserveScroll: true,
        preserveState: true,
        onStart: () => (refreshing.value = true),
        onFinish: () => (refreshing.value = false),
    });
};

watch(
    auto,
    (on) => {
        clearInterval(timer);
        timer = on ? setInterval(refresh, 30000) : null;
    },
);

onBeforeUnmount(() => clearInterval(timer));

// ---------------------------------------------------------------- reading

const t = computed(() => props.traffic);

const busiestGroup = computed(() => props.processes?.groups?.[0] ?? null);

const phpGroups = computed(() =>
    (props.processes?.groups ?? []).filter((g) => /php|fpm/i.test(g.name)),
);

const dangerous = computed(() => (t.value?.top_ips ?? []).filter((ip) => ip.danger));

const threatTotal = computed(() => (t.value?.threats ?? []).reduce((sum, th) => sum + th.requests, 0));

const statusCount = (cls) => t.value?.statuses?.[cls] ?? 0;

/*
 * The handful of sentences the rest of the page is evidence for. Each one is
 * only said when the numbers say it: no "everything is fine" filler.
 */
const findings = computed(() => {
    const list = [];
    const p = props.processes;
    const r = t.value;

    if (p && busiestGroup.value && busiestGroup.value.cpu >= 20) {
        const g = busiestGroup.value;
        const share = p.cores ? Math.round(g.cpu / p.cores) : null;
        list.push({
            tone: share >= 50 ? 'danger' : 'info',
            text:
                `${g.name} is the busiest program right now: ${g.processes} process${g.processes === 1 ? '' : 'es'} using ` +
                `${g.cpu}% of a core${share !== null ? ` — about ${share}% of the whole machine` : ''}.`,
        });
    }

    if (phpGroups.value.length && r?.sites?.length && r.dynamic) {
        const top = r.sites[0];
        if (top.dynamic_share >= 40 && top.dynamic >= 20) {
            list.push({
                tone: 'info',
                text:
                    `${top.domain} received ${top.dynamic_share}% of the requests that run PHP or the app ` +
                    `(${number(top.dynamic)} of ${number(r.dynamic)}) — if PHP is what is busy, this is the site keeping it busy.`,
            });
        }
    }

    if (dangerous.value.length) {
        const worst = dangerous.value[0];
        list.push({
            tone: 'danger',
            text:
                `${dangerous.value.length} address${dangerous.value.length === 1 ? ' looks' : 'es look'} like an attack. ` +
                `The worst: ${worst.ip} — ${worst.verdict}${worst.sites?.[0] ? `, on ${worst.sites[0].name}` : ''}.`,
            ip: worst.ip,
            reason: worst.verdict,
        });
    }

    for (const th of r?.threats ?? []) {
        if (th.requests >= 50) {
            list.push({
                tone: 'warn',
                text:
                    `${number(th.requests)} ${th.label.toLowerCase()} from ${number(th.ips)} address${th.ips === 1 ? '' : 'es'}` +
                    `${th.sites?.[0] ? `, mostly on ${th.sites[0].name}` : ''}.`,
            });
        }
    }

    for (const f of props.fpm ?? []) {
        list.push({
            tone: 'warn',
            text:
                `PHP-FPM ${f.version} ran out of workers ${f.count} time${f.count === 1 ? '' : 's'} recently ` +
                '(“max_children”): requests queued and every site on it slowed down.',
        });
    }

    const errors = statusCount('5xx');
    if (r && errors >= 20 && errors * 20 >= r.requests) {
        list.push({
            tone: 'warn',
            text: `${number(errors)} requests failed with a server error (5xx). The sites' error logs say why.`,
        });
    }

    return list;
});

const toneClass = {
    danger: 'bg-rose-50 text-rose-800 ring-rose-200',
    warn: 'bg-amber-50 text-amber-800 ring-amber-200',
    info: 'bg-sky-50 text-sky-800 ring-sky-200',
};

// --------------------------------------------------------------- timeline

const peakBucket = computed(() => Math.max(1, ...(t.value?.timeline ?? []).map((b) => b.requests)));

const bucketTitle = (b) => {
    const at = new Date(b.at * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    return `${at} — ${number(b.requests)} requests, ${number(b.dynamic)} to PHP/app, ${number(b.threats)} suspicious`;
};

const axisLabel = (b) => new Date(b.at * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });

const showAllProcesses = ref(false);

// Every list a page at a time.
const sitePages = usePagination(() => t.value?.sites ?? [], 20);
const focusedIps = computed(() => {
    const list = t.value?.top_ips ?? [];
    switch (props.focus) {
        case 'not-found':
            return list.filter((ip) => ip.not_found > 0).sort((a, b) => b.not_found - a.not_found);
        case 'suspicious':
            return list.filter((ip) => ip.danger || ip.verdict || Object.keys(ip.threats ?? {}).length);
        default:
            return list;
    }
});

const focusedPaths = computed(() => {
    const list = t.value?.paths ?? [];
    switch (props.focus) {
        case 'not-found':
            return list.filter((p) => p.not_found > 0).sort((a, b) => b.not_found - a.not_found);
        case 'server-errors':
            return list.filter((p) => p.errors > 0).sort((a, b) => b.errors - a.errors);
        case 'suspicious':
            return list.filter((p) => p.threat);
        default:
            return list;
    }
});

const ipPages = usePagination(focusedIps, 20);
const pathPages = usePagination(focusedPaths, 20);
const agentPages = usePagination(() => t.value?.agents ?? [], 20);
const groupPages = usePagination(() => props.processes?.groups ?? [], 20);
const processPages = usePagination(() => props.processes?.top ?? [], 20);
</script>

<template>
    <div class="space-y-6">
        <!-- Window and refreshing -->
        <div class="flex flex-wrap items-center gap-2">
            <label class="flex items-center gap-2 text-sm">
                <span class="text-xs font-medium text-slate-700">Traffic over</span>
                <select
                    :value="window"
                    class="rounded-lg border-slate-300 text-sm shadow-sm"
                    @change="setWindow($event.target.value)"
                >
                    <option v-for="key in windows" :key="key" :value="key">{{ WINDOW_LABEL[key] ?? key }}</option>
                </select>
            </label>

            <div class="ml-auto flex flex-wrap items-center gap-3 text-xs text-slate-500">
                <span v-if="traffic">
                    Read {{ number(traffic.scanned) }} log lines in {{ (traffic.took_ms / 1000).toFixed(1) }}s
                </span>
                <label class="inline-flex items-center gap-1.5">
                    <input v-model="auto" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                    Every 30s
                </label>
                <button
                    type="button"
                    :disabled="refreshing"
                    class="rounded-lg bg-white px-3 py-1.5 font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50 disabled:opacity-50"
                    @click="refresh"
                >
                    {{ refreshing ? 'Reading…' : 'Refresh' }}
                </button>
            </div>
        </div>

        <div v-if="loading && !traffic" class="rounded-2xl bg-white px-5 py-12 text-center text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5">
            Reading the access logs…
        </div>

        <!-- What stands out -->
        <div v-if="show('findings')" class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
            <h3 class="font-semibold text-slate-900">What stands out</h3>
            <ul v-if="findings.length" class="mt-3 space-y-2">
                <li
                    v-for="(f, i) in findings"
                    :key="i"
                    class="flex flex-wrap items-center justify-between gap-2 rounded-xl px-4 py-2.5 text-sm ring-1"
                    :class="toneClass[f.tone]"
                >
                    <span class="min-w-0 flex-1 break-words">{{ f.text }}</span>
                </li>
            </ul>
            <p v-else class="mt-2 text-sm text-slate-500">
                Nothing unusual in {{ (WINDOW_LABEL[window] ?? window).toLowerCase() }}: no address is
                attacking, no program is pinning the CPU and PHP has not run out of workers.
            </p>
        </div>

        <p v-if="traffic?.partial?.length" class="text-xs text-amber-700">
            {{ traffic.partial.join(', ') }} had more requests in this window than the panel reads at once, so
            the counts for {{ traffic.partial.length === 1 ? 'it' : 'them' }} are the most recent part of it.
        </p>

        <!-- Timeline -->
        <div v-if="show('timeline') && traffic?.timeline?.length" class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
            <div class="flex flex-wrap items-center justify-between gap-2">
                <h3 class="font-semibold text-slate-900">Requests over time</h3>
                <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-slate-500">
                    <span class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-sm bg-brand-500"></span>PHP / app</span>
                    <span class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-sm bg-slate-300"></span>Static files</span>
                    <span class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-sm bg-rose-500"></span>Suspicious</span>
                </div>
            </div>
            <div class="mt-4 flex h-36 items-end gap-px">
                <div
                    v-for="b in traffic.timeline"
                    :key="b.at"
                    class="group relative flex h-full flex-1 flex-col justify-end"
                    :title="bucketTitle(b)"
                >
                    <div
                        class="w-full rounded-t-sm bg-slate-300"
                        :style="{ height: `${((b.requests - b.dynamic) / peakBucket) * 100}%` }"
                    ></div>
                    <div
                        class="relative w-full bg-brand-500"
                        :style="{ height: `${(b.dynamic / peakBucket) * 100}%` }"
                    >
                        <div
                            v-if="b.threats"
                            class="absolute inset-x-0 bottom-0 bg-rose-500"
                            :style="{ height: `${Math.min(100, (b.threats / Math.max(1, b.dynamic)) * 100)}%` }"
                        ></div>
                    </div>
                </div>
            </div>
            <div class="mt-1 flex justify-between text-[11px] text-slate-400">
                <span>{{ axisLabel(traffic.timeline[0]) }}</span>
                <span>peak {{ number(peakBucket) }} per bar</span>
                <span>now</span>
            </div>
        </div>

        <div class="grid gap-6 xl:grid-cols-2">
            <!-- Sites -->
            <div v-if="show('sites') && traffic" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-2">
                <div class="px-5 pt-5">
                    <h3 class="font-semibold text-slate-900">Which sites are getting the requests</h3>
                    <p class="text-sm text-slate-500">
                        Ordered by requests that reach PHP or the app — the ones that use CPU. Static files are
                        served by nginx and cost almost nothing.
                    </p>
                </div>
                <div v-if="!traffic?.sites?.length" class="px-5 py-8 text-center text-sm text-slate-500">
                    Nothing to show for this period.
                </div>
                <div v-else class="mt-3 overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200 text-sm">
                        <thead class="bg-slate-50">
                            <tr class="text-left text-xs font-semibold uppercase tracking-wider text-slate-500">
                                <th class="px-5 py-2.5">Site</th>
                                <th class="px-5 py-2.5">PHP / app requests</th>
                                <th class="px-5 py-2.5 text-right">All requests</th>
                                <th class="px-5 py-2.5 text-right">Peak / min</th>
                                <th class="px-5 py-2.5 text-right">Addresses</th>
                                <th class="px-5 py-2.5 text-right">4xx</th>
                                <th class="px-5 py-2.5 text-right">5xx</th>
                                <th class="px-5 py-2.5 text-right">Suspicious</th>
                                <th class="px-5 py-2.5"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100">
                            <tr v-for="s in sitePages.visible.value" :key="s.domain" class="hover:bg-slate-50">
                                <td class="whitespace-nowrap px-5 py-2.5 font-medium text-slate-900">{{ s.domain }}</td>
                                <td class="min-w-[12rem] px-5 py-2.5">
                                    <div class="flex items-center gap-2">
                                        <div class="h-2 flex-1 overflow-hidden rounded-full bg-slate-100">
                                            <div class="h-full rounded-full bg-brand-500" :style="{ width: `${s.dynamic_share}%` }"></div>
                                        </div>
                                        <span class="w-24 text-right text-xs text-slate-600">
                                            {{ number(s.dynamic) }} · {{ s.dynamic_share }}%
                                        </span>
                                    </div>
                                </td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ number(s.requests) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ number(s.peak_per_minute) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ number(s.ips) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-500">{{ number(s.client_errors) }}</td>
                                <td class="px-5 py-2.5 text-right" :class="s.server_errors ? 'text-rose-600' : 'text-slate-500'">
                                    {{ number(s.server_errors) }}
                                </td>
                                <td class="px-5 py-2.5 text-right" :class="s.threats ? 'font-medium text-amber-600' : 'text-slate-500'">
                                    {{ number(s.threats) }}
                                </td>
                                <td class="whitespace-nowrap px-5 py-2.5 text-right">
                                    <a :href="logHref(s.domain)" class="text-xs font-medium text-brand-600 hover:underline">Raw log</a>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                    <Pagination v-model="sitePages.page.value" :pages="sitePages.pages.value" :total="sitePages.total.value" :per-page="sitePages.perPage" />
                </div>
            </div>

            <!-- CPU now -->
            <div v-if="show('cpu')" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-2">
                <div class="flex flex-wrap items-start justify-between gap-2 px-5 pt-5">
                    <div class="min-w-0">
                        <h3 class="font-semibold text-slate-900">What is using the CPU right now</h3>
                        <p class="text-sm text-slate-500">
                            Measured over one second when this page was read. 100% is one full core
                            <span v-if="processes">— this machine has {{ processes.cores }}</span>.
                        </p>
                    </div>
                    <div v-if="processes" class="text-right text-sm">
                        <p class="font-semibold text-slate-900">{{ processes.cpu }}% of the machine</p>
                        <p class="text-xs text-slate-500">
                            load {{ processes.load.map((l) => l.toFixed(2)).join(' · ') }}
                        </p>
                    </div>
                </div>
                <div v-if="processes" class="mt-3 overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200 text-sm">
                        <thead class="bg-slate-50">
                            <tr class="text-left text-xs font-semibold uppercase tracking-wider text-slate-500">
                                <th class="px-5 py-2.5">Program</th>
                                <th class="px-5 py-2.5">CPU</th>
                                <th class="px-5 py-2.5 text-right">Processes</th>
                                <th class="px-5 py-2.5 text-right">Memory</th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100">
                            <tr v-for="g in groupPages.visible.value" :key="g.name">
                                <td class="whitespace-nowrap px-5 py-2.5 font-mono text-xs text-slate-900">{{ g.name }}</td>
                                <td class="min-w-[12rem] px-5 py-2.5">
                                    <div class="flex items-center gap-2">
                                        <div class="h-2 flex-1 overflow-hidden rounded-full bg-slate-100">
                                            <div
                                                class="h-full rounded-full"
                                                :class="g.cpu / processes.cores >= 50 ? 'bg-rose-500' : 'bg-brand-500'"
                                                :style="{ width: `${Math.min(100, g.cpu / Math.max(1, processes.cores))}%` }"
                                            ></div>
                                        </div>
                                        <span class="w-16 text-right text-xs text-slate-600">{{ g.cpu }}%</span>
                                    </div>
                                </td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ g.processes }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ g.memory_mb.toFixed(0) }} MB</td>
                            </tr>
                        </tbody>
                    </table>
                    <Pagination v-model="groupPages.page.value" :pages="groupPages.pages.value" :total="groupPages.total.value" :per-page="groupPages.perPage" />
                    <button
                        type="button"
                        class="w-full border-t border-slate-100 px-5 py-2.5 text-left text-xs font-medium text-brand-600 hover:bg-slate-50"
                        @click="showAllProcesses = !showAllProcesses"
                    >
                        {{ showAllProcesses ? 'Hide' : 'Show' }} the individual processes
                    </button>
                    <table v-if="showAllProcesses" class="min-w-full divide-y divide-slate-200 text-xs">
                        <thead class="bg-slate-50">
                            <tr class="text-left font-semibold uppercase tracking-wider text-slate-500">
                                <th class="px-5 py-2">PID</th>
                                <th class="px-5 py-2">User</th>
                                <th class="px-5 py-2 text-right">CPU</th>
                                <th class="px-5 py-2 text-right">Memory</th>
                                <th class="px-5 py-2">Command</th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100">
                            <tr v-for="p in processPages.visible.value" :key="p.pid">
                                <td class="px-5 py-2 font-mono text-slate-500">{{ p.pid }}</td>
                                <td class="px-5 py-2 text-slate-700">{{ p.user }}</td>
                                <td class="px-5 py-2 text-right text-slate-900">{{ p.cpu }}%</td>
                                <td class="px-5 py-2 text-right text-slate-700">{{ p.memory_mb.toFixed(0) }} MB</td>
                                <td class="max-w-xl truncate px-5 py-2 font-mono text-slate-600" :title="p.command">{{ p.command }}</td>
                            </tr>
                        </tbody>
                    </table>
                    <Pagination
                        v-if="showAllProcesses"
                        v-model="processPages.page.value"
                        :pages="processPages.pages.value"
                        :total="processPages.total.value"
                        :per-page="processPages.perPage"
                    />
                </div>
            </div>

            <!-- Addresses -->
            <div v-if="show('ips') && traffic" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-2">
                <div class="px-5 pt-5">
                    <h3 class="font-semibold text-slate-900">Who is sending them</h3>
                    <p class="text-sm text-slate-500">
                        The busiest addresses, with the ones that look like an attack first.
                    </p>
                </div>
                <div v-if="!focusedIps.length" class="px-5 py-8 text-center text-sm text-slate-500">
                    Nothing to show for this period.
                </div>
                <div v-else class="mt-3 overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200 text-sm">
                        <thead class="bg-slate-50">
                            <tr class="text-left text-xs font-semibold uppercase tracking-wider text-slate-500">
                                <th class="px-5 py-2.5">Address</th>
                                <th class="px-5 py-2.5 text-right">Requests</th>
                                <th class="px-5 py-2.5 text-right">PHP / app</th>
                                <th class="px-5 py-2.5 text-right">Peak / min</th>
                                <th class="px-5 py-2.5">Mostly asking for</th>
                                <th class="px-5 py-2.5">Reading</th>
                                <th class="px-5 py-2.5"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100">
                            <tr
                                v-for="ip in ipPages.visible.value"
                                :key="ip.ip"
                                class="align-top hover:bg-slate-50"
                                :class="ip.danger ? 'bg-rose-50/40' : ''"
                            >
                                <td class="whitespace-nowrap px-5 py-2.5">
                                    <IpAddress :ip="ip.ip" class="text-slate-900" />
                                    <span
                                        v-if="ip.trusted"
                                        class="ml-1 rounded bg-emerald-50 px-1.5 py-0.5 text-[10px] font-medium text-emerald-700"
                                        :title="`Never flagged: ${ip.trusted}`"
                                        >trusted · {{ ip.trusted }}</span
                                    >
                                    <p class="max-w-[16rem] truncate text-xs text-slate-400" :title="ip.agent">
                                        <span
                                            v-if="ip.good_bot"
                                            class="mr-1 rounded bg-sky-50 px-1 py-0.5 font-sans text-[10px] font-medium text-sky-700"
                                            >{{ ip.good_bot.name }}</span
                                        >{{ ip.bot && !ip.good_bot ? '🤖 ' : '' }}{{ ip.agent || 'no user agent' }}
                                    </p>
                                </td>
                                <td class="px-5 py-2.5 text-right text-slate-900">{{ number(ip.requests) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ number(ip.dynamic) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ number(ip.peak_per_minute) }}</td>
                                <td class="px-5 py-2.5 text-xs">
                                    <p v-for="site in ip.sites.slice(0, 2)" :key="site.name" class="text-slate-500">
                                        {{ site.name }} · {{ number(site.count) }}
                                    </p>
                                    <p
                                        v-if="ip.paths[0]"
                                        class="max-w-[18rem] truncate font-mono text-slate-700"
                                        :title="ip.paths.map((p) => `${p.name} (${p.count})`).join('\n')"
                                    >
                                        {{ ip.paths[0].name }} · {{ number(ip.paths[0].count) }}
                                    </p>
                                </td>
                                <td class="px-5 py-2.5 text-xs">
                                    <span
                                        v-if="ip.verdict"
                                        class="inline-block rounded-lg px-2 py-0.5 ring-1"
                                        :class="ip.danger ? 'bg-rose-50 text-rose-700 ring-rose-200' : 'bg-amber-50 text-amber-700 ring-amber-200'"
                                    >
                                        {{ ip.verdict }}
                                    </span>
                                    <span v-else class="text-slate-400">—</span>
                                </td>
                                <td class="whitespace-nowrap px-5 py-2.5 text-right">
                                    <Link
                                        :href="route('logs.ip', ip.ip)"
                                        class="text-xs font-medium text-brand-600 hover:underline"
                                    >
                                        Details
                                    </Link>
                                </td>
                            </tr>
                        </tbody>
                    </table>
                    <Pagination v-model="ipPages.page.value" :pages="ipPages.pages.value" :total="ipPages.total.value" :per-page="ipPages.perPage" />
                </div>
            </div>

            <!-- Attacks -->
            <div v-if="show('attacks') && traffic" class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Attacks and probing</h3>
                <p class="text-sm text-slate-500">
                    Login posts, XML-RPC and requests for files only an attacker asks for.
                </p>
                <p v-if="!traffic?.threats?.length" class="mt-4 text-sm text-slate-500">None in this window.</p>
                <div v-for="th in traffic?.threats ?? []" :key="th.kind" class="mt-4 rounded-xl bg-slate-50 p-4">
                    <div class="flex flex-wrap items-baseline justify-between gap-x-2">
                        <p class="font-medium text-slate-900">{{ th.label }}</p>
                        <p class="text-sm text-slate-600">
                            {{ number(th.requests) }} from {{ number(th.ips) }} address{{ th.ips === 1 ? '' : 'es' }}
                        </p>
                    </div>
                    <p class="mt-1 text-xs text-slate-500">
                        On {{ th.sites.map((s) => `${s.name} (${number(s.count)})`).join(', ') }}
                    </p>
                    <ul class="mt-2 space-y-1">
                        <li v-for="ip in th.top_ips.slice(0, 8)" :key="ip.name" class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs">
                            <IpAddress :ip="ip.name" class="text-slate-700" />
                            <span class="flex items-center gap-3">
                                <span class="text-slate-500">{{ number(ip.count) }}</span>
                                <span v-if="trustedIps[ip.name]" class="text-emerald-700">trusted</span>
                                <Link :href="route('logs.ip', ip.name)" class="font-medium text-brand-600 hover:underline">Details</Link>
                            </span>
                        </li>
                    </ul>
                </div>
            </div>

            <!-- User agents -->
            <div v-if="show('agents') && traffic" class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Browsers and bots</h3>
                <p class="text-sm text-slate-500">What the requests said they were. A bot can lie; a browser rarely sends 1,000 requests.</p>
                <ul class="mt-4 space-y-2">
                    <li v-for="a in agentPages.visible.value" :key="a.agent" class="flex items-start justify-between gap-3 text-xs">
                        <span class="min-w-0 break-all font-mono text-slate-700">
                            <span v-if="a.bot" class="mr-1 rounded bg-amber-100 px-1 py-0.5 font-sans text-[10px] font-medium text-amber-800">bot</span>
                            {{ a.agent || '(none)' }}
                        </span>
                        <span class="shrink-0 text-slate-500">{{ number(a.requests) }}</span>
                    </li>
                </ul>
                <Pagination
                    class="-mx-5 -mb-5 mt-3"
                    v-model="agentPages.page.value"
                    :pages="agentPages.pages.value"
                    :total="agentPages.total.value"
                    :per-page="agentPages.perPage"
                />
            </div>

            <!-- URLs -->
            <div v-if="show('urls') && traffic" class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 xl:col-span-2">
                <div class="px-5 pt-5">
                    <h3 class="font-semibold text-slate-900">Most requested URLs</h3>
                    <p class="text-sm text-slate-500">Without the query string, per site.</p>
                </div>
                <div v-if="focusedPaths.length" class="mt-3 overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200 text-sm">
                        <thead class="bg-slate-50">
                            <tr class="text-left text-xs font-semibold uppercase tracking-wider text-slate-500">
                                <th class="px-5 py-2.5">URL</th>
                                <th class="px-5 py-2.5">Site</th>
                                <th class="px-5 py-2.5 text-right">Requests</th>
                                <th class="px-5 py-2.5 text-right">Addresses</th>
                                <th class="px-5 py-2.5 text-right">404</th>
                                <th class="px-5 py-2.5 text-right">5xx</th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100">
                            <tr v-for="p in pathPages.visible.value" :key="p.site + p.path" class="hover:bg-slate-50">
                                <td class="min-w-[12rem] max-w-md px-5 py-2.5">
                                    <span class="block truncate font-mono text-xs text-slate-900" :title="p.path">{{ p.path }}</span>
                                    <span class="text-[11px] text-slate-400">{{ p.dynamic ? 'PHP / app' : 'static' }}</span>
                                    <span
                                        v-if="p.threat"
                                        class="ml-2 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800"
                                        >{{ THREAT_LABEL[p.threat] ?? p.threat }}</span
                                    >
                                </td>
                                <td class="whitespace-nowrap px-5 py-2.5 text-xs text-slate-500">{{ p.site }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-900">{{ number(p.requests) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-700">{{ number(p.ips) }}</td>
                                <td class="px-5 py-2.5 text-right text-slate-500">{{ number(p.not_found) }}</td>
                                <td class="px-5 py-2.5 text-right" :class="p.errors ? 'text-rose-600' : 'text-slate-500'">
                                    {{ number(p.errors) }}
                                </td>
                            </tr>
                        </tbody>
                    </table>
                    <Pagination v-model="pathPages.page.value" :pages="pathPages.pages.value" :total="pathPages.total.value" :per-page="pathPages.perPage" />
                </div>
                <p v-else class="px-5 py-8 text-center text-sm text-slate-500">Nothing to show for this period.</p>
            </div>
        </div>
    </div>
</template>
