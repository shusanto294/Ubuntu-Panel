<script setup>
import { computed, ref, watch } from 'vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import { browser } from '@/Components/Security/ui';
import { useLiveRefresh } from '@/Composables/useLiveRefresh';
import { formatDuration, formatRelative, formatStamp } from '@/time';
import { Link, router } from '@inertiajs/vue3';

/*
 * A list of what happened, newest first, filtered and paged on the server:
 * the lines the server's own programs logged that the panel has copied in
 * (view "server"), or what the panel ran, its sign-ins and SSH ("panel").
 */
const props = defineProps({
    view: { type: String, default: 'server' },
    events: { type: Array, default: () => [] },
    filters: { type: Object, default: () => ({}) },
    pagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0, per_page: 20 }) },
    counts: { type: Object, default: () => ({}) },
    ranges: { type: Array, default: () => [] },
    sources: { type: Array, default: () => [] },
    sites: { type: Array, default: () => [] },
});

const form = ref({
    range: props.filters.range ?? '1h',
    source: props.filters.source ?? '',
    level: props.filters.level ?? '',
    site: props.filters.site ?? '',
    q: props.filters.q ?? '',
});

watch(
    () => props.filters,
    (filters) => {
        form.value = {
            range: filters.range ?? '1h',
            source: filters.source ?? '',
            level: filters.level ?? '',
            site: filters.site ?? '',
            q: filters.q ?? '',
        };
    },
);

const loading = ref(false);

const visit = (page = 1) => {
    const query = { view: props.view };

    for (const [key, value] of Object.entries(form.value)) {
        if (value) query[key] = value;
    }
    if (page > 1) query.page = page;

    router.get(route('logs.index'), query, {
        preserveScroll: true,
        preserveState: true,
        // On Server logs the boxes above follow the period too.
        only: ['events', 'filters', 'pagination', 'counts', 'sites', ...(props.view === 'server' ? ['stats'] : [])],
        onStart: () => (loading.value = true),
        onFinish: () => (loading.value = false),
    });
};

const totalCount = computed(() => props.sources.reduce((sum, s) => sum + (props.counts[s.key] ?? 0), 0));

// Kept current while a task the panel runs is still going.
const anyRunning = computed(() => props.events.some((entry) => entry.task?.status === 'running'));
useLiveRefresh(anyRunning, ['events', 'pagination', 'counts'], 3000, { onLoad: false });

// ---------------------------------------------------------------- reading

const SOURCE_STYLE = {
    task: 'bg-brand-50 text-brand-700',
    panel: 'bg-indigo-50 text-indigo-700',
    ssh: 'bg-cyan-50 text-cyan-700',
    requests: 'bg-orange-50 text-orange-700',
    site: 'bg-emerald-50 text-emerald-700',
    nginx: 'bg-green-50 text-green-700',
    php: 'bg-violet-50 text-violet-700',
    database: 'bg-sky-50 text-sky-700',
    fail2ban: 'bg-rose-50 text-rose-700',
    auth: 'bg-amber-50 text-amber-700',
    kernel: 'bg-slate-200 text-slate-800',
    system: 'bg-slate-100 text-slate-700',
    mail: 'bg-fuchsia-50 text-fuchsia-700',
};

const LEVEL_STYLE = {
    critical: 'bg-rose-600 text-white',
    error: 'bg-rose-50 text-rose-700 ring-1 ring-rose-200',
    warning: 'bg-amber-50 text-amber-700 ring-1 ring-amber-200',
    info: 'bg-slate-50 text-slate-500 ring-1 ring-slate-200',
};

const sourceLabel = computed(() => Object.fromEntries(props.sources.map((s) => [s.key, s.label])));

const reasonLabel = { password: 'password', two_factor: 'two-factor code', recovery: 'recovery code', locked: 'locked out' };

const sshTitle = (event) =>
    ({ accepted: 'SSH login', failed: 'SSH: wrong password or key', invalid: 'SSH: unknown user' })[event.kind] ?? 'SSH attempt';

// Every entry as one shape for the row.
const rows = computed(() =>
    props.events.map((entry) => {
        if (entry.task) {
            const t = entry.task;
            return {
                key: `task-${t.id}`,
                at: t.created_at,
                source: 'task',
                sourceLabel: 'Panel',
                level: t.status === 'failed' ? 'error' : 'info',
                title: t.action,
                message: t.message,
                status: t.status,
                extra: formatDuration(t.started_at, t.finished_at) ? `took ${formatDuration(t.started_at, t.finished_at)}` : '',
                link: route('logs.show', t.id),
            };
        }
        if (entry.login) {
            const l = entry.login;
            return {
                key: `login-${l.id}`,
                at: l.created_at,
                source: 'panel',
                sourceLabel: 'Panel sign-in',
                level: l.success ? 'info' : 'warning',
                title: l.success ? 'Signed in to the panel' : 'Panel sign-in refused',
                message: `${l.email || 'no email typed'} · ${reasonLabel[l.reason] ?? l.reason} · ${browser(l.user_agent)}`,
                ip: l.ip,
            };
        }
        if (entry.ssh) {
            const e = entry.ssh;
            return {
                key: `ssh-${e.id}`,
                at: e.created_at,
                source: 'ssh',
                sourceLabel: 'SSH',
                level: e.kind === 'accepted' ? 'info' : 'warning',
                title: sshTitle(e),
                message: `as ${e.user || '—'} · ${e.method}`,
                ip: e.ip,
            };
        }
        const e = entry.event;
        return {
            key: `event-${e.id}`,
            at: e.created_at,
            source: e.source,
            sourceLabel: sourceLabel.value[e.source] ?? e.source,
            level: e.level,
            message: e.message,
            site: e.site,
            ip: e.ip,
            mono: true,
        };
    }),
);

const expanded = ref(new Set());
const toggle = (key) => {
    const next = new Set(expanded.value);
    next.has(key) ? next.delete(key) : next.add(key);
    expanded.value = next;
};
</script>

<template>
    <div class="space-y-4">
        <!-- Every filter is a field: period, source, level, site, text. -->
        <form class="flex flex-wrap items-end gap-3 rounded-2xl bg-white p-4 shadow-sm ring-1 ring-slate-900/5" @submit.prevent="visit()">
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Period</span>
                <select v-model="form.range" class="mt-1 block rounded-lg border-slate-300 text-sm shadow-sm" @change="visit()">
                    <option v-for="r in ranges" :key="r.key" :value="r.key">{{ r.label }}</option>
                </select>
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Source</span>
                <select v-model="form.source" class="mt-1 block rounded-lg border-slate-300 text-sm shadow-sm" @change="visit()">
                    <option value="">All sources · {{ totalCount.toLocaleString() }}</option>
                    <option v-for="s in sources" :key="s.key" :value="s.key">
                        {{ s.label }} · {{ (counts[s.key] ?? 0).toLocaleString() }}
                    </option>
                </select>
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Level</span>
                <select v-model="form.level" class="mt-1 block rounded-lg border-slate-300 text-sm shadow-sm" @change="visit()">
                    <option value="">Everything</option>
                    <option value="warning">Warnings and errors</option>
                    <option value="error">Errors only</option>
                </select>
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Site</span>
                <select v-model="form.site" class="mt-1 block max-w-[16rem] rounded-lg border-slate-300 text-sm shadow-sm" @change="visit()">
                    <option value="">{{ view === 'panel' ? 'All sites and the panel' : 'All sites and the server' }}</option>
                    <option v-for="site in sites" :key="site" :value="site">{{ site }}</option>
                </select>
            </label>
            <label class="block min-w-[14rem] flex-1 text-sm">
                <span class="text-xs font-medium text-slate-700">Containing</span>
                <input
                    v-model="form.q"
                    type="search"
                    maxlength="200"
                    :placeholder="view === 'panel' ? 'an action, an email, an IP, a user…' : 'an IP, a URL, “timeout”…'"
                    class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm"
                />
            </label>
            <button
                type="submit"
                :disabled="loading"
                class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
            >
                {{ loading ? 'Reading…' : 'Search' }}
            </button>
        </form>

        <p v-if="view === 'server'" class="text-xs text-slate-500">
            Copied in from the server's log files every minute. Ordinary successful requests are only counted in
            the boxes above; every request is still in the access logs under Log files.
        </p>

        <div class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <div v-if="!rows.length" class="px-5 py-12 text-center text-sm text-slate-500">
                Nothing matches in this period.
            </div>

            <div v-else class="overflow-x-auto">
                <table class="min-w-full divide-y divide-slate-200 text-sm">
                    <thead class="bg-slate-50">
                        <tr class="text-left text-xs font-semibold uppercase tracking-wider text-slate-500">
                            <th class="px-4 py-3">When</th>
                            <th class="px-4 py-3">Source</th>
                            <th class="px-4 py-3">What happened</th>
                            <th class="px-4 py-3">Address</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="row in rows" :key="row.key" class="align-top hover:bg-slate-50">
                            <td class="whitespace-nowrap px-4 py-2.5">
                                <p class="text-slate-900">{{ formatStamp(row.at) }}</p>
                                <p class="text-xs text-slate-400">{{ formatRelative(row.at) }}</p>
                            </td>
                            <td class="whitespace-nowrap px-4 py-2.5">
                                <span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="SOURCE_STYLE[row.source] ?? SOURCE_STYLE.system">
                                    {{ row.sourceLabel }}
                                </span>
                                <p v-if="row.site" class="mt-1 text-xs text-slate-500">{{ row.site }}</p>
                            </td>
                            <td class="min-w-[20rem] px-4 py-2.5">
                                <div class="flex items-start gap-2">
                                    <span
                                        v-if="row.level !== 'info'"
                                        class="mt-0.5 shrink-0 rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase"
                                        :class="LEVEL_STYLE[row.level]"
                                        >{{ row.level }}</span
                                    >
                                    <div class="min-w-0 flex-1">
                                        <p v-if="row.title" class="font-medium text-slate-900">
                                            {{ row.title }}
                                            <span v-if="row.status" class="ml-1 text-xs font-normal text-slate-500">
                                                {{ row.status }}<template v-if="row.extra"> · {{ row.extra }}</template>
                                            </span>
                                        </p>
                                        <p
                                            v-if="row.message"
                                            class="whitespace-pre-wrap break-all text-xs"
                                            :class="[row.mono ? 'font-mono text-slate-700' : 'text-slate-500', expanded.has(row.key) ? '' : 'line-clamp-2']"
                                        >
                                            {{ row.message }}
                                        </p>
                                        <div class="mt-0.5 flex gap-3 text-xs">
                                            <button
                                                v-if="row.message && (row.message.length > 160 || row.message.includes('\n'))"
                                                type="button"
                                                class="font-medium text-brand-600 hover:underline"
                                                @click="toggle(row.key)"
                                            >
                                                {{ expanded.has(row.key) ? 'Less' : 'More' }}
                                            </button>
                                            <Link v-if="row.link" :href="row.link" class="font-medium text-brand-600 hover:underline">
                                                Open
                                            </Link>
                                        </div>
                                    </div>
                                </div>
                            </td>
                            <td class="whitespace-nowrap px-4 py-2.5 text-xs">
                                <IpAddress v-if="row.ip" :ip="row.ip" class="text-slate-700" />
                                <span v-else class="text-slate-300">—</span>
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>

            <Pagination
                :model-value="pagination.page"
                :pages="pagination.pages"
                :total="pagination.total"
                :per-page="pagination.per_page"
                @update:model-value="visit"
            />
        </div>
    </div>
</template>
