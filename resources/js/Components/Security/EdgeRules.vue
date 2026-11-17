<script setup>
import { ref } from 'vue';
import EdgeRuleForm from './EdgeRuleForm.vue';
import { ACTION_LABEL, ACTION_TONE, PERMISSION, isPermissionError, seconds } from './edge';
import { card, smallButton, when } from './ui';
import { confirmDanger } from '@/dialog';

/*
 * One phase's rules — WAF custom rules or rate limiting rules — in the order
 * Cloudflare runs them, with adding, editing, switching off, moving and
 * deleting. Every change goes to Cloudflare and the list is read again, so
 * what is shown is what Cloudflare has.
 */
const props = defineProps({
    accountId: { type: Number, required: true },
    zoneId: { type: String, required: true },
    phase: { type: String, required: true }, // 'custom' | 'ratelimit'
    rules: { type: Array, default: () => [] },
    error: { type: String, default: '' },
    myIp: { type: String, default: '' },
    allZones: { type: Array, default: () => [] },
    current: { type: Object, default: null },
    loadingZones: { type: Boolean, default: false },
});

const emit = defineEmits(['changed', 'message']);

const TITLE = {
    custom: 'Custom rules',
    ratelimit: 'Rate limiting rules',
};

const INTRO = {
    custom: 'Cloudflare runs these top to bottom on every request, before it reaches the server. The first that blocks, challenges or skips is the last that runs.',
    ratelimit: 'Too many requests from one address in a period, and Cloudflare blocks or challenges it for a while.',
};

// null, 'new', or the id of the rule being edited.
const editing = ref(null);
const saving = ref(false);
const formError = ref('');
const busy = ref('');
const expanded = ref(new Set());

const failure = (e, fallback) => e?.response?.data?.error ?? fallback;

const save = async (payload) => {
    saving.value = true;
    formError.value = '';

    try {
        const { targets, ...rule } = payload;
        const body = { ...rule, zone_id: props.zoneId };
        const onlyHere = !targets || (targets.length === 1 && targets[0].zone_id === props.zoneId);

        let data;
        if (editing.value !== 'new') {
            ({ data } = await window.axios.patch(route('edge.rules.update', { account: props.accountId, rule: editing.value }), body));
        } else if (onlyHere) {
            ({ data } = await window.axios.post(route('edge.rules.store', props.accountId), body));
        } else {
            ({ data } = await window.axios.post(route('edge.bulk.rules'), { ...rule, targets }));
        }

        editing.value = null;
        const failures = (data.outcomes ?? []).filter((o) => o.status === 'failed');
        emit('message', data.message, failures.length > 0, failures);
        emit('changed');
    } catch (e) {
        formError.value = failure(e, 'Cloudflare did not take the rule.');
    } finally {
        saving.value = false;
    }
};

const act = async (rule, request) => {
    busy.value = rule.id;
    try {
        const { data } = await request();
        emit('message', data.message);
        emit('changed');
    } catch (e) {
        emit('message', failure(e, 'Cloudflare refused the change.'), true);
    } finally {
        busy.value = '';
    }
};

const toggle = (rule) =>
    act(rule, () =>
        window.axios.patch(route('edge.rules.update', { account: props.accountId, rule: rule.id }), {
            zone_id: props.zoneId,
            phase: props.phase,
            description: rule.description,
            expression: rule.expression,
            action: rule.action,
            enabled: !rule.enabled,
            ...(rule.ratelimit
                ? {
                      period: rule.ratelimit.period,
                      requests_per_period: rule.ratelimit.requests_per_period,
                      mitigation_timeout: rule.ratelimit.mitigation_timeout,
                      characteristics: 'keep',
                  }
                : {}),
        }),
    );

const move = (rule, position) =>
    act(rule, () =>
        window.axios.post(route('edge.rules.move', { account: props.accountId, rule: rule.id }), {
            zone_id: props.zoneId,
            phase: props.phase,
            position,
        }),
    );

const remove = async (rule) => {
    if (!(await confirmDanger(`Delete “${rule.description || 'this rule'}”?`, 'Cloudflare stops applying it at once.'))) return;

    act(rule, () =>
        window.axios.delete(route('edge.rules.destroy', { account: props.accountId, rule: rule.id }), {
            params: { zone_id: props.zoneId, phase: props.phase },
        }),
    );
};

const toggleExpanded = (id) => {
    const next = new Set(expanded.value);
    next.has(id) ? next.delete(id) : next.add(id);
    expanded.value = next;
};

const startNew = () => {
    formError.value = '';
    editing.value = 'new';
};

const startEdit = (rule) => {
    formError.value = '';
    editing.value = rule.id;
};
</script>

<template>
    <section :class="card">
        <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="max-w-2xl">
                <h3 class="font-semibold text-slate-900">{{ TITLE[phase] }}</h3>
                <p class="text-sm text-slate-500">{{ INTRO[phase] }}</p>
            </div>
            <button
                v-if="!error && editing !== 'new'"
                type="button"
                class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                @click="startNew"
            >
                Add rule
            </button>
        </div>

        <div v-if="error" class="mt-4 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800 ring-1 ring-amber-200">
            <p>{{ error }}</p>
            <p v-if="isPermissionError(error)" class="mt-1 text-xs">
                The API token needs <strong>{{ PERMISSION[phase] }}</strong> — edit it in Cloudflare under My Profile →
                API Tokens, then reload this tab.
            </p>
        </div>

        <EdgeRuleForm
            v-if="editing === 'new'"
            class="mt-4"
            :phase="phase"
            :my-ip="myIp"
            :saving="saving"
            :error="formError"
            :all-zones="allZones"
            :current="current"
            :loading-zones="loadingZones"
            @save="save"
            @cancel="editing = null"
        />

        <p v-if="!error && !rules.length && editing !== 'new'" class="mt-4 text-sm text-slate-500">
            No {{ phase === 'custom' ? 'custom' : 'rate limiting' }} rules on this zone yet.
        </p>

        <ol v-if="rules.length" class="mt-4 divide-y divide-slate-100 rounded-xl ring-1 ring-slate-200">
            <li v-for="(rule, i) in rules" :key="rule.id" class="p-4" :class="busy === rule.id ? 'opacity-50' : ''">
                <EdgeRuleForm
                    v-if="editing === rule.id"
                    :phase="phase"
                    :rule="rule"
                    :my-ip="myIp"
                    :saving="saving"
                    :error="formError"
                    @save="save"
                    @cancel="editing = null"
                />
                <template v-else>
                    <div class="flex flex-wrap items-start justify-between gap-3">
                        <div class="min-w-0 flex-1">
                            <p class="flex flex-wrap items-center gap-2">
                                <span v-if="phase === 'custom'" class="text-xs font-semibold tabular-nums text-slate-400">{{ i + 1 }}</span>
                                <span class="font-medium" :class="rule.enabled ? 'text-slate-900' : 'text-slate-400 line-through'">
                                    {{ rule.description || '(no name)' }}
                                </span>
                                <span class="rounded-md px-2 py-0.5 text-xs font-medium ring-1" :class="ACTION_TONE[rule.action] ?? ACTION_TONE.log">
                                    {{ ACTION_LABEL[rule.action] ?? rule.action }}
                                </span>
                                <span v-if="!rule.enabled" class="rounded-md bg-slate-100 px-2 py-0.5 text-xs text-slate-500">off</span>
                            </p>
                            <p v-if="rule.ratelimit" class="mt-1 text-xs text-slate-600">
                                Over {{ rule.ratelimit.requests_per_period.toLocaleString() }} requests in
                                {{ seconds(rule.ratelimit.period) }}, then for {{ seconds(rule.ratelimit.mitigation_timeout) }}
                            </p>
                            <button
                                type="button"
                                class="mt-1 block max-w-full text-left font-mono text-xs text-slate-500 hover:text-slate-800"
                                :class="expanded.has(rule.id) ? 'whitespace-pre-wrap break-all' : 'truncate'"
                                :title="expanded.has(rule.id) ? '' : 'Show the whole expression'"
                                @click="toggleExpanded(rule.id)"
                            >
                                {{ rule.expression }}
                            </button>
                            <p v-if="rule.last_updated" class="mt-1 text-[11px] text-slate-400">Changed {{ when(rule.last_updated) }}</p>
                        </div>

                        <div class="flex flex-wrap items-center gap-1.5">
                            <template v-if="phase === 'custom' && rules.length > 1">
                                <button
                                    type="button"
                                    :class="smallButton"
                                    :disabled="i === 0 || Boolean(busy)"
                                    class="disabled:opacity-30"
                                    :aria-label="`Move ${rule.description} up`"
                                    @click="move(rule, i)"
                                >
                                    ↑
                                </button>
                                <button
                                    type="button"
                                    :class="smallButton"
                                    :disabled="i === rules.length - 1 || Boolean(busy)"
                                    class="disabled:opacity-30"
                                    :aria-label="`Move ${rule.description} down`"
                                    @click="move(rule, i + 2)"
                                >
                                    ↓
                                </button>
                            </template>
                            <button type="button" :class="smallButton" :disabled="Boolean(busy)" @click="toggle(rule)">
                                {{ rule.enabled ? 'Turn off' : 'Turn on' }}
                            </button>
                            <button type="button" :class="smallButton" :disabled="Boolean(busy)" @click="startEdit(rule)">Edit</button>
                            <button
                                type="button"
                                :class="smallButton"
                                class="text-rose-600"
                                :disabled="Boolean(busy)"
                                @click="remove(rule)"
                            >
                                Delete
                            </button>
                        </div>
                    </div>
                </template>
            </li>
        </ol>
    </section>
</template>
