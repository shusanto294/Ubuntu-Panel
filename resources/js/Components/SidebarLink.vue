<script setup>
import { computed } from 'vue';
import { Link } from '@inertiajs/vue3';

const props = defineProps({
    href: { type: String, required: true },
    icon: { type: String, required: true },
    label: { type: String, required: true },
    active: { type: Boolean, default: false },
    collapsed: { type: Boolean, default: false },
});

// The current row is a tinted pill rather than a filled bar: it reads as
// "you are here" without turning a quarter of the menu into a block of colour.
const classes = computed(() =>
    props.active
        ? 'bg-brand-50 text-brand-700'
        : 'text-slate-500 hover:bg-slate-50 hover:text-slate-900',
);
</script>

<template>
    <Link
        :href="href"
        :title="collapsed ? label : null"
        class="flex items-center gap-3 rounded-xl py-2.5 text-sm font-medium transition"
        :class="[classes, collapsed ? 'justify-center' : 'px-3']"
    >
        <svg
            class="h-5 w-5 shrink-0"
            :class="active ? 'text-brand-600' : 'text-slate-400'"
            xmlns="http://www.w3.org/2000/svg"
            fill="none"
            viewBox="0 0 24 24"
            stroke-width="1.6"
            stroke="currentColor"
            aria-hidden="true"
        >
            <path stroke-linecap="round" stroke-linejoin="round" :d="icon" />
        </svg>

        <span v-if="!collapsed" class="truncate">{{ label }}</span>
    </Link>
</template>
