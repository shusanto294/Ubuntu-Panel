<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import { Head } from '@inertiajs/vue3';

/*
 * Stands in for a page the Go port has not reached yet. The route exists so
 * that the navigation renders and `route()` resolves — an unknown name makes
 * the helper throw, which takes the whole page down rather than one link.
 */
defineProps({
    title: String,
});
</script>

<template>
    <Head :title="title" />

    <AuthenticatedLayout>
        <template #header>
            <h1 class="text-xl font-semibold text-gray-900">{{ title }}</h1>
        </template>

        <div class="rounded-lg border border-dashed border-gray-300 bg-white p-8 text-center">
            <h2 class="text-base font-semibold text-gray-900">Not ported yet</h2>

            <p class="mx-auto mt-2 max-w-prose text-sm text-gray-600">
                {{ title }} still lives in the C panel. The Vue page is already here —
                it needs the Go handler that supplies its data.
            </p>
        </div>
    </AuthenticatedLayout>
</template>
