<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import LiveUsage from '@/Components/LiveUsage.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    metrics: Object,
    counts: Object,
    sites: Array,
    activeTask: Object,
    recentActivity: Array,
});

const installProgress = computed(() =>
    props.counts.services_total
        ? Math.round(
              (props.counts.services_installed / props.counts.services_total) * 100,
          )
        : 0,
);
</script>

<template>
    <Head title="This server" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-800">
                        {{ system.hostname }}
                    </h2>
                    <p class="text-sm text-slate-500">
                        {{ system.os ?? 'Ubuntu' }} · PHP {{ system.php_version }} ·
                        Node {{ system.node_version }}
                    </p>
                </div>
                <div class="flex flex-wrap gap-2">
                    <Link
                        :href="route('sites.create')"
                        class="rounded-md bg-orange-500 px-3 py-2 text-sm font-medium text-white hover:bg-orange-600"
                    >
                        New site
                    </Link>
                    <Link
                        :href="route('services.index')"
                        class="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Software
                    </Link>
                </div>
            </div>
        </template>

        <!-- Something is installing right now -->
        <div
            v-if="system.preparing"
            class="mb-6 rounded-xl border border-orange-200 bg-orange-50 px-5 py-4"
        >
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <p class="text-sm font-medium text-orange-900">
                        Installing software
                    </p>
                    <p class="text-xs text-orange-800">
                        {{ counts.services_installed }} of
                        {{ counts.services_total }} in place ·
                        {{ system.services_pending_count }} in this batch.
                        <Link :href="route('services.index')" class="underline"
                            >See the list</Link
                        >
                    </p>
                </div>
                <span class="text-sm font-medium text-orange-900">
                    {{ installProgress }}%
                </span>
            </div>
            <div class="mt-2 h-1.5 w-full rounded bg-orange-200">
                <div
                    class="h-1.5 rounded bg-orange-500 transition-all duration-500"
                    :style="{ width: installProgress + '%' }"
                />
            </div>
        </div>

        <LiveUsage :initial="metrics" class="mb-6" />

        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Link
                :href="route('sites.index')"
                class="rounded-xl border border-slate-200 bg-white p-5 transition hover:border-orange-300"
            >
                <p class="text-sm text-slate-500">Sites</p>
                <p class="mt-1 text-2xl font-semibold text-slate-900">
                    {{ counts.sites }}
                </p>
            </Link>
            <Link
                :href="route('databases.index')"
                class="rounded-xl border border-slate-200 bg-white p-5 transition hover:border-orange-300"
            >
                <p class="text-sm text-slate-500">Databases</p>
                <p class="mt-1 text-2xl font-semibold text-slate-900">
                    {{ counts.databases }}
                </p>
            </Link>
            <Link
                :href="route('services.index')"
                class="rounded-xl border border-slate-200 bg-white p-5 transition hover:border-orange-300"
            >
                <p class="text-sm text-slate-500">Software</p>
                <p class="mt-1 text-2xl font-semibold text-slate-900">
                    {{ counts.services_installed }}/{{ counts.services_total }}
                </p>
                <p
                    v-if="system.services_failed_count"
                    class="mt-1 text-xs text-rose-600"
                >
                    {{ system.services_failed_count }} failed
                </p>
            </Link>
            <div class="rounded-xl border border-slate-200 bg-white p-5">
                <p class="text-sm text-slate-500">Mail</p>
                <p class="mt-1 text-sm text-slate-800">
                    {{ system.mail_configured ? 'Active' : 'Not installed' }}
                </p>
                <p v-if="system.mail_hostname" class="mt-1 text-xs text-slate-500">
                    {{ system.mail_hostname }}
                </p>
            </div>
        </div>

        <div v-if="activeTask" class="mt-6">
            <TaskConsole :task="activeTask" title="Running now" />
        </div>

        <div class="mt-6 grid gap-6 lg:grid-cols-2">
            <div class="rounded-xl border border-slate-200 bg-white">
                <div
                    class="flex items-center justify-between border-b border-slate-200 px-5 py-4"
                >
                    <h3 class="font-semibold text-slate-800">Recent sites</h3>
                    <Link
                        :href="route('sites.index')"
                        class="text-sm text-orange-600 hover:underline"
                        >All sites</Link
                    >
                </div>
                <ul class="divide-y divide-slate-100 text-sm">
                    <li
                        v-for="site in sites"
                        :key="site.id"
                        class="flex items-center justify-between px-5 py-3"
                    >
                        <Link
                            :href="route('sites.show', site.id)"
                            class="font-medium text-slate-800 hover:text-orange-600"
                            >{{ site.domain }}</Link
                        >
                        <span class="flex items-center gap-3">
                            <span class="text-xs text-slate-500">{{
                                site.type_label
                            }}</span>
                            <StatusBadge :status="site.status" />
                        </span>
                    </li>
                    <li
                        v-if="!sites.length"
                        class="px-5 py-10 text-center text-sm text-slate-500"
                    >
                        Nothing hosted yet.
                        <Link
                            :href="route('sites.create')"
                            class="text-orange-600 hover:underline"
                            >Create a site</Link
                        >.
                    </li>
                </ul>
            </div>

            <div class="rounded-xl border border-slate-200 bg-white">
                <div class="border-b border-slate-200 px-5 py-4">
                    <h3 class="font-semibold text-slate-800">Recent activity</h3>
                </div>
                <ul class="divide-y divide-slate-100 text-sm">
                    <li
                        v-for="log in recentActivity"
                        :key="log.id"
                        class="flex items-start justify-between gap-3 px-5 py-3"
                    >
                        <div class="min-w-0">
                            <p class="font-medium text-slate-800">
                                {{ log.action }}
                            </p>
                            <p class="truncate text-xs text-slate-500">
                                {{ log.message }}
                            </p>
                        </div>
                        <div class="shrink-0 text-right">
                            <StatusBadge :status="log.status" />
                            <p class="mt-1 text-xs text-slate-400">
                                {{ log.created_at }}
                            </p>
                        </div>
                    </li>
                    <li
                        v-if="!recentActivity.length"
                        class="px-5 py-10 text-center text-sm text-slate-500"
                    >
                        Nothing has happened yet.
                    </li>
                </ul>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
