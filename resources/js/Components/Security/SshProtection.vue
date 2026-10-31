<script setup>
import { computed, ref, watch } from 'vue';
import InputError from '@/Components/InputError.vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { usePagination } from '@/Composables/usePagination';
import { confirmAction } from '@/dialog';
import { ago, card, field, smallButton, when } from './ui';
import { router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    ssh: Object,
    bannedIps: { type: Array, default: () => [] },
    myIp: String,
});

const emit = defineEmits(['open']);

const jail = props.ssh.jail;

// The window and the ban are picked from a short list rather than typed in
// seconds, which nobody thinks in. "Custom" stays out: the server accepts any
// value in range, but the common ones are what people actually want.
const windows = [
    { value: 600, label: '10 minutes' },
    { value: 3600, label: '1 hour' },
    { value: 86400, label: '1 day' },
    { value: 604800, label: '1 week' },
];

const bans = [
    { value: -1, label: 'Permanently' },
    { value: 3600, label: '1 hour' },
    { value: 86400, label: '1 day' },
    { value: 604800, label: '1 week' },
    { value: 2592000, label: '30 days' },
];

const form = useForm({
    max_retry: jail.max_retry,
    find_time: windows.some((w) => w.value === jail.find_time) ? jail.find_time : 86400,
    ban_time: bans.some((b) => b.value === jail.ban_time) ? jail.ban_time : -1,
    ignore_ips: (jail.ignore_ips ?? []).join('\n'),
});

const save = () =>
    form
        .transform((data) => ({ ...data, permanent: data.ban_time === -1 }))
        .post(route('security.ssh-protection'), { preserveScroll: true });

const protectedNow = computed(() => props.ssh.status.running && props.ssh.status.managed);

const install = () => router.post(route('security.ssh-protection.install'), {}, { preserveScroll: true });

const trustMe = () => router.post(route('security.trust'), { ip: props.myIp }, { preserveScroll: true });

const ban1 = async (ip) => {
    if (!(await confirmAction('Ban this address?', `${ip} will be refused on the SSH port ${jail.ban_time === -1 ? 'permanently' : 'for the ban time'}.`, 'Ban')))
        return;
    router.post(route('security.bans.store'), { ip }, { preserveScroll: true });
};

// Banned by anything — this jail, the trap or a firewall block — is refused on
// the SSH port all the same.
const banned = computed(() => new Set(props.bannedIps));

const filter = ref('all');
const search = ref('');

const events = computed(() =>
    props.ssh.events.filter(
        (e) =>
            (filter.value === 'all' ||
                (filter.value === 'accepted' ? e.kind === 'accepted' : e.kind !== 'accepted')) &&
            (!search.value || e.ip.includes(search.value) || e.user.includes(search.value)),
    ),
);

const { page, pages, total, perPage, visible: visibleEvents } = usePagination(events, 25);

watch([filter, search], () => (page.value = 1));

const kindStyle = {
    accepted: 'bg-emerald-50 text-emerald-700',
    failed: 'bg-rose-50 text-rose-700',
    invalid: 'bg-amber-50 text-amber-700',
};

const kindLabel = { accepted: 'Logged in', failed: 'Wrong password', invalid: 'Unknown user' };
</script>

<template>
    <div class="space-y-6">
        <div
            v-if="!ssh.status.installed"
            class="rounded-2xl bg-rose-50 p-6 text-sm text-rose-800 ring-1 ring-rose-200"
        >
            <p class="font-semibold">SSH is not protected.</p>
            <p class="mt-1">
                fail2ban is not installed, so anybody can keep guessing passwords for
                as long as they like.
                <template v-if="ssh.installing">It is being installed now.</template>
            </p>
            <button
                v-if="!ssh.installing"
                type="button"
                class="mt-4 rounded-xl bg-rose-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-rose-700"
                @click="install"
            >
                Install and protect SSH
            </button>
        </div>

        <div
            v-else-if="!protectedNow"
            class="rounded-2xl bg-amber-50 p-6 text-sm text-amber-800 ring-1 ring-amber-200"
        >
            <p class="font-semibold">fail2ban is installed but the panel's SSH jail is not active.</p>
            <button
                type="button"
                class="mt-3 rounded-xl bg-amber-600 px-4 py-2 text-sm font-medium text-white hover:bg-amber-700"
                @click="install"
            >
                Turn SSH protection on
            </button>
        </div>

        <div class="grid gap-6 lg:grid-cols-2">
            <div :class="card">
                <h3 class="font-semibold text-slate-900">Brute-force protection</h3>
                <p class="mt-1 text-sm text-slate-500">
                    An address that fails to log in too often is banned from the SSH
                    port. Your sites and this panel stay reachable from it, which is
                    how you unban yourself.
                </p>

                <form class="mt-4 space-y-4" @submit.prevent="save">
                    <div class="grid gap-4 sm:grid-cols-3">
                        <div>
                            <label class="block text-sm font-medium text-slate-700">Failed attempts</label>
                            <input v-model.number="form.max_retry" type="number" min="1" max="100" :class="field" />
                            <InputError class="mt-2" :message="form.errors.max_retry" />
                        </div>
                        <div>
                            <label class="block text-sm font-medium text-slate-700">Within</label>
                            <select v-model.number="form.find_time" :class="field">
                                <option v-for="w in windows" :key="w.value" :value="w.value">{{ w.label }}</option>
                            </select>
                            <InputError class="mt-2" :message="form.errors.find_time" />
                        </div>
                        <div>
                            <label class="block text-sm font-medium text-slate-700">Ban</label>
                            <select v-model.number="form.ban_time" :class="field">
                                <option v-for="b in bans" :key="b.value" :value="b.value">{{ b.label }}</option>
                            </select>
                            <InputError class="mt-2" :message="form.errors.ban_time" />
                        </div>
                    </div>

                    <div>
                        <label class="block text-sm font-medium text-slate-700">Never ban these addresses</label>
                        <textarea
                            v-model="form.ignore_ips"
                            rows="3"
                            :class="field"
                            class="font-mono text-sm"
                            placeholder="e.g. 203.0.113.10 or 198.51.100.0/24"
                        />
                        <InputError class="mt-2" :message="form.errors.ignore_ips" />
                        <p class="mt-1 text-xs text-slate-500">
                            One address or range per line. With permanent bans, three typos
                            from your own connection lock you out of SSH for good — trust
                            your office or home address here.
                            <button type="button" class="font-medium text-brand-600 hover:underline" @click="trustMe">
                                Trust my address ({{ myIp }})
                            </button>
                        </p>
                    </div>

                    <PrimaryButton :disabled="form.processing">Save protection</PrimaryButton>
                </form>
            </div>

            <div :class="card" class="flex flex-col">
                <h3 class="font-semibold text-slate-900">Banned addresses</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Every ban — from this protection, the SSH trap, firewall blocks and panel lockouts — is
                    listed, counted and managed together on the Banned IPs tab.
                </p>
                <div class="mt-auto pt-6">
                    <PrimaryButton type="button" @click="emit('open', 'bans')">Open Banned IPs</PrimaryButton>
                </div>
            </div>
        </div>

        <div class="grid gap-6 lg:grid-cols-3">
            <div :class="card">
                <h3 class="font-semibold text-slate-900">Last 7 days</h3>
                <dl class="mt-4 grid grid-cols-3 gap-4 text-center">
                    <div>
                        <dt class="text-xs text-slate-500">Logins</dt>
                        <dd class="text-xl font-semibold text-emerald-600">{{ ssh.summary_7d.accepted }}</dd>
                    </div>
                    <div>
                        <dt class="text-xs text-slate-500">Failures</dt>
                        <dd class="text-xl font-semibold text-rose-600">{{ ssh.summary_7d.failed }}</dd>
                    </div>
                    <div>
                        <dt class="text-xs text-slate-500">Addresses</dt>
                        <dd class="text-xl font-semibold text-slate-900">{{ ssh.summary_7d.unique_ips }}</dd>
                    </div>
                </dl>
            </div>

            <div :class="card" class="lg:col-span-2">
                <h3 class="font-semibold text-slate-900">Who is trying hardest</h3>
                <table class="mt-3 w-full text-sm">
                    <thead class="text-left text-xs uppercase text-slate-400">
                        <tr>
                            <th class="py-1">Address</th>
                            <th class="py-1">Attempts</th>
                            <th class="hidden py-1 sm:table-cell">Users tried</th>
                            <th class="py-1">Last</th>
                            <th />
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="attacker in ssh.summary_7d.top_attackers" :key="attacker.ip">
                            <td class="py-2"><IpAddress :ip="attacker.ip" /></td>
                            <td class="py-2">{{ attacker.attempts }}</td>
                            <td class="hidden py-2 text-slate-500 sm:table-cell">{{ (attacker.users ?? []).join(', ') }}</td>
                            <td class="py-2 text-slate-500">{{ ago(attacker.last_seen) }}</td>
                            <td class="py-2 text-right">
                                <span v-if="banned.has(attacker.ip)" class="text-xs font-medium text-rose-600">Banned</span>
                                <button v-else type="button" :class="smallButton" class="text-rose-600" @click="ban1(attacker.ip)">
                                    Ban
                                </button>
                            </td>
                        </tr>
                        <tr v-if="!ssh.summary_7d.top_attackers.length">
                            <td colspan="5" class="py-6 text-center text-slate-400">No failed logins in the log.</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>

        <div :class="card" class="!p-0">
            <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-6 py-4">
                <div>
                    <h3 class="font-semibold text-slate-900">SSH login attempts</h3>
                    <p class="text-sm text-slate-500">From sshd's own log, newest first.</p>
                </div>
                <div class="flex gap-2">
                    <input v-model="search" type="text" placeholder="Filter by IP or user" class="rounded-lg border-slate-300 text-sm shadow-sm" />
                    <select v-model="filter" class="rounded-lg border-slate-300 text-sm shadow-sm">
                        <option value="all">All</option>
                        <option value="failed">Failures</option>
                        <option value="accepted">Logins</option>
                    </select>
                </div>
            </div>
            <div>
                <table class="w-full text-sm">
                    <thead class="text-left text-xs uppercase text-slate-400">
                        <tr>
                            <th class="px-6 py-2">Time</th>
                            <th class="px-2 py-2">Result</th>
                            <th class="px-2 py-2">User</th>
                            <th class="px-2 py-2">Address</th>
                            <th class="hidden px-2 py-2 sm:table-cell">Method</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="(event, index) in visibleEvents" :key="`${page}-${index}`">
                            <td class="whitespace-nowrap px-6 py-2 text-slate-500">{{ when(event.time) }}</td>
                            <td class="px-2 py-2">
                                <span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="kindStyle[event.kind]">
                                    {{ kindLabel[event.kind] }}
                                </span>
                            </td>
                            <td class="px-2 py-2 font-mono">{{ event.user || '—' }}</td>
                            <td class="px-2 py-2">
                                <IpAddress :ip="event.ip" />
                                <span v-if="banned.has(event.ip)" class="ml-1 text-xs text-rose-600">banned</span>
                            </td>
                            <td class="hidden px-2 py-2 text-slate-500 sm:table-cell">{{ event.method }}</td>
                        </tr>
                        <tr v-if="!events.length">
                            <td colspan="5" class="px-6 py-8 text-center text-slate-400">Nothing in the log matches.</td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>
    </div>
</template>
