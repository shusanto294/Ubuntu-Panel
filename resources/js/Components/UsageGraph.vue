<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';

/**
 * One metric over time: a hero number for now, a line for the range.
 *
 * Deliberately one series per card rather than three on shared axes — CPU,
 * memory and disk answer different questions and only share a unit by accident,
 * so stacking them would invite comparisons that do not mean anything.
 */
const props = defineProps({
    label: { type: String, required: true },
    // Hue for this metric, held across every range so the card keeps its identity.
    color: { type: String, default: '#4f46e5' },
    // [{ t: unix seconds, v: 0–100 | null, p?: peak 0–100 | null,
    //    s?: { at, cpu, top: [{ name, command, processes, cpu, finished }] } | null }],
    // oldest first. `s` is the point's busiest second and what was using it.
    points: { type: Array, default: () => [] },
    // The live reading, which is fresher than the newest recorded point.
    current: { type: [Number, null], default: null },
    // Context under the number, e.g. "3.1 GB of 7.8 GB".
    detail: { type: String, default: '' },
    // Seconds a point covers; a wider gap than this means the panel was down.
    bucketSeconds: { type: Number, default: 60 },
    loading: { type: Boolean, default: false },
});

const HEIGHT = 132;
const PAD = { top: 10, right: 8, bottom: 20, left: 30 };

const box = ref(null);
const width = ref(560);
const hover = ref(null);

let observer = null;

onMounted(() => {
    observer = new ResizeObserver(([entry]) => {
        // Drawing into a scaled viewBox would stretch the stroke with it, so
        // the geometry is computed at the real pixel width instead.
        width.value = Math.max(240, Math.round(entry.contentRect.width));
    });
    observer.observe(box.value);
});

onBeforeUnmount(() => observer?.disconnect());

const plot = computed(() => ({
    left: PAD.left,
    right: width.value - PAD.right,
    top: PAD.top,
    bottom: HEIGHT - PAD.bottom,
    width: Math.max(1, width.value - PAD.left - PAD.right),
    height: Math.max(1, HEIGHT - PAD.top - PAD.bottom),
}));

// One line, one value per point: the highest reading inside it. An average
// would flatten a second at 100% into the idle seconds around it, and the line
// would never show what the number above it said.
const series = computed(() => {
    const out = props.points.map((p) => ({
        t: p.t,
        s: p.s ?? null,
        v:
            p.v === null || p.v === undefined
                ? null
                : p.p !== null && p.p !== undefined
                  ? Math.max(p.v, p.p)
                  : p.v,
    }));

    // The live reading is the line's last point, so the line ends where the
    // number above it is now.
    if (props.current !== null && props.current !== undefined) {
        out.push({ t: Math.floor(Date.now() / 1000), v: props.current, live: true });
    }

    return out;
});

const known = computed(() => series.value.filter((p) => p.v !== null && p.v !== undefined));

const span = computed(() => {
    if (series.value.length < 2) return null;

    return {
        from: series.value[0].t,
        to: series.value[series.value.length - 1].t,
    };
});

const x = (t) => {
    if (!span.value || span.value.to === span.value.from) {
        return plot.value.left + plot.value.width;
    }

    const ratio = (t - span.value.from) / (span.value.to - span.value.from);

    return plot.value.left + ratio * plot.value.width;
};

// Always 0–100: a percentage graph that rescales to its own maximum makes a
// quiet machine look as busy as a loaded one.
const y = (v) => plot.value.bottom - (Math.max(0, Math.min(100, v)) / 100) * plot.value.height;

/** Points split into runs, so a stretch with no samples leaves a real gap. */
const runs = computed(() => {
    const out = [];
    let run = [];
    let previous = null;

    for (const point of series.value) {
        const missing = point.v === null || point.v === undefined;
        // The live tip sits up to two buckets past the last recorded one — the
        // current bucket is not written until it ends — which is not a gap.
        const allowed = props.bucketSeconds * (point.live ? 3 : 1.8);
        const jumped = previous !== null && point.t - previous > allowed;

        if (missing || jumped) {
            if (run.length) out.push(run);
            run = [];
        }

        if (!missing) {
            run.push(point);
            previous = point.t;
        }
    }

    if (run.length) out.push(run);

    return out;
});

const linePath = computed(() =>
    runs.value
        .map((run) =>
            run
                .map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`)
                .join(' '),
        )
        .join(' '),
);

const areaPath = computed(() =>
    runs.value
        .filter((run) => run.length > 1)
        .map((run) => {
            const line = run
                .map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`)
                .join(' ');

            return `${line} L${x(run[run.length - 1].t).toFixed(1)},${plot.value.bottom} L${x(run[0].t).toFixed(1)},${plot.value.bottom} Z`;
        })
        .join(' '),
);

const gridlines = [0, 25, 50, 75, 100];

const timeLabels = computed(() => {
    if (!span.value) return [];

    const total = span.value.to - span.value.from;
    const at = (t) => {
        const date = new Date(t * 1000);

        // Anything over a day is read as a date; below that, a clock time.
        return total > 86400
            ? date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
            : date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    };

    return [
        { t: span.value.from, anchor: 'start', text: at(span.value.from) },
        { t: span.value.from + total / 2, anchor: 'middle', text: at(span.value.from + total / 2) },
        { t: span.value.to, anchor: 'end', text: at(span.value.to) },
    ];
});

/** Read aloud instead of the drawing, for anyone not looking at the pixels. */
const description = computed(() =>
    known.value.length
        ? `${props.label} over time, now ${known.value[known.value.length - 1].v.toFixed(0)} percent.`
        : `${props.label}: nothing recorded for this range yet.`,
);

const onMove = (event) => {
    if (!series.value.length || !span.value) return;

    const bounds = event.currentTarget.getBoundingClientRect();
    const offset = event.clientX - bounds.left;

    let nearest = null;
    let distance = Infinity;

    for (const point of known.value) {
        const gap = Math.abs(x(point.t) - offset);

        if (gap < distance) {
            distance = gap;
            nearest = point;
        }
    }

    hover.value = nearest ? { point: nearest, x: x(nearest.t), y: y(nearest.v) } : null;
};

const hoverTime = computed(() =>
    hover.value
        ? new Date(hover.value.point.t * 1000).toLocaleString(undefined, {
              month: 'short',
              day: 'numeric',
              hour: '2-digit',
              minute: '2-digit',
          })
        : '',
);

const spikeTime = computed(() =>
    hover.value?.point.s
        ? new Date(hover.value.point.s.at * 1000).toLocaleTimeString(undefined, {
              hour: '2-digit',
              minute: '2-digit',
              second: '2-digit',
          })
        : '',
);

// Keep the tooltip inside the card rather than letting it run off the edge.
// One with processes in it is wider and taller: it sits below a high point
// and above a low one, so it never covers the line it is about.
const tooltipStyle = computed(() => {
    if (!hover.value) return {};

    const half = hover.value.point.s ? 125 : 56;
    const clamped = Math.max(half, Math.min(width.value - half, hover.value.x));

    if (!hover.value.point.s) {
        return { left: `${clamped}px`, top: `${Math.max(4, hover.value.y - 46)}px` };
    }

    return hover.value.y < HEIGHT / 2
        ? { left: `${clamped}px`, top: `${hover.value.y + 12}px` }
        : { left: `${clamped}px`, top: `${hover.value.y - 12}px`, transform: 'translate(-50%, -100%)' };
});

const displayed = computed(() =>
    props.current !== null && props.current !== undefined
        ? props.current
        : (known.value[known.value.length - 1]?.v ?? null),
);

const tone = computed(() => {
    if (displayed.value === null) return 'text-slate-400';
    if (displayed.value >= 90) return 'text-rose-600';
    if (displayed.value >= 75) return 'text-amber-600';
    return 'text-slate-900';
});
</script>

<template>
    <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5 p-5">
        <div class="flex items-start justify-between gap-3">
            <div>
                <p class="text-sm font-medium text-slate-500">{{ label }}</p>
                <p class="mt-0.5 text-2xl font-semibold tabular-nums" :class="tone">
                    {{ displayed === null ? '—' : displayed.toFixed(1) + '%' }}
                </p>
                <p class="text-xs text-slate-500">{{ detail || '&nbsp;' }}</p>
            </div>
            <span
                class="mt-1 h-2.5 w-2.5 shrink-0 rounded-full"
                :style="{ backgroundColor: color }"
                aria-hidden="true"
            />
        </div>

        <div ref="box" class="relative mt-3">
            <svg
                :width="width"
                :height="HEIGHT"
                :viewBox="`0 0 ${width} ${HEIGHT}`"
                class="block w-full select-none"
                role="img"
                :aria-label="description"
                @mousemove="onMove"
                @mouseleave="hover = null"
            >
                <defs>
                    <linearGradient :id="`fill-${label}`" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="0%" :stop-color="color" stop-opacity="0.22" />
                        <stop offset="100%" :stop-color="color" stop-opacity="0" />
                    </linearGradient>
                </defs>

                <g>
                    <line
                        v-for="value in gridlines"
                        :key="value"
                        :x1="plot.left"
                        :x2="plot.right"
                        :y1="y(value)"
                        :y2="y(value)"
                        stroke="#e2e8f0"
                        stroke-width="1"
                    />
                    <text
                        v-for="value in [0, 50, 100]"
                        :key="`label-${value}`"
                        :x="plot.left - 6"
                        :y="y(value) + 3"
                        text-anchor="end"
                        class="fill-slate-400 text-[9px] tabular-nums"
                    >
                        {{ value }}
                    </text>
                </g>

                <path v-if="areaPath" :d="areaPath" :fill="`url(#fill-${label})`" />
                <path
                    v-if="linePath"
                    :d="linePath"
                    fill="none"
                    :stroke="color"
                    stroke-width="2"
                    stroke-linejoin="round"
                    stroke-linecap="round"
                />

                <g v-if="hover">
                    <line
                        :x1="hover.x"
                        :x2="hover.x"
                        :y1="plot.top"
                        :y2="plot.bottom"
                        stroke="#94a3b8"
                        stroke-width="1"
                        stroke-dasharray="3 3"
                    />
                    <circle
                        :cx="hover.x"
                        :cy="hover.y"
                        r="4.5"
                        :fill="color"
                        stroke="#ffffff"
                        stroke-width="2"
                    />
                </g>

                <text
                    v-for="label_ in timeLabels"
                    :key="label_.text + label_.anchor"
                    :x="x(label_.t)"
                    :y="HEIGHT - 6"
                    :text-anchor="label_.anchor"
                    class="fill-slate-400 text-[9px]"
                >
                    {{ label_.text }}
                </text>
            </svg>

            <div
                v-if="hover"
                class="pointer-events-none absolute z-10 -translate-x-1/2 whitespace-nowrap rounded-md bg-slate-900 px-2 py-1 text-center text-[11px] text-white shadow"
                :style="tooltipStyle"
            >
                <span class="font-semibold tabular-nums">{{ hover.point.v.toFixed(1) }}%</span>
                <span class="block text-[10px] text-slate-300">{{ hover.point.live ? 'now' : hoverTime }}</span>

                <div
                    v-if="hover.point.s && hover.point.s.top?.length"
                    class="mt-1.5 w-[230px] border-t border-white/15 pt-1.5 text-left whitespace-normal"
                >
                    <p class="text-[10px] text-slate-300">
                        Busiest second, {{ spikeTime }}:
                        <span class="font-semibold text-white tabular-nums">{{ hover.point.s.cpu.toFixed(0) }}%</span>
                    </p>
                    <ul class="mt-1 space-y-0.5">
                        <li
                            v-for="process in hover.point.s.top"
                            :key="process.name"
                            class="flex items-baseline justify-between gap-2"
                        >
                            <span class="min-w-0 truncate" :title="process.command">
                                {{ process.name }}
                                <span v-if="process.processes > 1" class="text-slate-400">×{{ process.processes }}</span>
                                <span
                                    v-if="process.finished >= 1 && process.finished >= process.cpu / 2"
                                    class="block text-[10px] text-slate-400"
                                >
                                    incl. programs it ran
                                </span>
                            </span>
                            <span class="shrink-0 font-semibold tabular-nums">{{ process.cpu.toFixed(0) }}%</span>
                        </li>
                    </ul>
                </div>
            </div>

            <div
                v-if="loading"
                class="absolute inset-0 flex items-center justify-center text-xs text-slate-400"
            >
                Loading…
            </div>
            <div
                v-else-if="!known.length"
                class="absolute inset-0 flex items-center justify-center px-4 text-center text-xs text-slate-400"
            >
                Nothing recorded for this range yet.
            </div>
        </div>

    </div>
</template>
