<script setup>
import { Link } from '@inertiajs/vue3';
</script>

<template>
    <div
        class="flex min-h-screen flex-col items-center justify-center bg-slate-50 px-4 py-10"
    >
        <Link href="/" class="flex items-center gap-3">
            <span
                class="flex h-11 w-11 items-center justify-center rounded-xl bg-brand-600 font-bold text-white"
            >
                UP
            </span>
            <span class="leading-tight">
                <span class="block font-semibold text-slate-900">Ubuntu Panel</span>
                <span class="block text-xs text-slate-400">Server control</span>
            </span>
        </Link>

        <div
            class="mt-8 w-full rounded-2xl bg-white px-6 py-8 shadow-sm ring-1 ring-slate-900/5 sm:max-w-md sm:px-8"
        >
            <slot />
        </div>
    </div>
</template>
