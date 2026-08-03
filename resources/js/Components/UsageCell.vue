<script setup>
import { computed } from 'vue';

/** One usage bar in the server list: value, context and a bar, or a skeleton. */
const props = defineProps({
    percent: { type: [Number, null], default: null },
    detail: { type: String, default: '' },
    loading: { type: Boolean, default: false },
    // Sample too old to be trusted: shown, but visibly faded.
    stale: { type: Boolean, default: false },
});

const value = computed(() =>
    props.percent === null || props.percent === undefined
        ? null
        : Math.max(0, Math.min(100, Number(props.percent))),
);

const tone = computed(() => {
    if (value.value === null) return 'bg-slate-200';
    if (value.value >= 90) return 'bg-rose-500';
    if (value.value >= 75) return 'bg-amber-500';
    return 'bg-emerald-500';
});
</script>

<template>
    <div v-if="loading" class="animate-pulse">
        <div class="flex items-center justify-between gap-2">
            <div class="h-3 w-8 rounded bg-slate-100" />
            <div class="h-3 w-14 rounded bg-slate-50" />
        </div>
        <div class="mt-1 h-1.5 w-full rounded-full bg-slate-100" />
    </div>

    <div v-else :class="stale ? 'opacity-50' : ''">
        <div class="flex items-baseline justify-between gap-2 text-xs">
            <span class="tabular-nums text-slate-700">
                {{ value === null ? '—' : value.toFixed(0) + '%' }}
            </span>
            <span class="text-slate-400">{{ detail }}</span>
        </div>
        <div class="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-slate-100">
            <div
                class="h-full rounded-full transition-all duration-700"
                :class="tone"
                :style="{ width: (value ?? 0) + '%' }"
            />
        </div>
    </div>
</template>
