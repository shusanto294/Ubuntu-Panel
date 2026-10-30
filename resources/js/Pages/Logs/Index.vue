<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import { confirmDanger } from '@/dialog';
import { browser } from '@/Components/Security/ui';
import { useLiveRefresh } from '@/Composables/useLiveRefresh';
import { formatDuration, formatRelative, formatStamp } from '@/time';
import { Head, Link, router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    logs: { type: Array, default: () => [] },
    pagination: { type: Object, default: () => ({ page: 1, pages: 1, total: 0 }) },
    type: { type: String, default: null },
    retention: { type: Number, default: 90 },
    retentionOptions: { type: Array, default: () => [7, 30, 90, 180, 365] },
});

const FILTERS = [
    { key: null, label: 'Everything' },
    { key: 'provision', label: 'System' },
    { key: 'site', label: 'Sites' },
    { key: 'database', label: 'Databases' },
    { key: 'mail', label: 'Email' },
    { key: 'mcp', label: 'AI' },
    { key: 'panel-login', label: 'Panel sign-ins' },
    { key: 'ssh', label: 'SSH' },
];

const reasonLabel = {
    password: 'password',
    two_factor: 'two-factor code',
    recovery: 'recovery code',
    locked: 'locked out',
};

// Tasks carry a UTC text stamp; sign-ins and SSH events carry Unix seconds.
const stampOf = (entry) =>
    entry.task?.created_at ?? entry.login?.created_at ?? entry.ssh?.created_at;

const sshTitle = (event) =>
    ({
        accepted: 'SSH login',
        failed: 'SSH: wrong password or key',
        invalid: 'SSH: unknown user',
    })[event.kind] ?? 'SSH attempt';

const anyRunning = computed(() => props.logs.some((entry) => entry.task?.status === 'running'));

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

// Paged on the server, so a page is a visit rather than a slice.
const goToPage = (page) => router.visit(pageHref(page), { preserveScroll: true });

// ---------------------------------------------------------------- clearing

const showClear = ref(false);

const clearForm = useForm({ what: 'all', older_than: '0' });

const WHAT = {
    all: 'every log',
    tasks: 'the panel’s task logs',
    logins: 'the panel sign-in history',
    ssh: 'the SSH login history',
};

const clearLogs = async () => {
    const range =
        clearForm.older_than === '0' ? 'all of it' : `entries older than ${clearForm.older_than} days`;

    if (!(await confirmDanger('Clear logs?', `This deletes ${WHAT[clearForm.what]} — ${range}. It cannot be undone.`, 'Clear')))
        return;

    clearForm.post(route('logs.clear'), {
        preserveScroll: true,
        onSuccess: () => (showClear.value = false),
    });
};

const setRetention = (days) =>
    router.post(route('logs.retention'), { days }, { preserveScroll: true });
</script>

<template>
    <Head title="Logs" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Logs</h2>
                    <p class="text-sm text-slate-500">
                        What the panel ran, every sign-in to it, and every SSH login or attempt,
                        newest first
                        <span v-if="pagination.total">· {{ pagination.total }} in total</span>
                    </p>
                </div>
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    @click="showClear = !showClear"
                >
                    {{ showClear ? 'Close' : 'Clear logs' }}
                </button>
            </div>
        </template>

        <!-- Clearing, and how long logs are kept without anybody clearing them. -->
        <div v-if="showClear" class="mb-6 grid gap-6 lg:grid-cols-2">
            <form
                class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5"
                @submit.prevent="clearLogs"
            >
                <h3 class="font-semibold text-slate-900">Clear logs now</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Deleted from the panel's database, which is then compacted so the space
                    is given back. Tasks still running are kept.
                </p>
                <div class="mt-4 grid gap-3 sm:grid-cols-2">
                    <label class="block text-sm">
                        <span class="text-xs font-medium text-slate-700">Which</span>
                        <select v-model="clearForm.what" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm">
                            <option value="all">Everything</option>
                            <option value="tasks">Panel tasks</option>
                            <option value="logins">Panel sign-ins</option>
                            <option value="ssh">SSH logins and attempts</option>
                        </select>
                    </label>
                    <label class="block text-sm">
                        <span class="text-xs font-medium text-slate-700">How much</span>
                        <select v-model="clearForm.older_than" class="mt-1 block w-full rounded-lg border-slate-300 text-sm shadow-sm">
                            <option value="0">All of it</option>
                            <option value="7">Older than 7 days</option>
                            <option value="30">Older than 30 days</option>
                            <option value="90">Older than 90 days</option>
                        </select>
                    </label>
                </div>
                <button
                    type="submit"
                    :disabled="clearForm.processing"
                    class="mt-4 rounded-xl bg-rose-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-rose-700 disabled:opacity-50"
                >
                    {{ clearForm.processing ? 'Clearing…' : 'Clear' }}
                </button>
            </form>

            <div class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Keep logs for</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Anything older is deleted automatically every hour, so the logs never grow
                    past this.
                </p>
                <div class="mt-4 flex flex-wrap gap-2">
                    <button
                        v-for="days in retentionOptions"
                        :key="days"
                        type="button"
                        class="rounded-lg px-3 py-1.5 text-xs font-medium ring-1 transition"
                        :class="
                            days === retention
                                ? 'bg-brand-600 text-white ring-brand-600'
                                : 'bg-white text-slate-600 ring-slate-200 hover:bg-slate-50'
                        "
                        @click="setRetention(days)"
                    >
                        {{ days === 365 ? '1 year' : `${days} days` }}
                    </button>
                </div>
            </div>
        </div>

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
                Nothing recorded here yet.
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
                            <th class="px-5 py-3">Details</th>
                            <th class="px-5 py-3"></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr
                            v-for="entry in logs"
                            :key="`${entry.source}-${(entry.task ?? entry.login ?? entry.ssh).id}`"
                            class="text-sm transition hover:bg-slate-50"
                        >
                            <td class="whitespace-nowrap px-5 py-3 align-top">
                                <p class="text-slate-900">{{ formatStamp(stampOf(entry)) }}</p>
                                <p class="text-xs text-slate-400">
                                    {{ formatRelative(stampOf(entry)) }}
                                </p>
                            </td>

                            <!-- A task the panel ran. -->
                            <template v-if="entry.task">
                                <td class="px-5 py-3 align-top">
                                    <p class="font-medium text-slate-900">{{ entry.task.action }}</p>
                                    <p class="max-w-md truncate text-xs text-slate-500">
                                        {{ entry.task.message }}
                                    </p>
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 align-top">
                                    <span
                                        class="rounded-full px-2.5 py-0.5 text-xs font-medium ring-1"
                                        :class="statusClass(entry.task.status)"
                                    >
                                        {{ entry.task.status }}
                                    </span>
                                    <span
                                        v-if="entry.task.status === 'running'"
                                        class="ml-2 text-xs text-slate-500"
                                        >{{ entry.task.progress }}%</span
                                    >
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 align-top text-xs text-slate-500">
                                    {{
                                        formatDuration(entry.task.started_at, entry.task.finished_at)
                                            ? `Took ${formatDuration(entry.task.started_at, entry.task.finished_at)}`
                                            : '—'
                                    }}
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 text-right align-top">
                                    <Link
                                        :href="route('logs.show', entry.task.id)"
                                        class="text-xs font-medium text-brand-600 hover:underline"
                                    >
                                        Open
                                    </Link>
                                </td>
                            </template>

                            <!-- A sign-in to this panel, right or wrong. -->
                            <template v-else-if="entry.login">
                                <td class="px-5 py-3 align-top">
                                    <p class="font-medium text-slate-900">Panel sign-in</p>
                                    <p class="max-w-md truncate text-xs text-slate-500">
                                        {{ entry.login.email || 'no email typed' }} ·
                                        {{ reasonLabel[entry.login.reason] ?? entry.login.reason }}
                                    </p>
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 align-top">
                                    <span
                                        class="rounded-full px-2.5 py-0.5 text-xs font-medium ring-1"
                                        :class="statusClass(entry.login.success ? 'success' : 'failed')"
                                    >
                                        {{ entry.login.success ? 'signed in' : 'refused' }}
                                    </span>
                                </td>
                                <td class="px-5 py-3 align-top text-xs text-slate-500">
                                    <IpAddress :ip="entry.login.ip" class="text-slate-700" />
                                    <p :title="entry.login.user_agent">{{ browser(entry.login.user_agent) }}</p>
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 text-right align-top">
                                    <Link
                                        :href="`${route('security.index')}?tab=logins`"
                                        class="text-xs font-medium text-brand-600 hover:underline"
                                    >
                                        Security
                                    </Link>
                                </td>
                            </template>

                            <!-- An SSH login, or an attempt at one. -->
                            <template v-else-if="entry.ssh">
                                <td class="px-5 py-3 align-top">
                                    <p class="font-medium text-slate-900">{{ sshTitle(entry.ssh) }}</p>
                                    <p class="text-xs text-slate-500">
                                        as <span class="font-mono">{{ entry.ssh.user || '—' }}</span>
                                        · {{ entry.ssh.method }}
                                    </p>
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 align-top">
                                    <span
                                        class="rounded-full px-2.5 py-0.5 text-xs font-medium ring-1"
                                        :class="statusClass(entry.ssh.kind === 'accepted' ? 'success' : 'failed')"
                                    >
                                        {{ entry.ssh.kind === 'accepted' ? 'logged in' : 'refused' }}
                                    </span>
                                </td>
                                <td class="px-5 py-3 align-top text-xs">
                                    <IpAddress :ip="entry.ssh.ip" class="text-slate-700" />
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 text-right align-top">
                                    <Link
                                        :href="`${route('security.index')}?tab=ssh`"
                                        class="text-xs font-medium text-brand-600 hover:underline"
                                    >
                                        Security
                                    </Link>
                                </td>
                            </template>
                        </tr>
                    </tbody>
                </table>
            </div>

            <Pagination
                :model-value="pagination.page"
                :pages="pagination.pages"
                :total="pagination.total"
                :per-page="25"
                @update:model-value="goToPage"
            />
        </div>
    </AuthenticatedLayout>
</template>
