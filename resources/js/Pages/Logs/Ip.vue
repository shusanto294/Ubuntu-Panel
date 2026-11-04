<script setup>
import { onMounted } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import IpReport from '@/Components/Logs/IpReport.vue';
import { Head, Link, router } from '@inertiajs/vue3';

/*
 * One address: everything the server has on it. The logs are read after the
 * page is up.
 */
const props = defineProps({
    ip: { type: String, required: true },
    report: { type: Object, default: null },
});

onMounted(() => {
    if (!props.report) router.reload({ only: ['report'], preserveScroll: true, preserveState: true });
});
</script>

<template>
    <Head :title="ip" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex items-center gap-3">
                <Link :href="route('logs.index')" class="text-sm font-medium text-slate-500 hover:text-slate-900">← Logs</Link>
                <h2 class="font-mono text-lg font-semibold text-slate-900">{{ ip }}</h2>
            </div>
        </template>

        <IpReport v-if="report" :report="report" />
        <div v-else class="rounded-2xl bg-white px-5 py-12 text-center text-sm text-slate-500 shadow-sm ring-1 ring-slate-900/5">
            Reading the logs for {{ ip }}…
        </div>
    </AuthenticatedLayout>
</template>
