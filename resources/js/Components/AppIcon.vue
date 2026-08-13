<script setup>
/*
 * One mark per catalogue entry, drawn rather than fetched.
 *
 * The panel serves its own assets and makes no outbound requests from a page,
 * so a remote logo would be a broken image on any machine without internet —
 * which is most of the reasons somebody self-hosts. These are simple marks in
 * the same 24-grid and stroke weight as the rest of the interface, not the
 * projects' trademarks.
 *
 * Anything without an entry falls back to a lettered tile, so a catalogue that
 * grows never renders a blank square.
 */
import { computed } from 'vue';

const props = defineProps({
    appKey: { type: String, required: true },
    label: { type: String, default: '' },
    size: { type: String, default: 'md' }, // sm | md | lg
    // The glyph on its own, with no tile behind it and taking the colour of
    // whatever it sits in — for inside a button, where a tinted square next to
    // the label is a second button.
    bare: { type: Boolean, default: false },
});

// Stroked paths on a 24×24 grid. `fill` entries are drawn filled instead.
const MARKS = {
    // core
    base: { path: 'M4 7h16M4 12h16M4 17h10', tint: 'slate' },
    nginx: { path: 'M5 19V7l7 5 7-5v12', tint: 'emerald' },
    php: { path: 'M3 12h18M7 8v8M17 8v8M10 9v6h2a2 2 0 0 0 0-4h-2', tint: 'indigo' },
    composer: { path: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zm0 5.5v7', tint: 'amber' },
    certbot: { path: 'M12 3l7 3v6c0 4-3 7-7 9-4-2-7-5-7-9V6l7-3zm-2.5 9l2 2 3.5-4', tint: 'emerald' },

    // databases
    mysql: { path: 'M4 7c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3', tint: 'sky' },
    postgres: { path: 'M4 7c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7', tint: 'blue' },
    mongodb: { path: 'M12 3c3 4 4.5 7 4.5 10S14 20 12 21c-2-1-4.5-5-4.5-8S9 7 12 3zm0 4v11', tint: 'emerald' },
    phpmyadmin: { path: 'M3 5h18v11H3zM3 20h18M8 9h8M8 12h5', tint: 'orange' },
    redis: { path: 'M3 8l9-4 9 4-9 4-9-4zm0 4l9 4 9-4M3 16l9 4 9-4', tint: 'rose' },

    // runtimes and tools
    node: { path: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zm-2 6v5a2 2 0 0 0 4 0V9', tint: 'emerald' },
    pm2: { path: 'M4 5h16v14H4zM8 9l3 3-3 3m5 0h4', tint: 'violet' },
    wpcli: { path: 'M3 8l3 9 3-7 3 7 3-9M18 8v9', tint: 'slate' },

    // mail
    mail: { path: 'M3 6h18v12H3zM3 7l9 6 9-6', tint: 'sky' },
    roundcube: { path: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zm-4 7h8l-4 4z', tint: 'blue' },

    // site types — the same grid and stroke, so the create form reads as one
    // set rather than as a row of borrowed logos.
    wordpress: {
        path: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM5 8l3.5 9L12 8m0 0l3.5 9L19 8',
        tint: 'blue',
    },
    laravel: { path: 'M3 7l5-3 5 3-5 3-5-3zm5 3v6l5 3 5-3v-6l-5-3', tint: 'rose' },
    nextcloud: {
        path: 'M8.5 9.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5zm7 0a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5zM3 12h2m14 0h2',
        tint: 'sky',
    },
    n8n: {
        path: 'M4 12h3m3 0h4m4 0h2M7 12a1.5 1.5 0 1 0 3 0 1.5 1.5 0 0 0-3 0zm7-4a1.5 1.5 0 1 0 3 0 1.5 1.5 0 0 0-3 0zm0 8a1.5 1.5 0 1 0 3 0 1.5 1.5 0 0 0-3 0zm-4-4l4-4m-4 4l4 4',
        tint: 'rose',
    },
    nodejs: { path: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zm-2 6v5a2 2 0 0 0 4 0V9', tint: 'emerald' },
    nextjs: { path: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM9 16V9l6 8M15 9v4', tint: 'slate' },
    static: { path: 'M4 5h16v12H4zM4 20h16M9 9l-2 2 2 2m6-4l2 2-2 2', tint: 'violet' },
};

// Every tint written out in full: Tailwind scans the source for class names,
// so a string built at runtime is a class that never ships.
const TINTS = {
    slate: 'bg-slate-100 text-slate-600 ring-slate-200',
    sky: 'bg-sky-50 text-sky-600 ring-sky-200',
    blue: 'bg-blue-50 text-blue-600 ring-blue-200',
    indigo: 'bg-indigo-50 text-indigo-600 ring-indigo-200',
    violet: 'bg-violet-50 text-violet-600 ring-violet-200',
    emerald: 'bg-emerald-50 text-emerald-600 ring-emerald-200',
    amber: 'bg-amber-50 text-amber-600 ring-amber-200',
    orange: 'bg-orange-50 text-orange-600 ring-orange-200',
    rose: 'bg-rose-50 text-rose-600 ring-rose-200',
};

const SIZES = {
    sm: 'h-8 w-8 rounded-lg',
    md: 'h-10 w-10 rounded-xl',
    lg: 'h-12 w-12 rounded-2xl',
};

const GLYPH = { sm: 'h-4 w-4', md: 'h-5 w-5', lg: 'h-6 w-6' };

const mark = computed(() => MARKS[props.appKey] || null);

// Stable colour for anything with no mark of its own, so a new catalogue entry
// still looks deliberate.
const fallbackTint = computed(() => {
    const names = Object.keys(TINTS);
    let sum = 0;

    for (const character of props.appKey) sum += character.charCodeAt(0);

    return names[sum % names.length];
});

const tint = computed(() => TINTS[mark.value?.tint || fallbackTint.value]);
const initial = computed(() => (props.label || props.appKey).charAt(0).toUpperCase());
</script>

<template>
    <svg
        v-if="bare && mark"
        :class="GLYPH[size]"
        class="shrink-0"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.7"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
    >
        <path :d="mark.path" />
    </svg>

    <span
        v-else
        class="inline-flex shrink-0 items-center justify-center ring-1 ring-inset"
        :class="[SIZES[size], tint]"
        aria-hidden="true"
    >
        <svg
            v-if="mark"
            :class="GLYPH[size]"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
            stroke-linejoin="round"
        >
            <path :d="mark.path" />
        </svg>
        <span v-else class="text-sm font-semibold">{{ initial }}</span>
    </span>
</template>
