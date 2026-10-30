<script setup>
import { computed, watchEffect } from 'vue';
import { countryName, countryOf, flagOf, lookUp } from '@/countries';

/*
 * An address with the flag of the country it is in. The flag names its
 * country on hover, and on a tap or click for anybody without a mouse —
 * flags are easy to show and hard to read.
 */
const props = defineProps({
    ip: String,
});

watchEffect(() => lookUp(props.ip));

const code = computed(() => countryOf(props.ip));
</script>

<template>
    <span class="inline-flex items-center gap-1.5">
        <span v-if="code" class="group relative inline-flex">
            <button
                type="button"
                class="cursor-default rounded font-sans text-base leading-none focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
                :aria-label="countryName(code)"
            >
                {{ flagOf(code) }}
            </button>
            <span
                role="tooltip"
                class="pointer-events-none invisible absolute left-full top-1/2 z-30 ml-2 -translate-y-1/2 whitespace-nowrap rounded-md bg-slate-900 px-2 py-1 font-sans text-xs font-medium text-white opacity-0 shadow-lg transition group-focus-within:visible group-focus-within:opacity-100 group-hover:visible group-hover:opacity-100"
            >
                {{ countryName(code) }}
            </span>
        </span>
        <span class="font-mono">{{ ip }}</span>
    </span>
</template>
