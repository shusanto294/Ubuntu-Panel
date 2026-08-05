<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import UsageGraph from '@/Components/UsageGraph.vue';
import { streamState, subscribe } from '@/stream';

/**
 * CPU, memory and disk for this machine — one graph each.
 *
 * Two sources, on purpose. The headline number is read straight from /proc once
 * a second: the panel runs on the box it reports on, so a reading costs
 * microseconds and no daemon, queue or SSH is involved. The line behind it comes
 * from the minute-by-minute samples the scheduler records, which is the only way
 * to see further back than the page has been open.
 */
const props = defineProps({
    // Rendered with the page so there is never an empty frame.
    initial: { type: Object, default: null },
    // The shortest range, shipped with the page.
    history: { type: Object, default: null },
    ranges: { type: Array, default: () => [] },
    interval: { type: Number, default: 1000 },
});

const metrics = ref(props.initial);
const failed = ref(false);

const range = ref(props.history?.range ?? '1h');
const series = ref(props.history);
const loadingHistory = ref(false);

let liveTimer = null;
let historyTimer = null;
let unsubscribe = null;
let inFlight = false;

// The daemon pushes a reading a second. Polling is what happens when it cannot
// be reached, and it goes slower on purpose: the whole reason for the socket is
// that a reading over HTTP costs a framework boot.
const POLL_INTERVAL = 5000;

const currentRange = computed(
    () => props.ranges.find((r) => r.key === range.value) ?? props.ranges[0] ?? null,
);

const readLive = async () => {
    // Never stack requests if one is slow, and let a hidden tab rest.
    if (inFlight || (typeof document !== 'undefined' && document.hidden)) return;

    inFlight = true;

    try {
        const { data } = await window.axios.get(route('system.metrics'));
        metrics.value = data.metrics;
        failed.value = false;
    } catch (e) {
        failed.value = true;
    } finally {
        inFlight = false;
    }
};

const readHistory = async ({ quiet = false } = {}) => {
    if (typeof document !== 'undefined' && document.hidden) return;

    if (!quiet) loadingHistory.value = true;

    try {
        const { data } = await window.axios.get(route('system.metrics.history'), {
            params: { range: range.value },
        });

        // The user may have moved on while this was in flight.
        if (data.history.range === range.value) {
            series.value = data.history;
        }
    } catch (e) {
        // Leave the last good series on screen rather than blanking the graphs.
    } finally {
        loadingHistory.value = false;
    }
};

/** Refetch no faster than the range's buckets can actually change. */
const scheduleHistory = () => {
    if (historyTimer) clearInterval(historyTimer);

    const every = currentRange.value?.refresh_ms ?? 60000;

    historyTimer = setInterval(() => readHistory({ quiet: true }), every);
};

watch(range, () => {
    readHistory();
    scheduleHistory();
});

const startPolling = () => {
    if (liveTimer) return;

    readLive();
    liveTimer = setInterval(readLive, POLL_INTERVAL);
};

const stopPolling = () => {
    if (liveTimer) {
        clearInterval(liveTimer);
        liveTimer = null;
    }
};

// Follow the socket: poll only while it is not carrying anything.
watch(
    streamState,
    (state) => (state === 'live' ? stopPolling() : startPolling()),
    { immediate: false },
);

onMounted(() => {
    unsubscribe = subscribe('metrics', (reading) => {
        metrics.value = reading;
        failed.value = false;
    });

    // Something on screen immediately, whichever way the data ends up arriving.
    readLive();

    // The page arrived with the default range already in it; only go and get
    // one if it did not.
    if (!series.value) readHistory();

    scheduleHistory();
});

onBeforeUnmount(() => {
    unsubscribe?.();
    stopPolling();
    if (historyTimer) clearInterval(historyTimer);
});

const bytes = (value) => {
    if (!value && value !== 0) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let n = Number(value);
    let i = 0;
    while (n >= 1024 && i < units.length - 1) {
        n /= 1024;
        i++;
    }
    return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
};

const duration = (seconds) => {
    if (!seconds) return '—';
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    if (d) return `${d}d ${h}h`;
    if (h) return `${h}h ${m}m`;
    return `${m}m`;
};

const points = (key) =>
    (series.value?.points ?? []).map((point) => ({ t: point.t, v: point[key] }));

const bucketSeconds = computed(() => series.value?.bucket_seconds ?? 60);

const load = computed(() => metrics.value?.load ?? null);
const cores = computed(() => metrics.value?.cpu?.cores ?? null);

const loadTone = computed(() => {
    if (!load.value || !cores.value) return 'text-slate-600';
    const ratio = load.value[0] / cores.value;
    if (ratio >= 1.5) return 'text-rose-600';
    if (ratio >= 1) return 'text-amber-600';
    return 'text-slate-600';
});
</script>

<template>
    <div>
        <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
            <div class="flex items-center gap-3">
                <h3 class="text-sm font-semibold text-slate-700">Resource usage</h3>
                <span class="flex items-center gap-1.5 text-xs">
                    <span
                        class="h-1.5 w-1.5 rounded-full"
                        :class="failed ? 'bg-rose-500' : 'bg-emerald-500'"
                    />
                    <span :class="failed ? 'text-rose-600' : 'text-emerald-600'">
                        {{
                            failed
                                ? 'not responding'
                                : streamState === 'live'
                                  ? 'live'
                                  : 'live · polling'
                        }}
                    </span>
                </span>
            </div>

            <div
                class="flex items-center rounded-lg border border-slate-200 bg-white p-0.5"
                role="group"
                aria-label="Graph time range"
            >
                <button
                    v-for="option in ranges"
                    :key="option.key"
                    type="button"
                    @click="range = option.key"
                    :aria-pressed="range === option.key"
                    class="rounded-md px-2.5 py-1 text-xs font-medium transition"
                    :class="
                        range === option.key
                            ? 'bg-slate-900 text-white'
                            : 'text-slate-600 hover:bg-slate-100'
                    "
                >
                    {{ option.label }}
                </button>
            </div>
        </div>

        <div class="grid gap-4 lg:grid-cols-3">
            <UsageGraph
                label="CPU"
                color="#4f46e5"
                :points="points('cpu')"
                :current="metrics?.cpu?.usage ?? null"
                :detail="cores ? `${cores} core${cores === 1 ? '' : 's'}` : ''"
                :bucket-seconds="bucketSeconds"
                :loading="loadingHistory"
            />
            <UsageGraph
                label="Memory"
                color="#0d9488"
                :points="points('memory')"
                :current="metrics?.memory?.percent ?? null"
                :detail="
                    metrics
                        ? `${bytes(metrics.memory.used)} of ${bytes(metrics.memory.total)}`
                        : ''
                "
                :bucket-seconds="bucketSeconds"
                :loading="loadingHistory"
            />
            <UsageGraph
                label="Disk"
                color="#ea580c"
                :points="points('disk')"
                :current="metrics?.disk?.percent ?? null"
                :detail="
                    metrics
                        ? `${bytes(metrics.disk.used)} of ${bytes(metrics.disk.total)} · ${bytes(metrics.disk.free)} free`
                        : ''
                "
                :bucket-seconds="bucketSeconds"
                :loading="loadingHistory"
            />
        </div>

        <div
            class="mt-4 flex flex-wrap items-center gap-x-6 gap-y-1 rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 px-5 py-3 text-xs text-slate-500"
        >
            <span>
                Uptime
                <span class="ml-1 font-medium text-slate-700">{{
                    duration(metrics?.uptime_seconds)
                }}</span>
            </span>
            <span v-if="load">
                Load
                <span class="ml-1 font-medium tabular-nums" :class="loadTone">
                    {{ load[0] }} · {{ load[1] }} · {{ load[2] }}
                </span>
            </span>
            <span v-if="metrics?.swap?.total">
                Swap
                <span class="ml-1 font-medium text-slate-700">
                    {{ bytes(metrics.swap.used) }} of {{ bytes(metrics.swap.total) }}
                </span>
            </span>
        </div>
    </div>
</template>
