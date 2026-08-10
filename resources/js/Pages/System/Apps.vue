<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import AppCatalog from '@/Components/AppCatalog.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    services: { type: Array, default: () => [] },
    activeTask: Object,
    latestTask: Object,
});

// While the install or removal queue is draining, keep the cards in step. The
// console streams its own progress; this is what moves each app between
// `installing`, `installed` and `not installed` as it lands.
const busy = computed(
    () =>
        props.system.preparing ||
        props.services.some((service) => isBusyStatus(service.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['system', 'services', 'activeTask', 'latestTask']);
</script>

<template>
    <Head title="Apps" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">Apps</h2>
                <p class="text-sm text-slate-500">
                    {{ system.services_installed_count }} of
                    {{ services.length }} installed on {{ system.hostname }}
                    <span v-if="system.services_failed_count" class="text-rose-600"
                        >· {{ system.services_failed_count }} failed</span
                    >
                </p>
            </div>
        </template>

        <div v-if="activeTask ?? latestTask" class="mb-6">
            <TaskStatus :task="activeTask ?? latestTask" />
        </div>

        <AppCatalog :services="services" :active-task="activeTask" />
    </AuthenticatedLayout>
</template>
