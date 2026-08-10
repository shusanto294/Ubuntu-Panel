<script setup>
import { computed } from 'vue';
import { Link } from '@inertiajs/vue3';
import { useTaskStream } from '@/Composables/useTaskStream';

/**
 * The steps of one run, as a checklist.
 *
 * For the places where the work *is* the page — creating a site is the one
 * that matters: you press a button and then several minutes of unpacking,
 * composing and certificate-issuing happen somewhere you cannot see. A status
 * line is not enough there; a black terminal is more than anyone asked for.
 * A list of named steps, ticked off as they land, is what both those were
 * trying to be.
 */
const props = defineProps({
    task: { type: Object, default: null },
    title: { type: String, default: 'Setting things up' },
    subtitle: { type: String, default: '' },
});

const { state, running, failed, progress, steps } = useTaskStream(() => props.task);

const currentStep = computed(() => state.value?.current_step || 'starting…');

const headline = computed(() => {
    if (running.value) return props.title;

    return state.value?.status === 'success' ? 'Done' : 'Stopped';
});

const stepIcon = (status) => {
    switch (status) {
        case 'success':
            return '✓';
        case 'failed':
            return '✕';
        case 'running':
            return '●';
        case 'skipped':
            return '–';
        default:
            return '○';
    }
};

const stepClass = (status) => {
    switch (status) {
        case 'success':
            return 'text-emerald-600';
        case 'failed':
            return 'text-rose-600';
        case 'running':
            return 'animate-pulse text-brand-600';
        default:
            return 'text-slate-300';
    }
};
</script>

<template>
    <div
        v-if="state"
        class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
    >
        <div class="flex flex-wrap items-center justify-between gap-3 px-6 py-5">
            <div class="min-w-0">
                <p class="flex items-center gap-2 font-semibold text-slate-900">
                    <span
                        v-if="running"
                        class="h-2.5 w-2.5 animate-pulse rounded-full bg-brand-500"
                    />
                    {{ headline }}
                </p>
                <p
                    class="mt-0.5 truncate text-sm"
                    :class="failed ? 'text-rose-600' : 'text-slate-500'"
                >
                    {{ running ? currentStep : state.message || subtitle }}
                </p>
            </div>

            <div class="flex items-center gap-3">
                <span class="text-sm font-medium text-slate-500">{{ progress }}%</span>
                <Link
                    :href="route('logs.show', state.id)"
                    class="rounded-lg px-3 py-1.5 text-xs font-medium text-brand-600 ring-1 ring-brand-200 transition hover:bg-brand-50"
                >
                    View log
                </Link>
            </div>
        </div>

        <div class="h-1 w-full bg-slate-100">
            <div
                class="h-1 transition-all duration-500"
                :class="failed ? 'bg-rose-500' : running ? 'bg-brand-500' : 'bg-emerald-500'"
                :style="{ width: progress + '%' }"
            />
        </div>

        <ol v-if="steps.length" class="divide-y divide-slate-100 text-sm">
            <li
                v-for="(step, i) in steps"
                :key="i"
                class="flex items-start gap-3 px-6 py-2.5"
            >
                <span
                    class="w-4 shrink-0 text-center leading-6"
                    :class="stepClass(step.status)"
                    >{{ stepIcon(step.status) }}</span
                >
                <span
                    class="leading-6"
                    :class="
                        step.status === 'running'
                            ? 'font-medium text-slate-900'
                            : step.status === 'pending'
                              ? 'text-slate-400'
                              : 'text-slate-700'
                    "
                >
                    {{ step.name }}
                    <span v-if="step.status === 'skipped'" class="text-xs text-slate-400"
                        >(skipped)</span
                    >
                </span>
            </li>
        </ol>

        <p v-else class="px-6 py-4 text-sm text-slate-500">Starting…</p>
    </div>
</template>
