<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import UsageCell from '@/Components/UsageCell.vue';

/**
 * CPU, memory and disk for this machine, refreshed every second.
 *
 * The panel runs on the box it is reporting on, so a reading is a /proc read —
 * microseconds, no SSH, no daemon, no queue. Polling once a second is cheaper
 * than the machinery any other arrangement would need.
 */
const props = defineProps({
    // Rendered with the page so there is never an empty frame.
    initial: { type: Object, default: null },
    interval: { type: Number, default: 1000 },
});

const metrics = ref(props.initial);
const failed = ref(false);

let timer = null;
let inFlight = false;

const read = async () => {
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

onMounted(() => {
    read();
    timer = setInterval(read, props.interval);
});

onBeforeUnmount(() => timer && clearInterval(timer));

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

const load = computed(() => metrics.value?.load ?? null);
const cores = computed(() => metrics.value?.cpu?.cores ?? null);

const loadTone = computed(() => {
    if (!load.value || !cores.value) return 'text-slate-500';
    const ratio = load.value[0] / cores.value;
    if (ratio >= 1.5) return 'text-rose-600';
    if (ratio >= 1) return 'text-amber-600';
    return 'text-slate-500';
});
</script>

<template>
    <div class="rounded-xl border border-slate-200 bg-white p-5">
        <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-semibold text-slate-700">Resource usage</h3>
            <div class="flex items-center gap-2 text-xs">
                <span
                    class="h-1.5 w-1.5 rounded-full"
                    :class="failed ? 'bg-rose-500' : 'bg-emerald-500'"
                />
                <span :class="failed ? 'text-rose-600' : 'text-emerald-600'">
                    {{ failed ? 'not responding' : 'live' }}
                </span>
            </div>
        </div>

        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <div>
                <p class="mb-1 text-xs font-medium text-slate-500">CPU</p>
                <UsageCell
                    :percent="metrics?.cpu?.usage ?? null"
                    :detail="cores ? `${cores} core` : ''"
                    :loading="!metrics"
                />
            </div>
            <div>
                <p class="mb-1 text-xs font-medium text-slate-500">Memory</p>
                <UsageCell
                    :percent="metrics?.memory?.percent ?? null"
                    :detail="
                        metrics
                            ? `${bytes(metrics.memory.used)} / ${bytes(metrics.memory.total)}`
                            : ''
                    "
                    :loading="!metrics"
                />
            </div>
            <div>
                <p class="mb-1 text-xs font-medium text-slate-500">Disk</p>
                <UsageCell
                    :percent="metrics?.disk?.percent ?? null"
                    :detail="metrics ? `${bytes(metrics.disk.free)} free` : ''"
                    :loading="!metrics"
                />
            </div>
            <div>
                <p class="mb-1 text-xs font-medium text-slate-500">Uptime</p>
                <p class="text-sm font-semibold text-slate-800">
                    {{ duration(metrics?.uptime_seconds) }}
                </p>
                <p v-if="load" class="mt-1 text-xs" :class="loadTone">
                    load {{ load[0] }} · {{ load[1] }} · {{ load[2] }}
                </p>
                <p
                    v-if="metrics?.swap?.total"
                    class="text-xs text-slate-400"
                >
                    swap {{ bytes(metrics.swap.used) }} of
                    {{ bytes(metrics.swap.total) }}
                </p>
            </div>
        </div>
    </div>
</template>
