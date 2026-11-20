<script setup>
import { computed, reactive, ref } from 'vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { ACCESS_MODES, ACCESS_TARGETS, ACTION_LABEL, PERMISSION } from './edge';
import { card, field, smallButton, when } from './ui';
import { confirmDanger } from '@/dialog';
import ZoneErrors from './ZoneErrors.vue';
import ZoneScope from './ZoneScope.vue';

/*
 * IP access rules: an address, a range, a network or a country, always
 * blocked, challenged or let through. The simplest of Cloudflare's rules, and
 * the one to reach for when one address will not stop.
 *
 * The same rule in several zones is one row: changing what it does, its note
 * or deleting it does so in each, and a zone without it can be given it.
 * Rules made for the whole Cloudflare account show too, but can only be
 * changed in Cloudflare.
 *
 * A change shows at once and goes to Cloudflare behind it; the row says
 * "Saving…" until each zone's copy comes back.
 */
const props = defineProps({
    groups: { type: Array, default: () => [] }, // { key, rule, copies, missing, readonly, pending }
    errors: { type: Array, default: () => [] },
    zoneCount: { type: Number, default: 1 },
    single: { type: Boolean, default: true },
    myIp: { type: String, default: '' },
    allZones: { type: Array, default: () => [] },
    current: { type: Object, default: null },
    loadingZones: { type: Boolean, default: false },
});

const emit = defineEmits(['apply', 'synced', 'resync', 'message']);

const adding = ref(false);
const formError = ref('');
const search = ref('');

const form = reactive({ target: 'ip', value: '', mode: 'block', notes: '' });
const targets = ref([]);

const targetLabel = (key) => ACCESS_TARGETS.find((t) => t.key === key)?.label ?? key;
const placeholder = computed(() => ACCESS_TARGETS.find((t) => t.key === form.target)?.placeholder ?? '');

const filtered = computed(() => {
    const q = search.value.trim().toLowerCase();
    if (!q) return props.groups;
    return props.groups.filter((g) =>
        `${g.rule.value} ${g.rule.mode} ${g.copies.map((c) => c.zone_name).join(' ')}`.toLowerCase().includes(q),
    );
});

const pages = usePagination(filtered, 20);

const failure = (e, fallback) => e?.response?.data?.error ?? fallback;

const report = (data) => {
    const failures = (data.outcomes ?? []).filter((o) => o.status === 'failed');
    emit('message', data.message, failures.length > 0, failures);
};

const copiesOf = (group) => group.copies.map(({ account, zone_id, zone_name, rule_id }) => ({ account, zone_id, zone_name, rule_id }));

// Each copy's rule, in the zones the page holds.
const eachCopy = (all, copies, change) => {
    for (const copy of copies) {
        const zone = all.find((z) => z.zone_id === copy.zone_id);
        const at = zone?.access?.findIndex((r) => r.id === copy.rule_id) ?? -1;
        if (at >= 0) change(zone.access, at);
    }
};

// A rule not at Cloudflare yet, shown and marked until the zone comes back.
const addTo = (all, zones, rule) => {
    for (const target of zones) {
        all.find((z) => z.zone_id === target.zone_id)?.access?.unshift({
            id: `pending-${Math.random().toString(36).slice(2)}`,
            target: rule.target,
            value: rule.value.trim(),
            mode: rule.mode,
            notes: rule.notes,
            scope: 'zone',
            created_on: new Date().toISOString(),
            _pending: true,
        });
    }
};

const send = async (request, fallback = 'Cloudflare refused the change.') => {
    try {
        const { data } = await request();
        emit('synced', data.zones);
        report(data);
        return data;
    } catch (e) {
        emit('message', failure(e, fallback), true);
        emit('resync');
        return null;
    }
};

const allowMe = () => {
    adding.value = true;
    form.target = props.myIp.includes(':') ? 'ip6' : 'ip';
    form.value = props.myIp;
    form.mode = 'whitelist';
    form.notes = 'My address — added from the server panel';
};

const submit = async () => {
    formError.value = '';
    const rule = { ...form };
    const zones = targets.value;

    Object.assign(form, { value: '', notes: '' });
    adding.value = false;

    emit('apply', (all) => addTo(all, zones, rule));

    const data = await send(() => window.axios.post(route('edge.bulk.access'), { ...rule, targets: zones }), 'Cloudflare did not take the rule.');

    // Refused outright — most often the value itself: the form comes back
    // with it, and why.
    if (!data) {
        Object.assign(form, rule);
        adding.value = true;
        formError.value = 'Not added — see the message above.';
    }
};

const update = (group, changes) => {

    emit('apply', (all) =>
        eachCopy(all, group.copies, (list, at) => {
            list[at] = { ...list[at], ...changes, _pending: true };
        }),
    );

    send(() =>
        window.axios.post(route('edge.bulk.access.update'), {
            mode: group.rule.mode,
            notes: group.rule.notes,
            ...changes,
            copies: copiesOf(group),
        }),
    );
};

const addToRest = (group) => {
    const missing = group.missing;
    const rule = { target: group.rule.target, value: group.rule.value, mode: group.rule.mode, notes: group.rule.notes };

    emit('apply', (all) => addTo(all, missing, rule));
    send(() => window.axios.post(route('edge.bulk.access'), { ...rule, targets: missing }));
};

const remove = async (group) => {
    const where = group.copies.length > 1 ? ` from ${group.copies.length} zones` : '';
    if (!(await confirmDanger(`Delete the rule for ${group.rule.value}${where}?`, 'Cloudflare stops applying it at once.'))) return;

    const copies = copiesOf(group);
    emit('apply', (all) => eachCopy(all, copies, (list, at) => list.splice(at, 1)));
    send(() => window.axios.post(route('edge.bulk.access.destroy'), { value: group.rule.value, copies }));
};

// The line under an address: what it is, or — for one 404 protection made —
// that, and until when. Its note says so, in the words blockEverywhere writes.
const PROTECTION = /^Ubuntu Panel 404 protection: .* — until (.+)$/;

const origin = (group) => {
    const notes = group.rule.notes ?? '';
    if (/^Ubuntu Panel 404 protection: .* — until removed in the panel$/.test(notes)) return '404 protection · until removed';

    const made = PROTECTION.exec(notes);
    if (!made) return targetLabel(group.rule.target);

    const until = made[1].replace(/ UTC$/, 'Z').replace(' ', 'T');
    const date = new Date(until);
    return `404 protection · until ${Number.isNaN(date.getTime()) ? made[1] : when(date.toISOString())}`;
};

const ACTION_TEXT = {
    block: 'text-rose-700',
    whitelist: 'text-emerald-700',
};
const actionText = (mode) => ACTION_TEXT[mode] ?? 'text-amber-700';

const zoneNames = (group) => group.copies.map((c) => c.zone_name).join(', ');
</script>

<template>
    <section :class="card">
        <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="max-w-2xl">
                <h3 class="font-semibold text-slate-900">IP access rules</h3>
                <p class="text-sm text-slate-500">
                    Block, challenge or always allow an address, a range, a network (ASN) or a whole country —
                    whatever else the rules say.
                </p>
            </div>
            <div v-if="zoneCount > 0 && !adding" class="flex flex-wrap gap-2">
                <button
                    v-if="myIp"
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                    @click="allowMe"
                >
                    Always allow my address
                </button>
                <button
                    type="button"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                    @click="adding = true"
                >
                    Add rule
                </button>
            </div>
        </div>

        <ZoneErrors :errors="errors" :permission="PERMISSION.access" />

        <form v-if="adding" class="mt-4 space-y-3 rounded-xl bg-slate-50 p-4 ring-1 ring-slate-200" @submit.prevent="submit">
            <div class="grid gap-3 sm:grid-cols-4">
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">Match</span>
                    <select v-model="form.target" :class="field">
                        <option v-for="t in ACCESS_TARGETS" :key="t.key" :value="t.key">{{ t.label }}</option>
                    </select>
                </label>
                <label class="block text-sm sm:col-span-2">
                    <span class="text-xs font-medium text-slate-700">Value</span>
                    <input v-model="form.value" type="text" required :placeholder="placeholder" :class="[field, 'font-mono']" />
                </label>
                <label class="block text-sm">
                    <span class="text-xs font-medium text-slate-700">Then</span>
                    <select v-model="form.mode" :class="field">
                        <option v-for="m in ACCESS_MODES" :key="m" :value="m">{{ ACTION_LABEL[m] }}</option>
                    </select>
                </label>
            </div>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Note</span>
                <input v-model="form.notes" type="text" maxlength="500" :class="field" placeholder="Why — so it makes sense in a year" />
            </label>
            <ZoneScope v-model="targets" :zones="allZones" :current="current" :loading="loadingZones" :initial="single ? 'one' : 'sites'" />
            <p v-if="form.target === 'ip_range'" class="text-xs text-slate-500">
                Cloudflare takes IPv4 ranges as /16 or /24, and IPv6 ranges as /32, /48 or /64.
            </p>
            <div class="flex flex-wrap items-center justify-end gap-2">
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                    @click="adding = false"
                >
                    Cancel
                </button>
                <button
                    type="submit"
                    :disabled="!targets.length"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                >
                    {{ targets.length > 1 ? `Add to ${targets.length} zones` : 'Add rule' }}
                </button>
            </div>
            <p v-if="formError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200">{{ formError }}</p>
        </form>

        <template v-if="groups.length">
            <input
                v-if="groups.length > 10"
                v-model="search"
                type="search"
                placeholder="Search addresses and zones"
                class="mt-4 w-full rounded-lg border-slate-300 text-sm shadow-sm sm:w-72"
            />
            <div class="mt-4 overflow-x-auto rounded-xl ring-1 ring-slate-200">
                <table class="w-full text-sm">
                    <thead class="bg-slate-50 text-left text-xs text-slate-500">
                        <tr>
                            <th class="px-4 py-2 font-medium">Address</th>
                            <th class="px-4 py-2 font-medium">Then</th>
                            <th class="hidden px-4 py-2 font-medium sm:table-cell">{{ single ? 'Added' : 'Zones' }}</th>
                            <th class="px-4 py-2"><span class="sr-only">Actions</span></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="group in pages.visible.value" :key="group.key">
                            <td class="px-4 py-2">
                                <span class="whitespace-nowrap font-mono text-xs text-slate-800">{{ group.rule.value }}</span>
                                <span class="block text-[11px] text-slate-400">
                                    {{ origin(group) }}<template v-if="group.pending"> · Saving…</template>
                                </span>
                            </td>
                            <td class="whitespace-nowrap px-4 py-2 text-xs">
                                <select
                                    v-if="!group.readonly"
                                    :value="group.rule.mode"
                                    :disabled="group.pending"
                                    :aria-label="`What happens to ${group.rule.value}`"
                                    class="rounded-md border-0 bg-transparent py-0 pl-0 pr-7 text-xs font-medium focus:ring-0"
                                    :class="actionText(group.rule.mode)"
                                    @change="update(group, { mode: $event.target.value })"
                                >
                                    <option v-for="m in ACCESS_MODES" :key="m" :value="m">{{ ACTION_LABEL[m] }}</option>
                                </select>
                                <span v-else class="font-medium" :class="actionText(group.rule.mode)">
                                    {{ ACTION_LABEL[group.rule.mode] ?? group.rule.mode }}
                                </span>
                            </td>
                            <td class="hidden whitespace-nowrap px-4 py-2 text-xs sm:table-cell">
                                <span v-if="single" class="text-slate-500">{{ when(group.rule.created_on) }}</span>
                                <span v-else-if="group.readonly" class="text-slate-500">every zone on the account</span>
                                <span v-else-if="!group.missing.length" class="text-slate-500" :title="zoneNames(group)">
                                    All {{ group.copies.length }}
                                </span>
                                <span v-else>
                                    <span class="text-amber-700" :title="zoneNames(group)">
                                        {{ group.copies.length }} of {{ group.copies.length + group.missing.length }}
                                    </span>
                                    <button
                                        type="button"
                                        class="ml-1.5 font-medium text-brand-600 hover:underline disabled:opacity-50"
                                        :disabled="group.pending"
                                        :title="`Missing on ${group.missing.map((z) => z.zone_name).join(', ')}`"
                                        @click="addToRest(group)"
                                    >
                                        Add to the rest
                                    </button>
                                </span>
                            </td>
                            <td class="whitespace-nowrap px-4 py-2 text-right">
                                <button
                                    v-if="!group.readonly"
                                    type="button"
                                    :class="smallButton"
                                    :disabled="group.pending"
                                    @click="remove(group)"
                                >
                                    Remove
                                </button>
                                <span v-else class="text-xs text-slate-400" title="Made for the whole Cloudflare account — change it in Cloudflare">
                                    account-wide
                                </span>
                            </td>
                        </tr>
                    </tbody>
                </table>
                <Pagination
                    v-model="pages.page.value"
                    :pages="pages.pages.value"
                    :total="pages.total.value"
                    :per-page="pages.perPage"
                />
            </div>
        </template>
        <p v-else-if="zoneCount > 0 && !adding" class="mt-4 text-sm text-slate-500">
            No IP access rules {{ single ? 'on this zone' : 'on these zones' }} yet.
        </p>
    </section>
</template>
