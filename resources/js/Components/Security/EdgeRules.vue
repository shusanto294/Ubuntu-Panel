<script setup>
import { ref } from 'vue';
import EdgeRuleForm from './EdgeRuleForm.vue';
import ZoneErrors from './ZoneErrors.vue';
import { ACTION_LABEL, ACTION_TONE, PERMISSION, seconds } from './edge';
import { card, smallButton, when } from './ui';
import { confirmDanger } from '@/dialog';

/*
 * One phase's rules — WAF custom rules or rate limiting rules — across every
 * zone the tab shows. The same rule in several zones is one row: editing,
 * switching off or deleting it does it in each of them, and a rule some zones
 * lack can be added to the rest. Every change goes to Cloudflare and the list
 * is read again, so what is shown is what Cloudflare has.
 */
const props = defineProps({
    phase: { type: String, required: true }, // 'custom' | 'ratelimit'
    groups: { type: Array, default: () => [] }, // { key, rule, copies, enabledCount, missing }
    errors: { type: Array, default: () => [] }, // { zone, error }
    zoneCount: { type: Number, default: 1 }, // zones shown, readable for this phase
    single: { type: Boolean, default: true }, // one zone: rules can be moved
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

// null, 'new', or the key of the rule being edited.
const editing = ref(null);
const saving = ref(false);
const formError = ref('');
const busy = ref('');
const expanded = ref(new Set());

const failure = (e, fallback) => e?.response?.data?.error ?? fallback;

const report = (data) => {
    const failures = (data.outcomes ?? []).filter((o) => o.status === 'failed');
    emit('message', data.message, failures.length > 0, failures);
};

const copiesOf = (group) => group.copies.map(({ account, zone_id, zone_name, rule_id }) => ({ account, zone_id, zone_name, rule_id }));

const ruleFields = (rule) => ({
    phase: props.phase,
    description: rule.description,
    expression: rule.expression,
    action: rule.action,
    enabled: rule.enabled,
    ...(rule.ratelimit
        ? {
              period: rule.ratelimit.period,
              requests_per_period: rule.ratelimit.requests_per_period,
              mitigation_timeout: rule.ratelimit.mitigation_timeout,
              characteristics: 'keep',
          }
        : {}),
});

const save = async (payload, group = null) => {
    saving.value = true;
    formError.value = '';

    try {
        const { targets, addMissing, ...rule } = payload;

        if (group) {
            const { data } = await window.axios.post(route('edge.bulk.rules.update'), { ...rule, copies: copiesOf(group) });
            if (addMissing && group.missing.length) {
                // A new copy counts per address, as the panel makes them.
                const { characteristics, ...fresh } = rule;
                const { data: added } = await window.axios.post(route('edge.bulk.rules'), { ...fresh, targets: group.missing });
                const failures = [data, added].flatMap((d) => (d.outcomes ?? []).filter((o) => o.status === 'failed'));
                emit('message', `${data.message} ${added.message}`, failures.length > 0, failures);
            } else {
                report(data);
            }
        } else {
            const { data } = await window.axios.post(route('edge.bulk.rules'), { ...rule, targets });
            report(data);
        }

        editing.value = null;
        emit('changed');
    } catch (e) {
        formError.value = failure(e, 'Cloudflare did not take the rule.');
    } finally {
        saving.value = false;
    }
};

const act = async (group, request) => {
    busy.value = group.key;
    try {
        const { data } = await request();
        data.outcomes ? report(data) : emit('message', data.message);
        emit('changed');
    } catch (e) {
        emit('message', failure(e, 'Cloudflare refused the change.'), true);
    } finally {
        busy.value = '';
    }
};

// Off when every copy is on; otherwise on everywhere.
const toggle = (group) =>
    act(group, () =>
        window.axios.post(route('edge.bulk.rules.update'), {
            ...ruleFields(group.rule),
            enabled: group.enabledCount < group.copies.length,
            copies: copiesOf(group),
        }),
    );

const addToRest = (group) => {
    const { characteristics, ...rule } = ruleFields(group.rule);
    act(group, () => window.axios.post(route('edge.bulk.rules'), { ...rule, targets: group.missing }));
};

const move = (group, position) => {
    const copy = group.copies[0];
    act(group, () =>
        window.axios.post(route('edge.rules.move', { account: copy.account, rule: copy.rule_id }), {
            zone_id: copy.zone_id,
            phase: props.phase,
            position,
        }),
    );
};

const remove = async (group) => {
    const name = group.rule.description || 'this rule';
    const where = group.copies.length > 1 ? ` from ${group.copies.length} zones` : '';
    if (!(await confirmDanger(`Delete “${name}”${where}?`, 'Cloudflare stops applying it at once.'))) return;

    act(group, () =>
        window.axios.post(route('edge.bulk.rules.destroy'), {
            phase: props.phase,
            description: group.rule.description,
            copies: copiesOf(group),
        }),
    );
};

const toggleExpanded = (key) => {
    const next = new Set(expanded.value);
    next.has(key) ? next.delete(key) : next.add(key);
    expanded.value = next;
};

const start = (key) => {
    formError.value = '';
    editing.value = key;
};

const zoneNames = (group) => group.copies.map((c) => c.zone_name).join(', ');
</script>

<template>
    <section :class="card">
        <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="max-w-2xl">
                <h3 class="font-semibold text-slate-900">{{ TITLE[phase] }}</h3>
                <p class="text-sm text-slate-500">{{ INTRO[phase] }}</p>
            </div>
            <button
                v-if="editing !== 'new' && zoneCount > 0"
                type="button"
                class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700"
                @click="start('new')"
            >
                Add rule
            </button>
        </div>

        <ZoneErrors :errors="errors" :permission="PERMISSION[phase]" />

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
            :scope="single ? 'one' : 'sites'"
            @save="save($event)"
            @cancel="editing = null"
        />

        <p v-if="zoneCount > 0 && !groups.length && editing !== 'new'" class="mt-4 text-sm text-slate-500">
            No {{ phase === 'custom' ? 'custom' : 'rate limiting' }} rules {{ single ? 'on this zone' : 'on these zones' }} yet.
        </p>

        <ol v-if="groups.length" class="mt-4 divide-y divide-slate-100 rounded-xl ring-1 ring-slate-200">
            <li v-for="(group, i) in groups" :key="group.key" class="p-4" :class="busy === group.key ? 'opacity-50' : ''">
                <EdgeRuleForm
                    v-if="editing === group.key"
                    :phase="phase"
                    :rule="group.rule"
                    :my-ip="myIp"
                    :saving="saving"
                    :error="formError"
                    :copies="group.copies.length"
                    :missing="group.missing"
                    @save="save($event, group)"
                    @cancel="editing = null"
                />
                <template v-else>
                    <div class="flex flex-wrap items-start justify-between gap-3">
                        <div class="min-w-0 flex-1">
                            <p class="flex flex-wrap items-center gap-2">
                                <span v-if="phase === 'custom' && single" class="text-xs font-semibold tabular-nums text-slate-400">{{ i + 1 }}</span>
                                <span class="font-medium" :class="group.enabledCount ? 'text-slate-900' : 'text-slate-400 line-through'">
                                    {{ group.rule.description || '(no name)' }}
                                </span>
                                <span class="rounded-md px-2 py-0.5 text-xs font-medium ring-1" :class="ACTION_TONE[group.rule.action] ?? ACTION_TONE.log">
                                    {{ ACTION_LABEL[group.rule.action] ?? group.rule.action }}
                                </span>
                                <span v-if="!group.enabledCount" class="rounded-md bg-slate-100 px-2 py-0.5 text-xs text-slate-500">off</span>
                                <span
                                    v-else-if="group.enabledCount < group.copies.length"
                                    class="rounded-md bg-amber-50 px-2 py-0.5 text-xs text-amber-700"
                                >
                                    off on {{ group.copies.length - group.enabledCount }} of {{ group.copies.length }}
                                </span>
                            </p>
                            <p v-if="group.rule.ratelimit" class="mt-1 text-xs text-slate-600">
                                Over {{ group.rule.ratelimit.requests_per_period.toLocaleString() }} requests in
                                {{ seconds(group.rule.ratelimit.period) }}, then for {{ seconds(group.rule.ratelimit.mitigation_timeout) }}
                            </p>
                            <button
                                type="button"
                                class="mt-1 block max-w-full text-left font-mono text-xs text-slate-500 hover:text-slate-800"
                                :class="expanded.has(group.key) ? 'whitespace-pre-wrap break-all' : 'truncate'"
                                :title="expanded.has(group.key) ? '' : 'Show the whole expression'"
                                @click="toggleExpanded(group.key)"
                            >
                                {{ group.rule.expression }}
                            </button>
                            <p v-if="!single" class="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                                <span
                                    v-if="!group.missing.length"
                                    class="rounded-md bg-emerald-50 px-2 py-0.5 font-medium text-emerald-700"
                                    :title="zoneNames(group)"
                                >
                                    {{ group.copies.length === 2 ? 'On both zones' : `On all ${group.copies.length} zones` }}
                                </span>
                                <template v-else>
                                    <span class="rounded-md bg-amber-50 px-2 py-0.5 font-medium text-amber-700" :title="zoneNames(group)">
                                        On {{ group.copies.length }} of {{ group.copies.length + group.missing.length }} zones
                                    </span>
                                    <span class="text-slate-500">
                                        missing on {{ group.missing.map((z) => z.zone_name).join(', ') }}
                                    </span>
                                    <button
                                        type="button"
                                        class="font-medium text-brand-600 hover:underline disabled:opacity-50"
                                        :disabled="Boolean(busy)"
                                        @click="addToRest(group)"
                                    >
                                        Add to {{ group.missing.length === 1 ? 'it' : `the other ${group.missing.length}` }}
                                    </button>
                                </template>
                            </p>
                            <p v-else-if="group.rule.last_updated" class="mt-1 text-[11px] text-slate-400">
                                Changed {{ when(group.rule.last_updated) }}
                            </p>
                        </div>

                        <div class="flex flex-wrap items-center gap-1.5">
                            <template v-if="phase === 'custom' && single && groups.length > 1">
                                <button
                                    type="button"
                                    :class="smallButton"
                                    :disabled="i === 0 || Boolean(busy)"
                                    class="disabled:opacity-30"
                                    :aria-label="`Move ${group.rule.description} up`"
                                    @click="move(group, i)"
                                >
                                    ↑
                                </button>
                                <button
                                    type="button"
                                    :class="smallButton"
                                    :disabled="i === groups.length - 1 || Boolean(busy)"
                                    class="disabled:opacity-30"
                                    :aria-label="`Move ${group.rule.description} down`"
                                    @click="move(group, i + 2)"
                                >
                                    ↓
                                </button>
                            </template>
                            <button type="button" :class="smallButton" :disabled="Boolean(busy)" @click="toggle(group)">
                                {{ group.enabledCount === group.copies.length ? 'Turn off' : 'Turn on' }}
                            </button>
                            <button type="button" :class="smallButton" :disabled="Boolean(busy)" @click="start(group.key)">Edit</button>
                            <button
                                type="button"
                                :class="smallButton"
                                class="text-rose-600"
                                :disabled="Boolean(busy)"
                                @click="remove(group)"
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
