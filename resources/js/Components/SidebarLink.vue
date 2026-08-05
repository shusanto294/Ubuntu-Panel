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

// The active row is a solid block with a pointer notch on its inner edge,
// which is how WordPress marks the current menu item. Everything else lifts
// only on hover.
const classes = computed(() =>
    props.active
        ? 'bg-orange-500 text-white'
        : 'text-slate-300 hover:bg-slate-800 hover:text-white',
);
</script>

<template>
    <Link
        :href="href"
        :title="collapsed ? label : null"
        class="group relative flex items-center gap-3 py-2.5 text-sm font-medium transition"
        :class="[classes, collapsed ? 'justify-center' : 'px-4']"
    >
        <svg
            class="h-5 w-5 shrink-0"
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

        <span
            v-if="active"
            class="absolute right-0 top-1/2 -mt-2 h-0 w-0 border-y-8 border-r-8 border-y-transparent border-r-slate-100"
            aria-hidden="true"
        />
    </Link>
</template>
