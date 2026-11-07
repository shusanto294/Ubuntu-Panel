<script setup>
import { computed, ref } from 'vue';
import InputError from '@/Components/InputError.vue';
import IpAddress from '@/Components/IpAddress.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { confirmAction, confirmDanger } from '@/dialog';
import { card, field, smallButton } from './ui';
import { router, useForm } from '@inertiajs/vue3';

/*
 * Ports & firewall: every port something listens on or the firewall opens,
 * what it is for, and a switch to open it to the internet or close it. The
 * firewall (ufw) refuses everything not opened, so a closed port is one
 * nobody outside can reach, whatever is listening on it.
 */
const props = defineProps({
    firewall: Object,
    ports: { type: Array, default: () => [] },
    forwards: { type: Array, default: () => [] },
    sshPorts: Array,
    panelPort: Number,
    myIp: String,
});

const active = computed(() => props.firewall.active);

// Reachable from outside: something listens on a public address, or the
// firewall opens it. Everything else answers this machine only.
const outside = computed(() => props.ports.filter((p) => p.public || p.open || p.allowed_from.length));
const local = computed(() => props.ports.filter((p) => !(p.public || p.open || p.allowed_from.length)));

const showLocal = ref(false);
const showRules = ref(false);

const enable = async () => {
    const ports = (props.sshPorts?.length ? props.sshPorts : ['22']).join(', ');
    if (
        !(await confirmAction(
            'Switch the firewall on?',
            `SSH (${ports}), the websites (80, 443) and the panel (${props.panelPort}) are opened first. Every other incoming port is closed. You can open more afterwards.`,
            'Switch on',
        ))
    )
        return;
    router.post(route('security.firewall.enable'), {}, { preserveScroll: true });
};

const disable = async () => {
    if (!(await confirmDanger('Switch the firewall off?', 'Every port something listens on will be open to the internet — databases and caches included. The rules are kept.', 'Switch off')))
        return;
    router.post(route('security.firewall.disable'), {}, { preserveScroll: true });
};

// ---------------------------------------------------------------- the switch

const busy = ref('');
const keyOf = (p) => `${p.port}/${p.protocol}`;

const toggle = async (p) => {
    const open = !p.open;
    const label = `${p.name} (${keyOf(p)})`;

    if (open) {
        const text =
            p.risk === 'risky'
                ? `${p.description} Anyone on the internet will be able to connect to it. To let in only one address, use “Open a port” below with an address in “Only from”.`
                : `Anyone on the internet will be able to connect to port ${keyOf(p)}.`;
        const ok = p.risk === 'risky' ? await confirmDanger(`Open ${label} to everyone?`, text, 'Open anyway') : await confirmAction(`Open ${label}?`, text, 'Open');
        if (!ok) return;
    } else {
        const web = p.protocol === 'tcp' && (p.port === 80 || p.port === 443);
        const text = web
            ? 'Your websites will stop answering on this port' + (p.port === 80 ? ', and Let’s Encrypt will not be able to renew certificates.' : '.')
            : p.listening
              ? `${p.programs.join(', ') || 'The program'} keeps running, but nobody outside this server can reach it.`
              : 'The rule that opens it is removed.';
        if (!(await confirmDanger(`Close ${label}?`, text, 'Close'))) return;
    }

    busy.value = keyOf(p);
    router.post(
        route('security.firewall.ports'),
        { port: p.port, protocol: p.protocol, open },
        { preserveScroll: true, onFinish: () => (busy.value = '') },
    );
};

const state = (p) => {
    if (!active.value) return p.public ? { text: 'Reachable — firewall off', style: 'bg-rose-50 text-rose-700' } : null;
    if (p.open) return { text: 'Open', style: p.risk === 'risky' ? 'bg-rose-50 text-rose-700' : 'bg-emerald-50 text-emerald-700' };
    if (p.allowed_from.length) return { text: 'Some addresses only', style: 'bg-sky-50 text-sky-700' };
    return { text: 'Closed', style: 'bg-slate-100 text-slate-600' };
};

// ----------------------------------------------------------- open a port

const portForm = useForm({ action: 'allow', port: '', protocol: 'tcp', from: '', comment: '' });
const openPort = () =>
    portForm.post(route('security.firewall.rules.store'), {
        preserveScroll: true,
        onSuccess: () => portForm.reset(),
    });

// ---------------------------------------------------------- port forwards

const forwardForm = useForm({ protocol: 'tcp', port: '', to_ip: '', to_port: '', comment: '' });
const addForward = () =>
    forwardForm.post(route('security.firewall.forwards'), {
        preserveScroll: true,
        onSuccess: () => forwardForm.reset(),
    });
const removeForward = async (f) => {
    if (!(await confirmDanger(`Stop forwarding port ${f.port}?`, 'Anything connecting to it will be refused from now on.', 'Remove')))
        return;
    router.post(route('security.firewall.forwards'), { remove: `${f.protocol}/${f.port}` }, { preserveScroll: true });
};
const targetOf = (f) => (!f.to_ip || f.to_ip === '127.0.0.1' ? `port ${f.to_port} on this server` : `${f.to_ip}:${f.to_port}`);

// ------------------------------------------------------------- all rules

const removeRule = async (rule) => {
    if (!(await confirmDanger(`Remove rule ${rule.number}?`, `${rule.action} ${rule.to} from ${rule.from}`, 'Remove')))
        return;
    router.delete(route('security.firewall.rules.destroy', rule.number), { preserveScroll: true });
};

const actionStyle = {
    ALLOW: 'bg-emerald-50 text-emerald-700',
    LIMIT: 'bg-sky-50 text-sky-700',
    DENY: 'bg-rose-50 text-rose-700',
    REJECT: 'bg-rose-50 text-rose-700',
};
</script>

<template>
    <div class="space-y-6">
        <!-- The firewall itself. -->
        <div :class="card" class="flex flex-wrap items-center justify-between gap-4">
            <div class="min-w-0">
                <h3 class="flex items-center gap-2 font-semibold text-slate-900">
                    Firewall
                    <span
                        class="rounded-full px-2 py-0.5 text-xs font-medium"
                        :class="active ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'"
                    >
                        {{ !firewall.installed ? 'not installed' : active ? 'on' : 'off' }}
                    </span>
                </h3>
                <p class="mt-1 text-sm text-slate-500">
                    <template v-if="active">
                        Every incoming port is closed unless it is opened below. Closing a port costs nothing — the
                        kernel refuses the connection before any program sees it.
                    </template>
                    <template v-else>
                        Every port a program listens on is reachable from the internet. Switch the firewall on to close
                        everything you have not chosen to open.
                    </template>
                </p>
            </div>
            <button
                v-if="firewall.installed && !active"
                type="button"
                class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-brand-700"
                @click="enable"
            >
                Switch on
            </button>
            <button
                v-else-if="active"
                type="button"
                class="rounded-xl px-4 py-2.5 text-sm font-medium text-rose-600 ring-1 ring-rose-200 hover:bg-rose-50"
                @click="disable"
            >
                Switch off
            </button>
        </div>

        <!-- The ports. -->
        <div :class="card" class="!p-0">
            <div class="border-b border-slate-100 px-4 py-4 sm:px-6">
                <h3 class="font-semibold text-slate-900">Ports the internet can reach</h3>
                <p class="text-sm text-slate-500">
                    What each one is for, and whether the firewall lets it in. Close anything you do not use.
                </p>
            </div>

            <ul class="divide-y divide-slate-100">
                <li v-for="p in outside" :key="keyOf(p)" class="flex gap-3 px-4 py-4 sm:gap-4 sm:px-6">
                    <span class="w-20 shrink-0 pt-0.5 font-mono text-sm font-semibold text-slate-900 sm:w-24">
                        {{ p.port }}<span class="font-normal text-slate-400">/{{ p.protocol }}</span>
                    </span>
                    <div class="min-w-0 flex-1">
                        <p class="flex flex-wrap items-center gap-2 text-sm font-medium text-slate-900">
                            {{ p.name }}
                            <span v-if="p.risk === 'risky'" class="rounded-full bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700">
                                keep closed
                            </span>
                            <span v-else-if="p.risk === 'essential'" class="rounded-full bg-sky-50 px-2 py-0.5 text-xs font-medium text-sky-700">
                                needed
                            </span>
                            <span v-if="state(p)" class="rounded-full px-2 py-0.5 text-xs font-medium" :class="state(p).style">
                                {{ state(p).text }}
                            </span>
                        </p>
                        <p class="mt-0.5 text-sm text-slate-500">{{ p.description }}</p>
                        <p class="mt-1 text-xs text-slate-400">
                            <template v-if="p.listening">
                                {{ p.programs.length ? `Used by ${p.programs.join(', ')}` : 'Something is listening' }}{{ p.public ? '' : ' — on this server only' }}
                            </template>
                            <template v-else>Nothing is listening on it right now.</template>
                            <template v-if="p.allowed_from.length">
                                · Allowed from
                                <span v-for="(from, i) in p.allowed_from" :key="from"><template v-if="i">, </template><IpAddress :ip="from" /></span>
                            </template>
                        </p>
                        <p v-if="p.locked" class="mt-1 text-xs text-sky-700">{{ p.locked }}</p>
                        <p v-else-if="p.in_range" class="mt-1 text-xs text-slate-500">
                            Opened by rule {{ p.in_range }}, a port range — remove that rule under “All firewall rules” to close it.
                        </p>
                    </div>
                    <div class="shrink-0 pt-0.5">
                        <button
                            type="button"
                            role="switch"
                            :aria-checked="p.open"
                            :aria-label="`${p.open ? 'Close' : 'Open'} port ${keyOf(p)}`"
                            :title="!active ? 'Switch the firewall on first' : p.locked || (p.in_range ? 'Part of a port range' : '')"
                            class="relative inline-flex h-6 w-11 rounded-full transition disabled:cursor-not-allowed disabled:opacity-40"
                            :class="p.open ? 'bg-emerald-600' : 'bg-slate-300'"
                            :disabled="!active || busy !== '' || (p.open && (Boolean(p.locked) || Boolean(p.in_range)))"
                            @click="toggle(p)"
                        >
                            <span class="absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition" :class="p.open ? 'left-[22px]' : 'left-0.5'" />
                        </button>
                    </div>
                </li>
                <li v-if="!outside.length" class="px-6 py-8 text-center text-sm text-slate-400">Nothing is reachable from outside.</li>
            </ul>

            <div v-if="local.length" class="border-t border-slate-100 px-4 py-3 sm:px-6">
                <button type="button" class="text-sm font-medium text-brand-700 hover:underline" @click="showLocal = !showLocal">
                    {{ showLocal ? 'Hide' : 'Show' }} {{ local.length }} {{ local.length === 1 ? 'port' : 'ports' }} that only this server can reach
                </button>
                <ul v-if="showLocal" class="mt-3 divide-y divide-slate-100 rounded-lg ring-1 ring-slate-200">
                    <li v-for="p in local" :key="keyOf(p)" class="flex gap-3 px-3 py-2.5 text-sm">
                        <span class="w-20 shrink-0 font-mono text-slate-700 sm:w-24">{{ p.port }}<span class="text-slate-400">/{{ p.protocol }}</span></span>
                        <span class="min-w-0">
                            <span class="font-medium text-slate-900">{{ p.name }}</span>
                            <span class="block text-xs text-slate-500">{{ p.description }} Safe: it only answers this machine.</span>
                        </span>
                    </li>
                </ul>
            </div>
        </div>

        <div class="grid gap-6 lg:grid-cols-2">
            <!-- Open another port. -->
            <div :class="card">
                <h3 class="font-semibold text-slate-900">Open a port</h3>
                <p class="mt-1 text-xs text-slate-500">
                    For an app or service that is not in the list yet. Give an address in “Only from” to let in just
                    that address — the safe way to reach a database from your office.
                </p>
                <form class="mt-3 space-y-3" @submit.prevent="openPort">
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Port or range</label>
                            <input v-model="portForm.port" type="text" placeholder="8080 or 6000:6100" :class="field" class="font-mono" />
                            <InputError class="mt-1" :message="portForm.errors.port" />
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Protocol</label>
                            <select v-model="portForm.protocol" :class="field">
                                <option value="tcp">TCP</option>
                                <option value="udp">UDP</option>
                                <option value="any">Both</option>
                            </select>
                            <InputError class="mt-1" :message="portForm.errors.protocol" />
                        </div>
                    </div>
                    <div>
                        <label class="block text-xs font-medium text-slate-700">Only from <span class="font-normal text-slate-400">(empty for everyone)</span></label>
                        <input v-model="portForm.from" type="text" :placeholder="myIp || '203.0.113.4'" :class="field" class="font-mono" />
                        <InputError class="mt-1" :message="portForm.errors.from" />
                    </div>
                    <div>
                        <label class="block text-xs font-medium text-slate-700">What it is for</label>
                        <input v-model="portForm.comment" type="text" placeholder="My app" :class="field" />
                        <InputError class="mt-1" :message="portForm.errors.comment" />
                    </div>
                    <PrimaryButton :disabled="portForm.processing || !portForm.port || !active">Open port</PrimaryButton>
                </form>
            </div>

            <!-- Port forwarding. -->
            <div :class="card">
                <h3 class="font-semibold text-slate-900">Port forwarding</h3>
                <p class="mt-1 text-xs text-slate-500">
                    Send connections on a port of this server somewhere else — another port here, or a machine on your
                    private network. The forwarded port is opened for you. IPv4 only.
                </p>
                <ul v-if="forwards.length" class="mt-3 divide-y divide-slate-100 rounded-lg ring-1 ring-slate-200">
                    <li v-for="f in forwards" :key="`${f.protocol}/${f.port}`" class="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                        <span class="min-w-0">
                            <span class="font-mono font-medium text-slate-900">{{ f.port }}/{{ f.protocol }}</span>
                            <span class="text-slate-500"> → {{ targetOf(f) }}</span>
                            <span v-if="f.comment" class="block truncate text-xs text-slate-400">{{ f.comment }}</span>
                        </span>
                        <button type="button" :class="smallButton" class="shrink-0 text-rose-600" @click="removeForward(f)">Remove</button>
                    </li>
                </ul>
                <form class="mt-3 space-y-3" @submit.prevent="addForward">
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Port on this server</label>
                            <input v-model="forwardForm.port" type="number" min="1" max="65535" placeholder="8080" :class="field" class="font-mono" />
                            <InputError class="mt-1" :message="forwardForm.errors.port" />
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Protocol</label>
                            <select v-model="forwardForm.protocol" :class="field">
                                <option value="tcp">TCP</option>
                                <option value="udp">UDP</option>
                            </select>
                        </div>
                    </div>
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs font-medium text-slate-700">To address <span class="font-normal text-slate-400">(empty: here)</span></label>
                            <input v-model="forwardForm.to_ip" type="text" placeholder="10.0.0.5" :class="field" class="font-mono" />
                            <InputError class="mt-1" :message="forwardForm.errors.to_ip" />
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">To port</label>
                            <input v-model="forwardForm.to_port" type="number" min="1" max="65535" placeholder="3000" :class="field" class="font-mono" />
                            <InputError class="mt-1" :message="forwardForm.errors.to_port" />
                        </div>
                    </div>
                    <input v-model="forwardForm.comment" type="text" placeholder="Note (optional)" :class="field" />
                    <InputError class="mt-1" :message="forwardForm.errors.comment" />
                    <PrimaryButton :disabled="forwardForm.processing || !forwardForm.port || !forwardForm.to_port">Add forward</PrimaryButton>
                </form>
            </div>
        </div>

        <!-- Every rule, as ufw has them. -->
        <div v-if="active" :class="card" class="!p-0">
            <button
                type="button"
                class="flex w-full items-center justify-between gap-3 px-4 py-4 text-start sm:px-6"
                :aria-expanded="showRules"
                @click="showRules = !showRules"
            >
                <span>
                    <span class="block font-semibold text-slate-900">All firewall rules</span>
                    <span class="text-sm text-slate-500">{{ firewall.rules.length }} rules, exactly as ufw has them. For when a port needs a rule removed by hand.</span>
                </span>
                <span class="text-slate-400">{{ showRules ? '−' : '+' }}</span>
            </button>
            <div v-if="showRules" class="overflow-x-auto border-t border-slate-100">
                <table class="w-full text-sm">
                    <thead class="text-left text-xs uppercase text-slate-400">
                        <tr>
                            <th class="px-4 py-3 sm:px-6">#</th>
                            <th class="px-2 py-3">Port / service</th>
                            <th class="px-2 py-3">Action</th>
                            <th class="px-2 py-3">From</th>
                            <th class="hidden px-2 py-3 sm:table-cell">Note</th>
                            <th />
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="rule in firewall.rules" :key="rule.number">
                            <td class="px-4 py-2 text-slate-400 sm:px-6">{{ rule.number }}</td>
                            <td class="break-all px-2 py-2 font-mono">{{ rule.to }}</td>
                            <td class="px-2 py-2">
                                <span class="whitespace-nowrap rounded-full px-2 py-0.5 text-xs font-medium" :class="actionStyle[rule.action]">
                                    {{ rule.action }} {{ rule.direction }}
                                </span>
                            </td>
                            <td class="px-2 py-2 text-slate-600"><IpAddress :ip="rule.from" /></td>
                            <td class="hidden px-2 py-2 text-slate-500 sm:table-cell">{{ rule.comment }}</td>
                            <td class="px-4 py-2 text-right">
                                <button type="button" :class="smallButton" class="text-rose-600" @click="removeRule(rule)">Remove</button>
                            </td>
                        </tr>
                        <tr v-if="!firewall.rules.length">
                            <td colspan="6" class="px-6 py-8 text-center text-slate-400">No rules.</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>
</template>
