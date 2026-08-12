<script setup>
/*
 * The mark beside a name in the file manager.
 *
 * Drawn rather than fetched, like the rest of the panel's icons: a page that
 * makes no outbound requests works on a machine with no internet, which is
 * most of the reason somebody self-hosts.
 *
 * A folder is a folder. Pictures, video, sound and archives get a glyph for
 * what they are. Everything else is a page with its extension written on it,
 * the way macOS draws a document it has no better idea about — which is how
 * a .php reads as PHP without needing a logo for every language in the world.
 */
import { computed } from 'vue';

const props = defineProps({
    name: { type: String, required: true },
    directory: { type: Boolean, default: false },
    archive: { type: Boolean, default: false },
});

const extension = computed(() => {
    const name = props.name.toLowerCase();

    // .tar.gz reads as one thing, not as a .gz.
    if (name.endsWith('.tar.gz')) return 'tgz';

    const dot = name.lastIndexOf('.');

    // A dotfile is its own name, not an extension: .env is not a file of type
    // "env" so much as a file called .env.
    if (dot <= 0) return '';

    return name.slice(dot + 1);
});

// Families that get a picture instead of their letters.
const GLYPHS = {
    image: 'M4 6h16v12H4zM4 15l4-4 3 3 3-3 6 6M9 9.5a1 1 0 1 1-2 0 1 1 0 0 1 2 0z',
    video: 'M4 6h11v12H4zM15 10l5-3v10l-5-3z',
    audio: 'M9 17V6l10-2v11M9 17a2 2 0 1 1-4 0 2 2 0 0 1 4 0zm10-2a2 2 0 1 1-4 0 2 2 0 0 1 4 0z',
    archive: 'M4 7h16v12H4zM4 7l1.5-3h13L20 7M12 4v9m-1.5-3h3',
    disk: 'M4 7c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7',
    shell: 'M4 5h16v14H4zM7 9l3 3-3 3m5 0h5',
};

const FAMILIES = [
    { glyph: 'image', tint: 'emerald', match: /^(png|jpe?g|gif|svg|webp|ico|avif|bmp|heic)$/ },
    { glyph: 'video', tint: 'violet', match: /^(mp4|mov|webm|mkv|avi|m4v|ogv)$/ },
    { glyph: 'audio', tint: 'rose', match: /^(mp3|wav|ogg|flac|m4a|aac)$/ },
    { glyph: 'archive', tint: 'amber', match: /^(zip|tar|tgz|gz|bz2|xz|rar|7z)$/ },
    { glyph: 'disk', tint: 'sky', match: /^(sql|db|sqlite|sqlite3)$/ },
    { glyph: 'shell', tint: 'slate', match: /^(sh|bash|zsh|fish)$/ },
];

// What colour the page is, by what the file is for. A .php and a .env are both
// pages; they are not the same kind of thing to anybody scanning a directory.
const TINTS = [
    { tint: 'indigo', match: /^(php|phtml)$/ },
    { tint: 'amber', match: /^(js|mjs|cjs|ts|tsx|jsx|vue)$/ },
    { tint: 'orange', match: /^(html?|twig|blade)$/ },
    { tint: 'blue', match: /^(css|scss|sass|less)$/ },
    { tint: 'emerald', match: /^(json|ya?ml|toml|xml|env|ini|conf|cnf|lock)$/ },
    { tint: 'rose', match: /^(pdf)$/ },
    { tint: 'slate', match: /^(md|markdown|txt|log|csv)$/ },
];

const COLOURS = {
    slate: 'text-slate-400',
    sky: 'text-sky-500',
    blue: 'text-blue-500',
    indigo: 'text-indigo-500',
    violet: 'text-violet-500',
    emerald: 'text-emerald-500',
    amber: 'text-amber-500',
    orange: 'text-orange-500',
    rose: 'text-rose-500',
};

const family = computed(() => {
    if (props.directory) return null;
    if (props.archive) return FAMILIES.find((entry) => entry.glyph === 'archive');

    return FAMILIES.find((entry) => entry.match.test(extension.value)) ?? null;
});

const tint = computed(() => {
    if (family.value) return COLOURS[family.value.tint];

    const found = TINTS.find((entry) => entry.match.test(extension.value));

    return COLOURS[found?.tint ?? 'slate'];
});

// Three characters at most: the box is 24 wide and "javascript" does not fit
// in it at any size worth reading.
const label = computed(() => extension.value.slice(0, 3).toUpperCase());
</script>

<template>
    <!-- The folder, filled rather than stroked, so a directory reads first. -->
    <svg
        v-if="directory"
        class="h-5 w-5 shrink-0 text-sky-500"
        viewBox="0 0 24 24"
        fill="currentColor"
        aria-hidden="true"
    >
        <path
            d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4.1a1.5 1.5 0 0 1 1.06.44L11 6.78h8.5A1.5 1.5 0 0 1 21 8.28V17.5a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z"
        />
    </svg>

    <svg
        v-else-if="family"
        class="h-5 w-5 shrink-0"
        :class="tint"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.6"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
    >
        <path :d="GLYPHS[family.glyph]" />
    </svg>

    <!-- A page with its extension on it, folded corner and all. -->
    <svg
        v-else
        class="h-5 w-5 shrink-0"
        :class="tint"
        viewBox="0 0 24 24"
        aria-hidden="true"
    >
        <path
            d="M6 3h8l5 5v13a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linejoin="round"
        />
        <path
            d="M14 3v5h5"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linejoin="round"
        />
        <text
            v-if="label"
            x="12"
            y="17.5"
            text-anchor="middle"
            fill="currentColor"
            stroke="none"
            font-size="6.5"
            font-weight="700"
            font-family="ui-sans-serif, system-ui, sans-serif"
        >
            {{ label }}
        </text>
    </svg>
</template>
