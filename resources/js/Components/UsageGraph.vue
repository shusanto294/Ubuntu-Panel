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
    // [{ t: unix seconds, v: 0–100 | null, p?: peak 0–100 | null }], oldest first.
    points: { type: Array, default: () => [] },
    // The live reading, which is fresher than the newest recorded point.
    current: { type: [Number, null], default: null },
    // Context under the number, e.g. "3.1 GB of 7.8 GB".
    detail: { type: String, default: '' },
    // Seconds a point covers; a wider gap than this means the panel was down.
    bucketSeconds: { type: Number, default: 60 },
    loading: { type: Boolean, default: false },
    // 'cpu' | 'memory' | 'disk': what the tooltip's breakdown is.
    kind: { type: String, default: '' },
    // CPU / memory: the newest recorded breakdown, for the live tip.
    currentTop: { type: Array, default: null },
    // Disk: { items, loading, error, measuredAt }, measured on first hover.
    disk: { type: Object, default: null },
});

const emit = defineEmits(['hover-start']);

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
        top: p.top ?? null,
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
        out.push({ t: Math.floor(Date.now() / 1000), v: props.current, live: true, top: props.currentTop });
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

    if (nearest && !hover.value) emit('hover-start');
    hover.value = nearest ? { point: nearest, x: x(nearest.t), y: y(nearest.v) } : null;
};

const size = (mb) => (mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`);
const diskSize = (b) => {
    const gb = b / 1024 ** 3;
    return gb >= 1 ? `${gb.toFixed(gb >= 100 ? 0 : 1)} GB` : `${Math.round(b / 1024 ** 2)} MB`;
};

/**
 * What the tooltip lists under the figure: the processes and sites that used
 * the CPU or memory in that minute, or what is using the disk now.
 */
const breakdown = computed(() => {
    if (!hover.value) return null;
    const point = hover.value.point;

    if (props.kind === 'disk') {
        const d = props.disk;
        if (!d || (d.loading && !d.items)) return { heading: 'Using the disk now', note: 'Measuring…', rows: [] };
        if (d.error && !d.items) return { heading: 'Using the disk now', note: d.error, rows: [] };
        return {
            heading: 'Using the disk now',
            rows: (d.items ?? []).map((i) => ({ name: i.name, site: i.site, value: diskSize(i.bytes), pct: i.pct })),
        };
    }

    if (props.kind !== 'cpu' && props.kind !== 'memory') return null;

    const top = point.top;
    let heading;
    if (top?.some((i) => i.spike)) heading = props.bucketSeconds > 60 ? 'At its busiest second' : 'At the busiest second';
    else if (point.live) heading = 'In the last recorded minute';
    else if (props.bucketSeconds > 60) heading = props.kind === 'cpu' ? 'In its busiest minute' : 'At its fullest minute';
    else heading = props.kind === 'cpu' ? 'Over this minute' : 'At this minute';

    if (!top?.length) return { heading, note: 'Nothing recorded for this point.', rows: [] };

    return {
        heading,
        rows: top.map((i) => ({
            name: i.name,
            site: i.site,
            value: props.kind === 'memory' ? size(i.mb ?? 0) : `${i.pct.toFixed(1)}%`,
            pct: props.kind === 'memory' ? i.pct : null,
            count: i.n > 1 ? i.n : 0,
        })),
    };
});

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

// Keep the tooltip inside the card rather than letting it run off the edge.
// It sits below a high point and above a low one, so it never covers the line
// it is about.
const tooltipStyle = computed(() => {
    if (!hover.value) return {};

    const half = breakdown.value ? 116 : 50;
    const clamped = Math.max(half, Math.min(width.value - half, hover.value.x));

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
                class="pointer-events-none absolute z-10 -translate-x-1/2 rounded-md bg-slate-900 px-2 py-1 text-[11px] text-white shadow"
                :class="breakdown ? 'w-[232px] text-left' : 'whitespace-nowrap text-center'"
                :style="tooltipStyle"
            >
                <span class="font-semibold tabular-nums">{{ hover.point.v.toFixed(1) }}%</span>
                <span :class="breakdown ? 'ml-1.5 text-[10px] text-slate-300' : 'block text-[10px] text-slate-300'">
                    {{ hover.point.live ? 'now' : hoverTime }}
                </span>

                <div v-if="breakdown" class="mt-1 border-t border-white/15 pt-1">
                    <p class="text-[10px] text-slate-400">{{ breakdown.heading }}</p>
                    <p v-if="breakdown.note" class="text-[10px] text-slate-300">{{ breakdown.note }}</p>
                    <div
                        v-for="row in breakdown.rows"
                        :key="row.name"
                        class="flex items-baseline justify-between gap-2 leading-snug"
                    >
                        <span class="min-w-0 truncate" :class="row.site ? 'text-sky-300' : ''">
                            {{ row.name }}<span v-if="row.count" class="text-slate-400"> ×{{ row.count }}</span>
                        </span>
                        <span class="shrink-0 tabular-nums text-slate-200">
                            {{ row.value }}<span v-if="row.pct !== null && row.pct !== undefined" class="text-slate-400"> · {{ row.pct.toFixed(0) }}%</span>
                        </span>
                    </div>
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
