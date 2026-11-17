<script setup>
import { computed, reactive, ref } from 'vue';
import Pagination from '@/Components/Pagination.vue';
import { usePagination } from '@/Composables/usePagination';
import { ACCESS_MODES, ACCESS_TARGETS, ACTION_LABEL, ACTION_TONE, PERMISSION, isPermissionError } from './edge';
import { card, field, smallButton, when } from './ui';
import { confirmDanger } from '@/dialog';
import ZoneScope from './ZoneScope.vue';

/*
 * IP access rules: an address, a range, a network or a country, always
 * blocked, challenged or let through. The simplest of Cloudflare's rules, and
 * the one to reach for when one address will not stop. Rules made for the
 * whole account show here too, but can only be changed in Cloudflare.
 */
const props = defineProps({
    accountId: { type: Number, required: true },
    zoneId: { type: String, required: true },
    rules: { type: Array, default: () => [] },
    error: { type: String, default: '' },
    myIp: { type: String, default: '' },
    allZones: { type: Array, default: () => [] },
    current: { type: Object, default: null },
    loadingZones: { type: Boolean, default: false },
});

const emit = defineEmits(['changed', 'message']);

const adding = ref(false);
const saving = ref(false);
const formError = ref('');
const busy = ref('');
const search = ref('');
const editingNote = ref('');
const note = ref('');

const form = reactive({ target: 'ip', value: '', mode: 'block', notes: '' });
const targets = ref([]);
const onlyHere = computed(() => targets.value.length <= 1 && (targets.value[0]?.zone_id ?? props.zoneId) === props.zoneId);

const targetLabel = (key) => ACCESS_TARGETS.find((t) => t.key === key)?.label ?? key;
const placeholder = computed(() => ACCESS_TARGETS.find((t) => t.key === form.target)?.placeholder ?? '');

const filtered = computed(() => {
    const q = search.value.trim().toLowerCase();
    if (!q) return props.rules;
    return props.rules.filter((r) => `${r.value} ${r.notes} ${r.mode}`.toLowerCase().includes(q));
});

const pages = usePagination(filtered, 20);

const failure = (e, fallback) => e?.response?.data?.error ?? fallback;

const allowMe = () => {
    adding.value = true;
    form.target = props.myIp.includes(':') ? 'ip6' : 'ip';
    form.value = props.myIp;
    form.mode = 'whitelist';
    form.notes = 'My address — added from the server panel';
};

const submit = async () => {
    saving.value = true;
    formError.value = '';

    try {
        const { data } = onlyHere.value
            ? await window.axios.post(route('edge.access.store', props.accountId), { ...form, zone_id: props.zoneId })
            : await window.axios.post(route('edge.bulk.access'), { ...form, targets: targets.value });
        Object.assign(form, { value: '', notes: '' });
        adding.value = false;
        const failures = (data.outcomes ?? []).filter((o) => o.status === 'failed');
        emit('message', data.message, failures.length > 0, failures);
        emit('changed');
    } catch (e) {
        formError.value = failure(e, 'Cloudflare did not take the rule.');
    } finally {
        saving.value = false;
    }
};

const update = async (rule, changes) => {
    busy.value = rule.id;
    try {
        const { data } = await window.axios.patch(route('edge.access.update', { account: props.accountId, rule: rule.id }), {
            zone_id: props.zoneId,
            mode: rule.mode,
            notes: rule.notes,
            ...changes,
        });
        editingNote.value = '';
        emit('message', data.message);
        emit('changed');
    } catch (e) {
        emit('message', failure(e, 'Cloudflare refused the change.'), true);
    } finally {
        busy.value = '';
    }
};

const editNote = (rule) => {
    editingNote.value = rule.id;
    note.value = rule.notes;
};

const remove = async (rule) => {
    if (!(await confirmDanger(`Delete the rule for ${rule.value}?`, 'Cloudflare stops applying it at once.'))) return;

    busy.value = rule.id;
    try {
        const { data } = await window.axios.delete(route('edge.access.destroy', { account: props.accountId, rule: rule.id }), {
            params: { zone_id: props.zoneId },
        });
        emit('message', data.message);
        emit('changed');
    } catch (e) {
        emit('message', failure(e, 'Cloudflare refused the change.'), true);
    } finally {
        busy.value = '';
    }
};
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
            <div v-if="!error" class="flex flex-wrap gap-2">
                <button
                    v-if="myIp && !adding"
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                    @click="allowMe"
                >
                    Always allow my address
                </button>
                <button
                    v-if="!adding"
                    type="button"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                    @click="adding = true"
                >
                    Add rule
                </button>
            </div>
        </div>

        <div v-if="error" class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 ring-1 ring-amber-200">
            <p>{{ error }}</p>
            <p v-if="isPermissionError(error)" class="mt-1 text-xs">
                The API token needs <strong>{{ PERMISSION.access }}</strong> — edit it in Cloudflare under My Profile →
                API Tokens, then reload this tab.
            </p>
        </div>

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
            <ZoneScope v-model="targets" :zones="allZones" :current="current" :loading="loadingZones" />
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
                    :disabled="saving"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                >
                    {{ saving ? 'Saving…' : onlyHere ? 'Add rule' : `Add to ${targets.length} zones` }}
                </button>
            </div>
            <p v-if="formError" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200">{{ formError }}</p>
        </form>

        <template v-if="!error && rules.length">
            <input
                v-if="rules.length > 10"
                v-model="search"
                type="search"
                placeholder="Search addresses and notes"
                class="mt-4 w-full rounded-lg border-slate-300 text-sm shadow-sm sm:w-72"
            />
            <div class="mt-4 overflow-x-auto rounded-xl ring-1 ring-slate-200">
                <table class="w-full text-sm">
                    <thead class="bg-slate-50 text-left text-xs text-slate-500">
                        <tr>
                            <th class="px-4 py-2 font-medium">Matches</th>
                            <th class="px-4 py-2 font-medium">Then</th>
                            <th class="px-4 py-2 font-medium">Note</th>
                            <th class="px-4 py-2 font-medium">Added</th>
                            <th class="px-4 py-2"><span class="sr-only">Actions</span></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100">
                        <tr v-for="rule in pages.visible.value" :key="rule.id" :class="busy === rule.id ? 'opacity-50' : ''">
                            <td class="px-4 py-2.5">
                                <span class="font-mono text-slate-900">{{ rule.value }}</span>
                                <span class="block text-xs text-slate-400">{{ targetLabel(rule.target) }}</span>
                            </td>
                            <td class="px-4 py-2.5">
                                <select
                                    v-if="rule.scope === 'zone'"
                                    :value="rule.mode"
                                    :disabled="Boolean(busy)"
                                    class="rounded-lg border-slate-300 py-1 text-xs shadow-sm"
                                    @change="update(rule, { mode: $event.target.value })"
                                >
                                    <option v-for="m in ACCESS_MODES" :key="m" :value="m">{{ ACTION_LABEL[m] }}</option>
                                </select>
                                <span v-else class="rounded-md px-2 py-0.5 text-xs font-medium ring-1" :class="ACTION_TONE[rule.mode]">
                                    {{ ACTION_LABEL[rule.mode] ?? rule.mode }}
                                </span>
                            </td>
                            <td class="max-w-xs px-4 py-2.5">
                                <form v-if="editingNote === rule.id" class="flex gap-1" @submit.prevent="update(rule, { notes: note })">
                                    <input v-model="note" type="text" maxlength="500" class="w-full rounded-lg border-slate-300 py-1 text-xs shadow-sm" />
                                    <button type="submit" :class="smallButton">Save</button>
                                </form>
                                <span v-else class="block truncate text-xs text-slate-600" :title="rule.notes">{{ rule.notes || '—' }}</span>
                            </td>
                            <td class="whitespace-nowrap px-4 py-2.5 text-xs text-slate-500">{{ when(rule.created_on) }}</td>
                            <td class="whitespace-nowrap px-4 py-2.5 text-right">
                                <template v-if="rule.scope === 'zone'">
                                    <button
                                        v-if="editingNote !== rule.id"
                                        type="button"
                                        :class="smallButton"
                                        :disabled="Boolean(busy)"
                                        @click="editNote(rule)"
                                    >
                                        Note
                                    </button>
                                    <button
                                        type="button"
                                        :class="smallButton"
                                        class="ml-1.5 text-rose-600"
                                        :disabled="Boolean(busy)"
                                        @click="remove(rule)"
                                    >
                                        Delete
                                    </button>
                                </template>
                                <span v-else class="text-xs text-slate-400" title="Made for the whole Cloudflare account">account-wide</span>
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
        <p v-else-if="!error && !adding" class="mt-4 text-sm text-slate-500">No IP access rules on this zone yet.</p>
    </section>
</template>
