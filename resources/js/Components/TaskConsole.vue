<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { router } from '@inertiajs/vue3';

const props = defineProps({
    // Initial task payload from the server (may be null).
    task: { type: Object, default: null },
    // Poll even if the initial task is already finished (used right after dispatch).
    watchLatest: { type: Boolean, default: false },
    siteId: { type: Number, default: null },
    title: { type: String, default: 'Live output' },
    // Reload the Inertia page when a task finishes so statuses refresh.
    reloadOnFinish: { type: Boolean, default: true },
    // A finished task collapses to a single line instead of leaving a wall of
    // output on the page.
    collapseWhenDone: { type: Boolean, default: true },
});

const state = ref(props.task ? { ...props.task } : null);
const output = ref(props.task?.output ?? '');
const offset = ref(props.task?.output?.length ?? 0);
const polling = ref(false);
const terminal = ref(null);
const stuck = ref(true);

let timer = null;

const running = computed(() => state.value?.status === 'running');
const steps = computed(() => state.value?.steps ?? []);
const progress = computed(() => state.value?.progress ?? 0);

const expanded = ref(false);
const bodyVisible = computed(
    () => running.value || expanded.value || !props.collapseWhenDone,
);

const statusColour = computed(() => {
    switch (state.value?.status) {
        case 'success':
            return 'bg-emerald-500';
        case 'failed':
            return 'bg-rose-500';
        default:
            return 'bg-orange-500';
    }
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
            return 'text-emerald-400';
        case 'failed':
            return 'text-rose-400';
        case 'running':
            return 'text-orange-400 animate-pulse';
        case 'skipped':
            return 'text-slate-500';
        default:
            return 'text-slate-500';
    }
};

// Strip the ANSI bold sequences the runner uses for step headers.
const clean = (text) => text.replace(/\[[0-9;]*m/g, '');

const scrollToBottom = async () => {
    if (!stuck.value) return;
    await nextTick();
    if (terminal.value) {
        terminal.value.scrollTop = terminal.value.scrollHeight;
    }
};

const onScroll = () => {
    if (!terminal.value) return;
    const { scrollTop, scrollHeight, clientHeight } = terminal.value;
    stuck.value = scrollHeight - scrollTop - clientHeight < 40;
};

const fetchTask = async () => {
    if (polling.value) return;
    polling.value = true;

    try {
        let data;

        if (state.value?.id) {
            const response = await window.axios.get(
                route('tasks.show', state.value.id),
                { params: { offset: offset.value } },
            );
            data = response.data;
            output.value += data.output ?? '';
            offset.value = data.offset ?? output.value.length;
        } else {
            const response = await window.axios.get(route('tasks.latest'), {
                params: { site: props.siteId },
            });
            data = response.data;
            if (!data) return;
            output.value = data.output ?? '';
            offset.value = output.value.length;
        }

        const wasRunning = running.value;
        state.value = { ...data, output: undefined };
        await scrollToBottom();

        if (wasRunning && !running.value) {
            stop();
            if (props.reloadOnFinish) {
                router.reload({ preserveScroll: true });
            }
        }
    } catch (e) {
        // A 403/404 means the task is gone — stop hammering the endpoint.
        if (e?.response?.status === 403 || e?.response?.status === 404) {
            stop();
        }
    } finally {
        polling.value = false;
    }
};

const start = () => {
    if (timer) return;
    timer = setInterval(fetchTask, 1500);
};

const stop = () => {
    if (timer) {
        clearInterval(timer);
        timer = null;
    }
};

onMounted(() => {
    scrollToBottom();
    if (running.value || props.watchLatest) {
        fetchTask();
        start();
    }
});

onBeforeUnmount(stop);

watch(
    () => props.task?.id,
    (id) => {
        if (!id || id === state.value?.id) return;
        state.value = { ...props.task };
        output.value = props.task.output ?? '';
        offset.value = output.value.length;
        if (running.value) start();
    },
);

defineExpose({ start, stop, refresh: fetchTask });
</script>

<template>
    <div
        v-if="state"
        class="overflow-hidden rounded-xl border border-slate-800 bg-slate-950"
    >
        <div
            class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-800 px-5 py-3"
        >
            <div class="flex items-center gap-3">
                <span
                    class="h-2.5 w-2.5 rounded-full"
                    :class="[statusColour, running ? 'animate-pulse' : '']"
                />
                <div>
                    <p class="text-sm font-medium text-slate-100">
                        {{ title }} — {{ state.action }}
                    </p>
                    <p class="text-xs text-slate-400">
                        {{
                            running
                                ? state.current_step || 'starting…'
                                : state.message || state.status
                        }}
                    </p>
                </div>
            </div>
            <div class="flex items-center gap-3">
                <button
                    v-if="!running && collapseWhenDone"
                    @click="expanded = !expanded"
                    class="rounded border border-slate-700 px-2 py-0.5 text-xs text-slate-300 hover:bg-slate-800"
                >
                    {{ expanded ? 'Hide' : 'Show' }} output
                </button>
                <span class="text-xs font-medium text-slate-300"
                    >{{ progress }}%</span
                >
                <span
                    v-if="running"
                    class="rounded-full bg-orange-500/15 px-2.5 py-0.5 text-xs font-medium text-orange-400"
                >
                    running
                </span>
                <span
                    v-else
                    class="rounded-full px-2.5 py-0.5 text-xs font-medium"
                    :class="
                        state.status === 'success'
                            ? 'bg-emerald-500/15 text-emerald-400'
                            : 'bg-rose-500/15 text-rose-400'
                    "
                >
                    {{ state.status }}
                </span>
            </div>
        </div>

        <div class="h-1 w-full bg-slate-800">
            <div
                class="h-1 transition-all duration-500"
                :class="statusColour"
                :style="{ width: progress + '%' }"
            />
        </div>

        <div
            v-if="bodyVisible"
            class="grid gap-0 lg:grid-cols-[minmax(0,18rem)_1fr]"
        >
            <ol
                v-if="steps.length"
                class="max-h-96 overflow-y-auto border-b border-slate-800 p-4 text-sm lg:border-b-0 lg:border-r"
            >
                <li
                    v-for="(step, i) in steps"
                    :key="i"
                    class="flex items-start gap-2 py-1"
                >
                    <span
                        class="mt-0.5 w-4 shrink-0 text-center"
                        :class="stepClass(step.status)"
                        >{{ stepIcon(step.status) }}</span
                    >
                    <span
                        class="text-slate-300"
                        :class="
                            step.status === 'running'
                                ? 'font-medium text-white'
                                : ''
                        "
                    >
                        {{ step.name }}
                        <span
                            v-if="step.status === 'skipped'"
                            class="text-xs text-slate-500"
                            >(skipped)</span
                        >
                    </span>
                </li>
            </ol>

            <pre
                ref="terminal"
                @scroll="onScroll"
                class="max-h-96 overflow-auto p-4 font-mono text-xs leading-relaxed text-slate-200"
                >{{ clean(output) || 'Waiting for output…' }}</pre
            >
        </div>

        <div
            v-if="bodyVisible && !stuck && running"
            class="border-t border-slate-800 px-5 py-2 text-center"
        >
            <button
                @click="
                    stuck = true;
                    scrollToBottom();
                "
                class="text-xs text-orange-400 hover:underline"
            >
                Jump to latest output
            </button>
        </div>
    </div>
</template>
