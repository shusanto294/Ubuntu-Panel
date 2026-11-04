<script setup>
import { computed, ref, watch } from 'vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { confirmAction, confirmDanger } from '@/dialog';
import { ago, card, field, smallButton, when } from './ui';
import { Link, router, useForm } from '@inertiajs/vue3';

/*
 * Every address this server refuses, whatever refuses it. The four sources
 * stay where they are enforced — two fail2ban jails, ufw and the panel's own
 * lockout — and are read into one list here, one row per address.
 */
const props = defineProps({
    bans: Object,
    myIp: String,
});

const emit = defineEmits(['open']);

const sources = {
    ssh: { label: 'SSH brute-force', scope: 'SSH port', style: 'bg-amber-50 text-amber-700', tab: 'ssh' },
    web_login: { label: 'Website login attack', scope: 'Web ports', style: 'bg-orange-50 text-orange-700', tab: 'protection' },
    web_scan: { label: 'Website scanner', scope: 'Web ports', style: 'bg-orange-50 text-orange-700', tab: 'protection' },
    mail: { label: 'Mail login attack', scope: 'Mail ports', style: 'bg-violet-50 text-violet-700', tab: 'protection' },
    recidive: { label: 'Repeat offender', scope: 'Whole server', style: 'bg-rose-50 text-rose-700', tab: 'protection' },
    firewall: { label: 'Firewall block', scope: 'Whole server', style: 'bg-rose-50 text-rose-700', tab: 'firewall' },
    panel: { label: 'Panel lockout', scope: 'Panel sign-in', style: 'bg-slate-100 text-slate-700', tab: 'logins' },
};

const PROTECT_KINDS = ['web_login', 'web_scan', 'mail', 'recidive'];

const counts = computed(() => props.bans.counts);

const tiles = computed(() => [
    { key: 'all', label: 'Banned now', value: counts.value.total, detail: 'unique addresses, all sources' },
    {
        key: 'ssh',
        label: 'SSH brute-force',
        value: counts.value.ssh,
        detail: `${counts.value.ssh_total} since fail2ban started · SSH port only`,
    },
    {
        key: 'protect',
        label: 'Website & mail',
        value: counts.value.protect ?? 0,
        detail: 'Protection tab jails · web, mail or every port',
    },
    { key: 'firewall', label: 'Firewall blocks', value: counts.value.firewall, detail: 'ufw deny rules · every port' },
    { key: 'panel', label: 'Panel lockouts', value: counts.value.panel, detail: 'panel sign-in only, temporary' },
]);

const source = ref('all');
const search = ref('');

const rows = computed(() =>
    props.bans.list.filter(
        (row) =>
            (source.value === 'all' ||
                row.sources.some((s) =>
                    source.value === 'protect' ? PROTECT_KINDS.includes(s.kind) : s.kind === source.value,
                )) &&
            (!search.value || row.ip.includes(search.value.trim())),
    ),
);

const { page, pages, total, perPage, visible } = usePagination(rows, 20);

watch([source, search], () => (page.value = 1));

const banForm = useForm({ ip: '', scope: 'ssh', comment: '' });

const ban = async () => {
    if (
        banForm.scope === 'server' &&
        !(await confirmAction(
            'Ban from the whole server?',
            `${banForm.ip} will be refused on every port — SSH, the sites and this panel — by a firewall rule.` +
                (props.bans.firewall_active ? '' : ' The firewall is off, so the rule only takes effect once it is turned on.'),
            'Ban',
        ))
    )
        return;

    banForm.post(route('security.bans.store'), { preserveScroll: true, onSuccess: () => banForm.reset() });
};

// The most recent ban of an address, of all the things refusing it.
const bannedAt = (row) => Math.max(0, ...row.sources.map((s) => s.at ?? 0)) || null;

const release = async (row) => {
    const names = row.sources.map((s) => sources[s.kind].label.toLowerCase()).join(', ');

    if (!(await confirmDanger('Unban this address?', `${row.ip} is let go from: ${names}.`, 'Unban')))
        return;

    router.post(route('security.bans.release'), { ip: row.ip }, { preserveScroll: true });
};
</script>

<template>
    <div class="space-y-6">
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-5">
            <button
                v-for="tile in tiles"
                :key="tile.key"
                type="button"
                :class="[card, source === tile.key ? 'ring-2 !ring-brand-500' : '']"
                class="min-w-0 break-words !p-4 text-left transition hover:shadow-md sm:!p-5"
                @click="source = tile.key"
            >
                <p class="text-xs font-medium uppercase tracking-wide text-slate-400">{{ tile.label }}</p>
                <p class="mt-2 text-2xl font-semibold text-slate-900">{{ tile.value }}</p>
                <p class="text-xs text-slate-500">{{ tile.detail }}</p>
            </button>
        </div>

        <div :class="card">
            <h3 class="font-semibold text-slate-900">Ban an address</h3>
            <form class="mt-3 flex flex-wrap gap-2" @submit.prevent="ban">
                <input
                    v-model="banForm.ip"
                    type="text"
                    placeholder="IP address"
                    :class="field"
                    class="!mt-0 min-w-0 flex-1 font-mono text-sm sm:max-w-xs"
                />
                <select v-model="banForm.scope" :class="field" class="!mt-0 w-auto text-sm">
                    <option value="ssh">From SSH only</option>
                    <option value="server">From the whole server</option>
                </select>
                <input
                    v-if="banForm.scope === 'server'"
                    v-model="banForm.comment"
                    type="text"
                    placeholder="Note (optional)"
                    :class="field"
                    class="!mt-0 min-w-0 flex-1 text-sm sm:max-w-xs"
                />
                <button
                    type="submit"
                    class="shrink-0 rounded-lg bg-rose-600 px-4 py-2 text-sm font-medium text-white hover:bg-rose-700 disabled:opacity-50"
                    :disabled="banForm.processing || !banForm.ip"
                >
                    Ban
                </button>
            </form>
            <p class="mt-2 text-xs text-slate-500">
                SSH only goes to fail2ban's SSH jail, so the address can still load the sites and reach this
                panel. The whole server adds a firewall rule that refuses it on every port.
            </p>
        </div>

        <div :class="card" class="!p-0">
            <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 px-5 py-4 sm:px-6">
                <div class="min-w-0">
                    <h3 class="font-semibold text-slate-900">Banned addresses</h3>
                    <p class="text-sm text-slate-500">
                        One row per address. Unban lets it go from everything that refuses it.
                    </p>
                </div>
                <div class="flex w-full flex-wrap gap-2 sm:w-auto">
                    <input
                        v-model="search"
                        type="text"
                        placeholder="Filter by IP"
                        class="min-w-0 flex-1 rounded-lg border-slate-300 text-sm shadow-sm sm:flex-none"
                    />
                    <select v-model="source" class="min-w-0 rounded-lg border-slate-300 text-sm shadow-sm">
                        <option value="all">All sources</option>
                        <option v-for="(s, key) in sources" :key="key" :value="key">{{ s.label }}</option>
                    </select>
                </div>
            </div>

            <div class="overflow-x-auto">
                <table class="w-full text-sm">
                    <thead class="text-left text-xs uppercase text-slate-400">
                        <tr>
                            <th class="px-4 py-2 sm:px-6">Address</th>
                            <th class="px-2 py-2">Banned by</th>
                            <th class="hidden px-2 py-2 md:table-cell">Details</th>
                            <th class="px-4 py-2 sm:px-6" />
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="row in visible" :key="row.ip">
                            <td class="whitespace-nowrap px-4 py-2 sm:px-6">
                                <Link :href="route('logs.ip', row.ip)" class="hover:underline" title="What this address did">
                                    <IpAddress :ip="row.ip" class="text-slate-700" />
                                </Link>
                                <span v-if="row.ip === myIp" class="ml-1 text-xs font-medium text-amber-700">you</span>
                                <span v-if="bannedAt(row)" class="block text-xs text-slate-400" :title="when(bannedAt(row))">
                                    banned {{ ago(bannedAt(row)) }}
                                </span>
                            </td>
                            <td class="px-2 py-2">
                                <span class="flex flex-wrap gap-1">
                                    <button
                                        v-for="s in row.sources"
                                        :key="s.kind + (s.rule ?? '')"
                                        type="button"
                                        class="rounded-full px-2 py-0.5 text-xs font-medium"
                                        :class="sources[s.kind].style"
                                        :title="`${sources[s.kind].scope} — open its tab`"
                                        @click="emit('open', sources[s.kind].tab)"
                                    >
                                        {{ sources[s.kind].label }}
                                    </button>
                                </span>
                            </td>
                            <td class="hidden px-2 py-2 text-xs text-slate-500 md:table-cell">
                                <template v-for="s in row.sources" :key="'d' + s.kind + (s.rule ?? '')">
                                    <span v-if="s.at || s.until" class="block">
                                        <span class="font-medium text-slate-700">{{ sources[s.kind].label }}:</span>
                                        <template v-if="s.at"> banned {{ ago(s.at) }} ({{ when(s.at) }})</template>
                                        <template v-if="s.until"> · until {{ when(s.until) }}</template>
                                        <template v-else-if="s.at && s.kind !== 'panel'"> · permanently</template>
                                    </span>
                                    <span v-if="s.kind === 'firewall'" class="block">
                                        rule #{{ s.rule }}<template v-if="s.comment"> · {{ s.comment }}</template>
                                    </span>
                                </template>
                                <span>{{
                                    row.sources.some((s) => s.kind === 'firewall' || s.kind === 'recidive')
                                        ? 'Refused on every port'
                                        : row.sources.some((s) => s.kind === 'web_login' || s.kind === 'web_scan')
                                          ? 'Refused on the web ports'
                                          : row.sources.some((s) => s.kind === 'mail')
                                            ? 'Refused on the mail ports'
                                        : row.sources.some((s) => s.kind === 'ssh')
                                          ? 'Refused on the SSH port'
                                          : 'Refused at panel sign-in'
                                }}</span>
                            </td>
                            <td class="px-4 py-2 text-right sm:px-6">
                                <button type="button" :class="smallButton" class="text-slate-700" @click="release(row)">
                                    Unban
                                </button>
                            </td>
                        </tr>
                        <tr v-if="!rows.length">
                            <td colspan="4" class="px-6 py-8 text-center text-slate-400">
                                {{ bans.list.length ? 'No banned address matches.' : 'Nobody is banned right now.' }}
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>
    </div>
</template>
