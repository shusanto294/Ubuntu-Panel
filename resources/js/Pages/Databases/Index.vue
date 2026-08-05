<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link, router } from '@inertiajs/vue3';

const props = defineProps({
    databases: Array,
    availableEngines: Array,
    engines: Object,
    // Whether phpMyAdmin is on the machine, so the button only appears when
    // there is somewhere for it to go.
    phpMyAdmin: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.databases.some((database) => isBusyStatus(database.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['databases', 'activeTask']);

const revealed = ref({});

const destroy = (database) => {
    if (confirm(`Drop ${database.name}? All data in it is lost.`)) {
        router.delete(route('databases.destroy', database.id), {
            preserveScroll: true,
        });
    }
};

const reveal = async (database) => {
    const { data } = await window.axios.get(
        route('databases.credentials', database.id),
    );
    revealed.value = { ...revealed.value, [database.id]: data };
};
</script>

<template>
    <Head title="Databases" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Databases</h2>
                    <p class="text-sm text-slate-500">
                        {{ databases.length }}
                        {{ databases.length === 1 ? 'database' : 'databases' }}
                        on this server
                    </p>
                </div>
                <Link
                    :href="route('databases.create')"
                    class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                >
                    New database
                </Link>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskConsole :task="activeTask" title="Working" />
        </div>

        <div
            class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="overflow-x-auto">
                <table class="min-w-full divide-y divide-slate-200">
                    <thead class="bg-slate-50">
                        <tr
                            class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                        >
                            <th class="px-5 py-3">Name</th>
                            <th class="px-5 py-3">Engine</th>
                            <th class="px-5 py-3">Status</th>
                            <th class="px-5 py-3"></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-100 text-sm">
                        <template v-for="database in databases" :key="database.id">
                            <tr>
                                <td class="px-5 py-3">
                                    <p class="font-medium text-slate-800">
                                        {{ database.name }}
                                    </p>
                                    <p class="text-xs text-slate-500">
                                        user: {{ database.username }}
                                        <span
                                            v-if="database.managed_by_site"
                                            class="ml-1 text-brand-600"
                                            >· created for a site</span
                                        >
                                    </p>
                                </td>
                                <td class="px-5 py-3 text-slate-600">
                                    {{ database.engine_label }}
                                </td>
                                <td class="px-5 py-3">
                                    <StatusBadge :status="database.status" />
                                </td>
                                <td class="whitespace-nowrap px-5 py-3 text-right">
                                    <a
                                        v-if="phpMyAdmin && database.engine === 'mysql' && database.status === 'ready'"
                                        :href="route('databases.phpmyadmin', database.id)"
                                        target="_blank"
                                        rel="noopener"
                                        class="mr-3 text-brand-600 hover:underline"
                                        >Manage</a
                                    >
                                    <button
                                        @click="reveal(database)"
                                        class="text-brand-600 hover:underline"
                                    >
                                        Credentials
                                    </button>
                                    <button
                                        @click="destroy(database)"
                                        class="ml-3 text-rose-600 hover:underline"
                                    >
                                        Drop
                                    </button>
                                </td>
                            </tr>
                            <tr v-if="revealed[database.id]">
                                <td colspan="4" class="bg-slate-900 px-5 py-3">
                                    <pre
                                        class="overflow-x-auto font-mono text-xs text-slate-200"
                                        >host: {{ revealed[database.id].host }}
port: {{ revealed[database.id].port }}
database: {{ revealed[database.id].database }}
username: {{ revealed[database.id].username }}
password: {{ revealed[database.id].password }}</pre
                                    >
                                </td>
                            </tr>
                            <tr v-if="database.last_error">
                                <td
                                    colspan="4"
                                    class="bg-rose-50 px-5 py-2 text-xs text-rose-700"
                                >
                                    {{ database.last_error }}
                                </td>
                            </tr>
                        </template>
                        <tr v-if="!databases.length">
                            <td
                                colspan="4"
                                class="px-5 py-16 text-center text-sm text-slate-500"
                            >
                                No databases yet. WordPress and Laravel sites
                                create theirs automatically.
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
