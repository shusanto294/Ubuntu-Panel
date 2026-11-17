<script setup>
import { computed, reactive, ref, watch } from 'vue';
import {
    ACTION_LABEL,
    FIELDS,
    OPERATOR_LABEL,
    PHASE_ACTIONS,
    RATE_PERIODS,
    RATE_TIMEOUTS,
    buildExpression,
    fieldOf,
    newCondition,
    operatorsFor,
    presets,
    seconds,
} from './edge';
import { field } from './ui';
import ZoneScope from './ZoneScope.vue';

/*
 * One custom or rate limiting rule, new or being edited. A new rule starts in
 * the builder — field, operator, value, as many as it needs — which writes the
 * expression; an existing one opens on its expression, because a rule made in
 * Cloudflare's dashboard can say things the builder has no row for.
 */
const props = defineProps({
    phase: { type: String, required: true }, // 'custom' | 'ratelimit'
    rule: { type: Object, default: null },
    myIp: { type: String, default: '' },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    allZones: { type: Array, default: () => [] },
    current: { type: Object, default: null },
    loadingZones: { type: Boolean, default: false },
});

const emit = defineEmits(['save', 'cancel']);

const editing = computed(() => Boolean(props.rule));

const form = reactive({
    description: props.rule?.description ?? '',
    expression: props.rule?.expression ?? '',
    action: props.rule?.action ?? 'block',
    enabled: props.rule?.enabled ?? true,
    period: props.rule?.ratelimit?.period ?? 10,
    requests_per_period: props.rule?.ratelimit?.requests_per_period ?? 10,
    mitigation_timeout: props.rule?.ratelimit?.mitigation_timeout ?? 10,
});

// Builder or the expression itself.
const mode = ref(editing.value ? 'expression' : 'builder');
const joiner = ref('and');
const conditions = ref([newCondition()]);

const built = computed(() => buildExpression(conditions.value, joiner.value));

watch(built, (expression) => {
    if (mode.value === 'builder') form.expression = expression;
});

const useBuilder = () => {
    mode.value = 'builder';
    form.expression = built.value;
};

const setField = (condition, key) => {
    condition.field = key;
    if (!operatorsFor(key).includes(condition.operator)) condition.operator = operatorsFor(key)[0];
};

const addCondition = () => conditions.value.push(newCondition());
const removeCondition = (i) => {
    conditions.value.splice(i, 1);
    if (!conditions.value.length) conditions.value.push(newCondition());
};

const isList = (c) => c.operator === 'in' || c.operator === 'not_in';

// A preset is a starting point, written as an expression to read and change.
const applyPreset = (preset) => {
    mode.value = 'expression';
    form.description = preset.description;
    form.expression = preset.expression;
    form.action = preset.action;
    if (preset.period) {
        form.period = preset.period;
        form.requests_per_period = preset.requests_per_period;
        form.mitigation_timeout = preset.mitigation_timeout;
    }
};

const targets = ref([]);

const presetList = computed(() => (editing.value ? [] : presets(props.phase, props.myIp)));

const submit = () => {
    const payload = {
        phase: props.phase,
        description: form.description,
        expression: form.expression,
        action: form.action,
        enabled: form.enabled,
    };

    if (props.phase === 'ratelimit') {
        Object.assign(payload, {
            period: form.period,
            requests_per_period: form.requests_per_period,
            mitigation_timeout: form.mitigation_timeout,
            // An existing rule keeps what it counts by: the panel counts per
            // address, a rule from the dashboard may count by a header.
            characteristics: editing.value ? 'keep' : '',
        });
    }

    if (!editing.value) payload.targets = targets.value;

    emit('save', payload);
};
</script>

<template>
    <form class="space-y-4 rounded-xl bg-slate-50 p-4 ring-1 ring-slate-200" @submit.prevent="submit">
        <div v-if="presetList.length" class="flex flex-wrap items-center gap-2">
            <span class="text-xs font-medium text-slate-500">Start from</span>
            <button
                v-for="p in presetList"
                :key="p.label"
                type="button"
                class="rounded-lg bg-white px-2.5 py-1 text-xs font-medium text-slate-700 ring-1 ring-slate-200 transition hover:bg-brand-50 hover:ring-brand-200"
                @click="applyPreset(p)"
            >
                {{ p.label }}
            </button>
        </div>

        <div class="grid gap-3 sm:grid-cols-3">
            <label class="block text-sm sm:col-span-2">
                <span class="text-xs font-medium text-slate-700">Name</span>
                <input v-model="form.description" type="text" maxlength="500" required :class="field" placeholder="Block XML-RPC" />
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Then</span>
                <select v-model="form.action" :class="field">
                    <option v-for="a in PHASE_ACTIONS[phase]" :key="a" :value="a">{{ ACTION_LABEL[a] }}</option>
                </select>
            </label>
        </div>

        <!-- Which requests -->
        <div>
            <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="text-xs font-medium text-slate-700">When incoming requests match</span>
                <div class="inline-flex rounded-lg bg-white p-0.5 text-xs ring-1 ring-slate-200">
                    <button
                        type="button"
                        class="rounded-md px-2.5 py-1 font-medium"
                        :class="mode === 'builder' ? 'bg-brand-600 text-white' : 'text-slate-600'"
                        @click="useBuilder"
                    >
                        Builder
                    </button>
                    <button
                        type="button"
                        class="rounded-md px-2.5 py-1 font-medium"
                        :class="mode === 'expression' ? 'bg-brand-600 text-white' : 'text-slate-600'"
                        @click="mode = 'expression'"
                    >
                        Expression
                    </button>
                </div>
            </div>

            <div v-if="mode === 'builder'" class="mt-2 space-y-2">
                <p v-if="editing" class="text-xs text-amber-700">
                    The builder starts empty: saving from it replaces this rule's expression.
                </p>
                <div v-for="(c, i) in conditions" :key="i" class="space-y-2">
                    <div v-if="i > 0" class="flex items-center gap-2">
                        <select v-model="joiner" class="rounded-lg border-slate-300 py-1 text-xs shadow-sm">
                            <option value="and">and</option>
                            <option value="or">or</option>
                        </select>
                    </div>
                    <div class="grid gap-2 sm:grid-cols-[1fr_1fr_2fr_auto]">
                        <select :value="c.field" class="rounded-lg border-slate-300 text-sm shadow-sm" @change="setField(c, $event.target.value)">
                            <option v-for="f in FIELDS" :key="f.key" :value="f.key">{{ f.label }}</option>
                        </select>
                        <select v-model="c.operator" class="rounded-lg border-slate-300 text-sm shadow-sm">
                            <option v-for="o in operatorsFor(c.field)" :key="o" :value="o">{{ OPERATOR_LABEL[o] }}</option>
                        </select>
                        <template v-if="fieldOf(c.field).kind !== 'bool'">
                            <textarea
                                v-if="isList(c)"
                                v-model="c.value"
                                rows="1"
                                class="rounded-lg border-slate-300 font-mono text-sm shadow-sm"
                                :placeholder="`${fieldOf(c.field).placeholder} … (one per line, or commas)`"
                            />
                            <input
                                v-else
                                v-model="c.value"
                                type="text"
                                class="rounded-lg border-slate-300 font-mono text-sm shadow-sm"
                                :placeholder="fieldOf(c.field).placeholder"
                            />
                        </template>
                        <span v-else class="hidden sm:block" />
                        <button
                            type="button"
                            class="rounded-lg px-2 py-1.5 text-xs font-medium text-slate-500 ring-1 ring-slate-200 hover:bg-white hover:text-rose-600"
                            :aria-label="`Remove condition ${i + 1}`"
                            @click="removeCondition(i)"
                        >
                            Remove
                        </button>
                    </div>
                </div>
                <button type="button" class="text-xs font-medium text-brand-600 hover:underline" @click="addCondition">
                    + Another condition
                </button>
            </div>

            <label class="mt-3 block text-sm">
                <span class="text-xs font-medium text-slate-700">
                    Expression
                    <span class="font-normal text-slate-400">
                        — Cloudflare's rule language{{ mode === 'builder' ? ', written by the builder' : '' }}
                    </span>
                </span>
                <textarea
                    v-model="form.expression"
                    rows="3"
                    required
                    :readonly="mode === 'builder'"
                    :class="[field, 'font-mono text-xs', mode === 'builder' ? 'bg-slate-100 text-slate-600' : '']"
                    placeholder='(http.request.uri.path eq "/xmlrpc.php")'
                />
            </label>
            <a
                href="https://developers.cloudflare.com/ruleset-engine/rules-language/"
                target="_blank"
                rel="noopener noreferrer"
                class="mt-1 inline-block text-xs text-slate-500 hover:text-brand-600 hover:underline"
            >
                The rule language's fields and operators ↗
            </a>
        </div>

        <!-- How a rate limit counts -->
        <div v-if="phase === 'ratelimit'" class="grid gap-3 sm:grid-cols-3">
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Requests from one address</span>
                <input v-model.number="form.requests_per_period" type="number" min="1" required :class="field" />
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">In</span>
                <select v-model.number="form.period" :class="field">
                    <option v-for="p in RATE_PERIODS" :key="p" :value="p">{{ seconds(p) }}</option>
                </select>
            </label>
            <label class="block text-sm">
                <span class="text-xs font-medium text-slate-700">Then act for</span>
                <select v-model.number="form.mitigation_timeout" :class="field">
                    <option v-for="t in RATE_TIMEOUTS" :key="t" :value="t">{{ seconds(t) }}</option>
                </select>
            </label>
            <p class="text-xs text-slate-500 sm:col-span-3">
                Counted per address at each Cloudflare data centre. The Free plan allows one rate limiting rule, with
                10 seconds for both the period and the time it acts for.
            </p>
        </div>

        <ZoneScope
            v-if="!editing"
            v-model="targets"
            :zones="allZones"
            :current="current"
            :loading="loadingZones"
            class="border-t border-slate-200 pt-4"
        />

        <p v-if="form.action === 'skip'" class="text-xs text-slate-500">
            A skip rule only skips the custom rules after it — keep it at the top of the list.
        </p>

        <div class="flex flex-wrap items-center justify-between gap-3">
            <label class="inline-flex items-center gap-2 text-sm text-slate-700">
                <input v-model="form.enabled" type="checkbox" class="rounded border-slate-300 text-brand-600" />
                Enabled
            </label>
            <div class="flex gap-2">
                <button
                    type="button"
                    class="rounded-xl bg-white px-4 py-2 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                    @click="emit('cancel')"
                >
                    Cancel
                </button>
                <button
                    type="submit"
                    :disabled="saving || !form.expression"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-brand-700 disabled:opacity-50"
                >
                    {{
                        saving
                            ? 'Saving…'
                            : editing
                              ? 'Save rule'
                              : targets.length > 1
                                ? `Add to ${targets.length} zones`
                                : 'Add rule'
                    }}
                </button>
            </div>
        </div>

        <p v-if="error" class="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 ring-1 ring-rose-200">{{ error }}</p>
    </form>
</template>
