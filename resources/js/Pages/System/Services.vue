<script setup>
import { computed, onBeforeUnmount, onMounted, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import ServiceList from '@/Components/ServiceList.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { Head, router } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    services: { type: Array, default: () => [] },
    activeTask: Object,
    latestTask: Object,
});

// While the install queue is draining, keep the service rows in step.
const preparing = computed(() => props.system.preparing);

let timer = null;

const start = () => {
    if (timer) return;
    // The console streams its own progress; this is only here to move the
    // service rows from `installing` to `installed` as each one lands.
    timer = setInterval(
        () =>
            router.reload({
                only: ['system', 'services', 'activeTask', 'latestTask'],
                preserveScroll: true,
            }),
        10000,
    );
};

const stop = () => {
    if (timer) {
        clearInterval(timer);
        timer = null;
    }
};

onMounted(() => preparing.value && start());
onBeforeUnmount(stop);
watch(preparing, (value) => (value ? start() : stop()));
</script>

<template>
    <Head title="Services" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">Services</h2>
                <p class="text-sm text-slate-500">
                    {{ system.services_installed_count }} of
                    {{ services.length }} installed on {{ system.hostname }}
                    <span v-if="system.services_failed_count" class="text-rose-600"
                        >· {{ system.services_failed_count }} failed</span
                    >
                </p>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskConsole :task="activeTask" title="Working" />
        </div>

        <div v-else-if="latestTask" class="mb-6">
            <TaskConsole :task="latestTask" title="Install output" />
        </div>

        <ServiceList :services="services" />
    </AuthenticatedLayout>
</template>
