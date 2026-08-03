<script setup>
import { computed, onBeforeUnmount, onMounted, watch } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import ServiceList from '@/Components/ServiceList.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { Head, router } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    services: Array,
    activeTask: Object,
    latestTask: Object,
});

const preparing = computed(() => props.system.preparing);

// While the queue is draining, keep the list and its statuses in step.
let timer = null;

const start = () => {
    if (timer) return;
    timer = setInterval(
        () =>
            router.reload({
                only: ['system', 'services', 'activeTask', 'latestTask'],
                preserveScroll: true,
            }),
        4000,
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
    <Head title="Software" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-800">Software</h2>
                <p class="text-sm text-slate-500">
                    Everything the panel can install on {{ system.hostname }} —
                    {{ system.services_installed_count }} in place
                    <span v-if="system.services_failed_count" class="text-rose-600"
                        >· {{ system.services_failed_count }} failed</span
                    >
                </p>
            </div>
        </template>

        <div v-if="activeTask || latestTask" class="mb-6">
            <TaskConsole
                :task="activeTask ?? latestTask"
                title="Install output"
            />
        </div>

        <ServiceList :services="services" />
    </AuthenticatedLayout>
</template>
