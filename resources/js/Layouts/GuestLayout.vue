<script setup>
import { Link } from '@inertiajs/vue3';

// A path the panel serves from its own root, not a bundled asset: the same
// file is the sidebar's mark and the browser tab's icon, and binding it keeps
// the build from trying to resolve it as a module.
const logo = '/logo.png';
</script>

<template>
    <div
        class="flex min-h-screen flex-col items-center justify-center bg-slate-50 px-4 py-10"
    >
        <Link href="/" class="flex items-center gap-3">
            <img
                :src="logo"
                alt="Ubuntu Panel"
                class="h-11 w-11 object-contain"
            />
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
