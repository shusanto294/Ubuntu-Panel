<script setup>
import InputError from '@/Components/InputError.vue';
import IpAddress from '@/Components/IpAddress.vue';
import Pagination from '@/Components/Pagination.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import { usePagination } from '@/Composables/usePagination';
import { confirmAction, confirmDanger } from '@/dialog';
import { card, field, smallButton } from './ui';
import { router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    firewall: Object,
    forwards: { type: Array, default: () => [] },
    sshPorts: Array,
    panelPort: Number,
    myIp: String,
});

const { page, pages, total, perPage, visible: visibleRules } = usePagination(() => props.firewall.rules, 25);

const enable = async () => {
    const ports = (props.sshPorts?.length ? props.sshPorts : ['22']).join(', ');
    if (
        !(await confirmAction(
            'Switch the firewall on?',
            `SSH (${ports}), web (80, 443) and the panel (${props.panelPort}) are allowed first. Everything else incoming is refused. If your SSH runs somewhere else, add a rule for it after.`,
            'Switch on',
        ))
    )
        return;
    router.post(route('security.firewall.enable'), {}, { preserveScroll: true });
};

const disable = async () => {
    if (!(await confirmDanger('Switch the firewall off?', 'Every port something listens on will be open to the internet. The rules are kept.', 'Switch off')))
        return;
    router.post(route('security.firewall.disable'), {}, { preserveScroll: true });
};

// Port forwards: a port here that leads to another port, here or elsewhere.
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

const ruleForm = useForm({ action: 'allow', port: '', protocol: 'tcp', from: '', comment: '' });

const addRule = () =>
    ruleForm.post(route('security.firewall.rules.store'), {
        preserveScroll: true,
        onSuccess: () => ruleForm.reset(),
    });

const removeRule = async (rule) => {
    if (!(await confirmDanger(`Remove rule ${rule.number}?`, `${rule.action} ${rule.to} from ${rule.from}`, 'Remove')))
        return;
    router.delete(route('security.firewall.rules.destroy', rule.number), { preserveScroll: true });
};

const blockForm = useForm({ ip: '', comment: '' });

const block = () =>
    blockForm.post(route('security.firewall.block'), {
        preserveScroll: true,
        onSuccess: () => blockForm.reset(),
    });

const actionStyle = {
    ALLOW: 'bg-emerald-50 text-emerald-700',
    LIMIT: 'bg-sky-50 text-sky-700',
    DENY: 'bg-rose-50 text-rose-700',
    REJECT: 'bg-rose-50 text-rose-700',
};
</script>

<template>
    <div class="space-y-6">
        <div :class="card" class="flex flex-wrap items-center justify-between gap-4">
            <div class="min-w-0">
                <h3 class="flex items-center gap-2 font-semibold text-slate-900">
                    Firewall (ufw)
                    <span
                        class="rounded-full px-2 py-0.5 text-xs font-medium"
                        :class="firewall.active ? 'bg-emerald-50 text-emerald-700' : 'bg-rose-50 text-rose-700'"
                    >
                        {{ !firewall.installed ? 'not installed' : firewall.active ? 'on' : 'off' }}
                    </span>
                </h3>
                <p class="mt-1 text-sm text-slate-500">
                    <template v-if="firewall.active">
                        Incoming is {{ firewall.incoming }} by default, outgoing
                        {{ firewall.outgoing }}. Rules are checked top to bottom; the first
                        that matches wins.
                    </template>
                    <template v-else>
                        Every port a program listens on is reachable from the internet.
                    </template>
                </p>
            </div>
            <button
                v-if="firewall.installed && !firewall.active"
                type="button"
                class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-brand-700"
                @click="enable"
            >
                Switch on
            </button>
            <button
                v-else-if="firewall.active"
                type="button"
                class="rounded-xl px-4 py-2.5 text-sm font-medium text-rose-600 ring-1 ring-rose-200 hover:bg-rose-50"
                @click="disable"
            >
                Switch off
            </button>
        </div>

        <div v-if="firewall.active" class="grid gap-6 lg:grid-cols-3">
            <div :class="card" class="!p-0 lg:col-span-2">
                <div class="overflow-x-auto">
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
                        <tr v-for="rule in visibleRules" :key="rule.number">
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
                <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
            </div>

            <div class="space-y-6">
                <div :class="card">
                    <h3 class="font-semibold text-slate-900">Add a rule</h3>
                    <form class="mt-3 space-y-3" @submit.prevent="addRule">
                        <div class="grid grid-cols-2 gap-3">
                            <div>
                                <label class="block text-xs font-medium text-slate-700">Action</label>
                                <select v-model="ruleForm.action" :class="field">
                                    <option value="allow">Allow</option>
                                    <option value="deny">Deny</option>
                                    <option value="limit">Limit (rate)</option>
                                </select>
                                <InputError class="mt-1" :message="ruleForm.errors.action" />
                            </div>
                            <div>
                                <label class="block text-xs font-medium text-slate-700">Protocol</label>
                                <select v-model="ruleForm.protocol" :class="field">
                                    <option value="tcp">TCP</option>
                                    <option value="udp">UDP</option>
                                    <option value="any">Any</option>
                                </select>
                                <InputError class="mt-1" :message="ruleForm.errors.protocol" />
                            </div>
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Port or range</label>
                            <input v-model="ruleForm.port" type="text" placeholder="3306 or 6000:6100" :class="field" class="font-mono" />
                            <InputError class="mt-1" :message="ruleForm.errors.port" />
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">From <span class="font-normal text-slate-400">(empty for anywhere)</span></label>
                            <input v-model="ruleForm.from" type="text" placeholder="203.0.113.0/24" :class="field" class="font-mono" />
                            <InputError class="mt-1" :message="ruleForm.errors.from" />
                        </div>
                        <div>
                            <label class="block text-xs font-medium text-slate-700">Note</label>
                            <input v-model="ruleForm.comment" type="text" placeholder="Office MySQL" :class="field" />
                            <InputError class="mt-1" :message="ruleForm.errors.comment" />
                        </div>
                        <PrimaryButton :disabled="ruleForm.processing">Add rule</PrimaryButton>
                    </form>
                </div>

                <div :class="card">
                    <h3 class="font-semibold text-slate-900">Port forwarding</h3>
                    <p class="mt-1 text-xs text-slate-500">
                        Send connections on a port of this server somewhere else — another port here, or a machine on your
                        private network. The forwarded port is opened for you; the target is not. IPv4 only.
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
                                <label class="block text-xs font-medium text-slate-700">To address <span class="font-normal text-slate-400">(empty: this server)</span></label>
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

                <div :class="card">
                    <h3 class="font-semibold text-slate-900">Block an address everywhere</h3>
                    <p class="mt-1 text-xs text-slate-500">
                        Refused on every port, sites included — unlike an SSH ban.
                    </p>
                    <form class="mt-3 space-y-3" @submit.prevent="block">
                        <input v-model="blockForm.ip" type="text" placeholder="IP address or range" :class="field" class="font-mono" />
                        <input v-model="blockForm.comment" type="text" placeholder="Note (optional)" :class="field" />
                        <button
                            type="submit"
                            class="rounded-xl bg-rose-600 px-4 py-2.5 text-sm font-medium text-white hover:bg-rose-700 disabled:opacity-50"
                            :disabled="!blockForm.ip || blockForm.processing"
                        >
                            Block
                        </button>
                    </form>
                </div>
            </div>
        </div>
    </div>
</template>
