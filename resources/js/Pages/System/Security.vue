<script setup>
import { computed, onMounted, ref, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import SshProtection from '@/Components/Security/SshProtection.vue';
import SshUsers from '@/Components/Security/SshUsers.vue';
import LoginActivity from '@/Components/Security/LoginActivity.vue';
import Firewall from '@/Components/Security/Firewall.vue';
import Hardening from '@/Components/Security/Hardening.vue';
import Updates from '@/Components/Security/Updates.vue';
import Sessions from '@/Components/Security/Sessions.vue';
import Alerts from '@/Components/Security/Alerts.vue';
import { ago, card } from '@/Components/Security/ui';
import { Head, Link, router } from '@inertiajs/vue3';

const props = defineProps({
    score: Number,
    checks: Array,
    myIp: String,
    ssh: Object,
    users: Array,
    hardening: Object,
    sshService: Object,
    firewall: Object,
    panelPort: Number,
    updates: Object,
    logins: Object,
    sessions: Array,
    alerts: Object,
    smtpReady: Boolean,
    checkedAt: Number,
    activeTask: Object,
    latestTask: Object,
});

// One page, many tabs, each its own part of the machine. The tab lives in the
// URL's query so a reload, a bookmark or a link from the dashboard lands on
// the same one.
const tabs = [
    { key: 'overview', name: 'Overview' },
    { key: 'ssh', name: 'SSH protection' },
    { key: 'users', name: 'SSH users' },
    { key: 'logins', name: 'Panel logins' },
    { key: 'firewall', name: 'Firewall' },
    { key: 'hardening', name: 'SSH settings' },
    { key: 'updates', name: 'Updates' },
    { key: 'sessions', name: 'Sessions' },
    { key: 'alerts', name: 'Alerts' },
];

const tab = ref('overview');

onMounted(() => {
    const asked = new URLSearchParams(window.location.search).get('tab');
    if (tabs.some((t) => t.key === asked)) tab.value = asked;
});

watch(tab, (value) => {
    const url = new URL(window.location.href);
    url.searchParams.set('tab', value);
    window.history.replaceState(window.history.state, '', url);
});

// A check's "fix it" goes to the tab that fixes it, or to the page that does.
const openCheck = (item) => {
    if (item.tab === 'profile') return router.visit(route('profile.edit'));
    if (item.tab === 'settings') return router.visit(route('settings'));
    tab.value = item.tab;
};

const scoreColour = computed(() => {
    if (props.score >= 80) return 'text-emerald-600';
    if (props.score >= 50) return 'text-amber-600';
    return 'text-rose-600';
});

const ringColour = computed(() => {
    if (props.score >= 80) return '#059669';
    if (props.score >= 50) return '#d97706';
    return '#e11d48';
});

const failing = computed(() => props.checks.filter((c) => !c.ok));

const levelStyle = {
    critical: 'bg-rose-50 text-rose-700',
    warning: 'bg-amber-50 text-amber-700',
    info: 'bg-slate-100 text-slate-600',
};

const refresh = () =>
    router.visit(route('security.index') + `?fresh=1&tab=${tab.value}`, {
        preserveScroll: true,
        preserveState: true,
    });

const tiles = computed(() => [
    {
        label: 'Failed SSH logins, 24 h',
        value: props.ssh.summary_24h.failed,
        detail: `from ${props.ssh.summary_24h.unique_ips} addresses`,
        tab: 'ssh',
    },
    {
        label: 'Banned from SSH',
        value: props.ssh.status.currently_banned,
        detail: props.ssh.status.running ? 'fail2ban is running' : 'fail2ban is not running',
        tab: 'ssh',
    },
    {
        label: 'Failed panel logins, 24 h',
        value: props.logins.failed_24h,
        detail: `${props.logins.locked.length} address(es) locked out`,
        tab: 'logins',
    },
    {
        label: 'Security updates waiting',
        value: props.updates.security,
        detail: props.updates.automatic ? 'installed automatically' : 'not automatic',
        tab: 'updates',
    },
]);
</script>

<template>
    <Head title="Security" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Security</h2>
                    <p class="text-sm text-slate-500">
                        You are connected from
                        <span class="font-mono">{{ myIp }}</span> · checked
                        {{ ago(checkedAt) }}
                    </p>
                </div>
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 shadow-sm ring-1 ring-slate-900/10 transition hover:bg-slate-50"
                    @click="refresh"
                >
                    Check again
                </button>
            </div>
        </template>

        <div v-if="activeTask ?? latestTask" class="mb-6">
            <TaskStatus :task="activeTask ?? latestTask" />
        </div>

        <div class="mb-6 overflow-x-auto border-b border-slate-200">
            <nav class="-mb-px flex gap-6 whitespace-nowrap" aria-label="Sections">
                <button
                    v-for="t in tabs"
                    :key="t.key"
                    type="button"
                    class="border-b-2 px-1 pb-3 text-sm font-medium transition"
                    :class="
                        tab === t.key
                            ? 'border-brand-600 text-brand-700'
                            : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                    "
                    @click="tab = t.key"
                >
                    {{ t.name }}
                    <span
                        v-if="t.key === 'ssh' && ssh.status.currently_banned"
                        class="ml-1 rounded-full bg-rose-50 px-1.5 text-xs text-rose-700"
                        >{{ ssh.status.currently_banned }}</span
                    >
                    <span
                        v-if="t.key === 'users'"
                        class="ml-1 text-xs text-slate-400"
                        >{{ users.length }}</span
                    >
                </button>
            </nav>
        </div>

        <!-- Overview -->
        <div v-if="tab === 'overview'" class="space-y-6">
            <div class="grid gap-6 lg:grid-cols-3">
                <div :class="card" class="flex items-center gap-6">
                    <svg viewBox="0 0 36 36" class="h-28 w-28 shrink-0 -rotate-90">
                        <circle cx="18" cy="18" r="15.9" fill="none" stroke="#e2e8f0" stroke-width="3" />
                        <circle
                            cx="18"
                            cy="18"
                            r="15.9"
                            fill="none"
                            :stroke="ringColour"
                            stroke-width="3"
                            stroke-linecap="round"
                            :stroke-dasharray="`${score} 100`"
                        />
                    </svg>
                    <div>
                        <p class="text-4xl font-semibold" :class="scoreColour">{{ score }}</p>
                        <p class="text-sm font-medium text-slate-900">Security score</p>
                        <p class="mt-1 text-xs text-slate-500">
                            {{ failing.length === 0 ? 'Every check passes.' : `${failing.length} check(s) need attention.` }}
                        </p>
                    </div>
                </div>

                <div class="grid grid-cols-2 gap-4 lg:col-span-2">
                    <button
                        v-for="tile in tiles"
                        :key="tile.label"
                        type="button"
                        :class="card"
                        class="!p-5 text-left transition hover:shadow-md"
                        @click="tab = tile.tab"
                    >
                        <p class="text-xs font-medium uppercase tracking-wide text-slate-400">
                            {{ tile.label }}
                        </p>
                        <p class="mt-2 text-2xl font-semibold text-slate-900">{{ tile.value }}</p>
                        <p class="text-xs text-slate-500">{{ tile.detail }}</p>
                    </button>
                </div>
            </div>

            <div :class="card" class="!p-0">
                <div class="border-b border-slate-100 px-6 py-4">
                    <h3 class="font-semibold text-slate-900">Checklist</h3>
                    <p class="text-sm text-slate-500">
                        What protects this server, and what is still open.
                    </p>
                </div>
                <ul class="divide-y divide-slate-100">
                    <li
                        v-for="item in checks"
                        :key="item.key"
                        class="flex flex-wrap items-center gap-4 px-6 py-4"
                    >
                        <span
                            class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-sm font-bold"
                            :class="item.ok ? 'bg-emerald-50 text-emerald-600' : levelStyle[item.level]"
                        >
                            {{ item.ok ? '✓' : '!' }}
                        </span>
                        <div class="min-w-0 flex-1">
                            <p class="text-sm font-medium text-slate-900">{{ item.label }}</p>
                            <p class="text-sm text-slate-500">{{ item.detail }}</p>
                        </div>
                        <button
                            v-if="!item.ok"
                            type="button"
                            class="rounded-lg bg-brand-600 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-brand-700"
                            @click="openCheck(item)"
                        >
                            Fix
                        </button>
                    </li>
                </ul>
            </div>
        </div>

        <SshProtection v-else-if="tab === 'ssh'" :ssh="ssh" :my-ip="myIp" />
        <SshUsers v-else-if="tab === 'users'" :users="users" :hardening="hardening" :service="sshService" />
        <LoginActivity v-else-if="tab === 'logins'" :logins="logins" />
        <Firewall
            v-else-if="tab === 'firewall'"
            :firewall="firewall"
            :ssh-ports="hardening.ports"
            :panel-port="panelPort"
            :my-ip="myIp"
        />
        <Hardening v-else-if="tab === 'hardening'" :hardening="hardening" :service="sshService" />
        <Updates v-else-if="tab === 'updates'" :updates="updates" :busy="Boolean(activeTask)" />
        <Sessions v-else-if="tab === 'sessions'" :sessions="sessions" />
        <Alerts v-else-if="tab === 'alerts'" :alerts="alerts" :smtp-ready="smtpReady" />
    </AuthenticatedLayout>
</template>
