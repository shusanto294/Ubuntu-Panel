<script setup>
import { computed } from 'vue';
import { Link } from '@inertiajs/vue3';
import { useTaskStream } from '@/Composables/useTaskStream';
import { formatRelative } from '@/time';

/**
 * One line saying what the queue is doing, with a link to the log.
 *
 * The pages that start work used to each carry a full console: a black
 * terminal panel, a step list and a scrolling wall of apt output, on the
 * Dashboard, on Apps, on Settings, on both site pages and both mail pages. It
 * is the right thing to look at when something has gone wrong and the wrong
 * thing to have on screen the rest of the time, which is most of the time.
 *
 * So this is the progress, and the output moved to Logs — one place, in time
 * order, where reading it is what you went there to do. Where the steps
 * themselves are the point, TaskProgress shows those instead.
 */
const props = defineProps({
    task: { type: Object, default: null },
    reloadOnFinish: { type: Boolean, default: true },
    // Keep a finished run on screen. Off for pages that only care about now.
    showFinished: { type: Boolean, default: true },
});

const { state, running, failed, progress } = useTaskStream(() => props.task, {
    reloadOnFinish: props.reloadOnFinish,
});

const visible = computed(() => Boolean(state.value) && (running.value || props.showFinished));

const detail = computed(() => {
    if (!state.value) return '';

    return running.value
        ? state.value.current_step || 'starting…'
        : state.value.message || state.value.status;
});

const when = computed(() =>
    formatRelative(state.value?.finished_at || state.value?.created_at),
);

const dotClass = computed(() => {
    if (running.value) return 'bg-brand-500 animate-pulse';

    return state.value?.status === 'success' ? 'bg-emerald-500' : 'bg-rose-500';
});

const barClass = computed(() =>
    state.value?.status === 'failed' ? 'bg-rose-500' : 'bg-brand-500',
);
</script>

<template>
    <div
        v-if="visible"
        class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
    >
        <div class="flex flex-wrap items-center gap-3 px-5 py-4">
            <span class="h-2.5 w-2.5 shrink-0 rounded-full" :class="dotClass" />

            <div class="min-w-0 flex-1">
                <p class="truncate text-sm font-medium text-slate-900">
                    {{ state.action }}
                    <span v-if="!running" class="font-normal text-slate-400"
                        >· {{ when }}</span
                    >
                </p>
                <p
                    class="truncate text-xs"
                    :class="failed ? 'text-rose-600' : 'text-slate-500'"
                >
                    {{ detail }}
                </p>
            </div>

            <span v-if="running" class="text-xs font-medium text-slate-500"
                >{{ progress }}%</span
            >

            <Link
                :href="route('logs.show', state.id)"
                class="shrink-0 rounded-lg px-3 py-1.5 text-xs font-medium text-brand-600 ring-1 ring-brand-200 transition hover:bg-brand-50"
            >
                View log
            </Link>
        </div>

        <div v-if="running" class="h-1 w-full bg-slate-100">
            <div
                class="h-1 transition-all duration-500"
                :class="barClass"
                :style="{ width: progress + '%' }"
            />
        </div>
    </div>
</template>
