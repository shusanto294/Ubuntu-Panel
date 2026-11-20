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
 * lack can be added to the rest.
 *
 * A change shows at once (apply) and goes to Cloudflare behind it; the row
 * says "Saving…" until the panel's copy of each zone comes back (synced) —
 * Cloudflare's answer, or the rule as it was where Cloudflare refused.
 */
const props = defineProps({
    phase: { type: String, required: true }, // 'custom' | 'ratelimit'
    groups: { type: Array, default: () => [] }, // { key, rule, copies, enabledCount, missing, pending }
    errors: { type: Array, default: () => [] }, // { zone, error }
    zoneCount: { type: Number, default: 1 }, // zones shown, readable for this phase
    single: { type: Boolean, default: true }, // one zone: rules can be moved
    myIp: { type: String, default: '' },
    allZones: { type: Array, default: () => [] },
    current: { type: Object, default: null },
    loadingZones: { type: Boolean, default: false },
});

const emit = defineEmits(['apply', 'synced', 'resync', 'message']);

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
const formError = ref('');
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

// ---------------------------------------------- the change, on the page first

// Each copy's rule, in the zones the page holds.
const eachCopy = (all, copies, change) => {
    for (const copy of copies) {
        const zone = all.find((z) => z.zone_id === copy.zone_id);
        const list = zone?.[props.phase];
        const at = list?.findIndex((r) => r.id === copy.rule_id) ?? -1;
        if (at >= 0) change(list, at, zone);
    }
};

// A rule that is not at Cloudflare yet: shown, marked, and replaced by the
// real one when the zone comes back.
const placeholder = (rule) => ({
    id: `pending-${Math.random().toString(36).slice(2)}`,
    description: rule.description,
    expression: rule.expression,
    action: rule.action,
    enabled: rule.enabled !== false && rule.enabled !== '0',
    ratelimit:
        props.phase === 'ratelimit'
            ? {
                  period: Number(rule.period),
                  requests_per_period: Number(rule.requests_per_period),
                  mitigation_timeout: Number(rule.mitigation_timeout),
              }
            : undefined,
    _pending: true,
});

const addTo = (all, targets, rule) => {
    for (const target of targets) {
        all.find((z) => z.zone_id === target.zone_id)?.[props.phase]?.push(placeholder(rule));
    }
};

// send is the Cloudflare half: the zones' copies on an answer, and the page
// put back from the panel's copy when the panel refused the request itself.
const send = async (request, fallback = 'Cloudflare refused the change.') => {
    try {
        const { data } = await request();
        emit('synced', data.zones);
        data.outcomes ? report(data) : emit('message', data.message);
        return data;
    } catch (e) {
        emit('message', failure(e, fallback), true);
        emit('resync');
        return null;
    }
};

const save = async (payload, group = null) => {
    formError.value = '';
    const { targets, addMissing, ...rule } = payload;
    editing.value = null;

    if (!group) {
        emit('apply', (all) => addTo(all, targets, rule));
        send(() => window.axios.post(route('edge.bulk.rules'), { ...rule, targets }), 'Cloudflare did not take the rule.');
        return;
    }

    const missing = addMissing ? group.missing : [];

    emit('apply', (all) => {
        eachCopy(all, group.copies, (list, at) => {
            const ratelimit = list[at].ratelimit
                ? {
                      ...list[at].ratelimit,
                      period: Number(rule.period),
                      requests_per_period: Number(rule.requests_per_period),
                      mitigation_timeout: Number(rule.mitigation_timeout),
                  }
                : undefined;
            list[at] = { ...list[at], ...placeholder(rule), id: list[at].id, ratelimit };
        });
        addTo(all, missing, rule);
    });

    const updated = await send(() => window.axios.post(route('edge.bulk.rules.update'), { ...rule, copies: copiesOf(group) }));

    if (updated && missing.length) {
        // A new copy counts per address, as the panel makes them.
        const { characteristics, ...fresh } = rule;
        send(() => window.axios.post(route('edge.bulk.rules'), { ...fresh, targets: missing }));
    }
};

// Off when every copy is on; otherwise on everywhere.
const toggle = (group) => {
    const enabled = group.enabledCount < group.copies.length;

    emit('apply', (all) =>
        eachCopy(all, group.copies, (list, at) => {
            list[at] = { ...list[at], enabled, _pending: true };
        }),
    );

    send(() =>
        window.axios.post(route('edge.bulk.rules.update'), {
            ...ruleFields(group.rule),
            enabled,
            copies: copiesOf(group),
        }),
    );
};

const addToRest = (group) => {
    const { characteristics, ...rule } = ruleFields(group.rule);
    const missing = group.missing;

    emit('apply', (all) =>
        addTo(all, missing, {
            ...rule,
            period: group.rule.ratelimit?.period,
            requests_per_period: group.rule.ratelimit?.requests_per_period,
            mitigation_timeout: group.rule.ratelimit?.mitigation_timeout,
        }),
    );

    send(() => window.axios.post(route('edge.bulk.rules'), { ...rule, targets: missing }));
};

const move = (group, position) => {
    const copy = group.copies[0];

    emit('apply', (all) =>
        eachCopy(all, [copy], (list, at) => {
            const [rule] = list.splice(at, 1);
            list.splice(Math.max(0, position - 1), 0, { ...rule, _pending: true });
        }),
    );

    send(() =>
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

    const copies = copiesOf(group);
    emit('apply', (all) => eachCopy(all, copies, (list, at) => list.splice(at, 1)));

    send(() =>
        window.axios.post(route('edge.bulk.rules.destroy'), {
            phase: props.phase,
            description: group.rule.description,
            copies,
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
            :saving="false"
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
            <li v-for="(group, i) in groups" :key="group.key" class="p-4">
                <EdgeRuleForm
                    v-if="editing === group.key"
                    :phase="phase"
                    :rule="group.rule"
                    :my-ip="myIp"
                    :saving="false"
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
                                <span v-if="group.pending" class="text-xs text-slate-400">Saving…</span>
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
                                        :disabled="group.pending"
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
                                    :disabled="i === 0 || group.pending"
                                    class="disabled:opacity-30"
                                    :aria-label="`Move ${group.rule.description} up`"
                                    @click="move(group, i)"
                                >
                                    ↑
                                </button>
                                <button
                                    type="button"
                                    :class="smallButton"
                                    :disabled="i === groups.length - 1 || group.pending"
                                    class="disabled:opacity-30"
                                    :aria-label="`Move ${group.rule.description} down`"
                                    @click="move(group, i + 2)"
                                >
                                    ↓
                                </button>
                            </template>
                            <button type="button" :class="smallButton" :disabled="group.pending" @click="toggle(group)">
                                {{ group.enabledCount === group.copies.length ? 'Turn off' : 'Turn on' }}
                            </button>
                            <button type="button" :class="smallButton" :disabled="group.pending" @click="start(group.key)">Edit</button>
                            <button
                                type="button"
                                :class="smallButton"
                                class="text-rose-600"
                                :disabled="group.pending"
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
