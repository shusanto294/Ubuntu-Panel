<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import PanelTerminal from '@/Components/PanelTerminal.vue';
import { Head } from '@inertiajs/vue3';
</script>

<template>
    <Head title="Terminal" />

    <AuthenticatedLayout>
        <template #header>
            <h2 class="text-xl font-semibold text-slate-800">Terminal</h2>
        </template>

        <PanelTerminal :active="true" />

        <p class="mt-2 text-xs text-slate-500">
            A real root login shell on this machine — history, tab completion,
            colours, vim and top all work. The session ends when you leave the
            page.
        </p>
    </AuthenticatedLayout>
</template>
