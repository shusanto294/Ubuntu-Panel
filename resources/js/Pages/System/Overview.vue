<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import LiveUsage from '@/Components/LiveUsage.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import VersionCard from '@/Components/VersionCard.vue';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    system: Object,
    metrics: Object,
    history: Object,
    historyRanges: Array,
    update: Object,
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
    <Head title="Dashboard" />

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
                        class="rounded-xl bg-brand-600 px-3 py-2 text-sm font-medium text-white hover:bg-brand-700"
                    >
                        New site
                    </Link>
                    <Link
                        :href="route('services.index')"
                        class="rounded-xl bg-white ring-1 ring-slate-900/10 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50"
                    >
                        Software
                    </Link>
                </div>
            </div>
        </template>

        <!-- Something is installing right now -->
        <div
            v-if="system.preparing"
            class="mb-6 rounded-xl border border-brand-200 bg-brand-50 px-5 py-4"
        >
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <p class="text-sm font-medium text-brand-900">
                        Installing software
                    </p>
                    <p class="text-xs text-brand-800">
                        {{ counts.services_installed }} of
                        {{ counts.services_total }} in place ·
                        {{ system.services_pending_count }} in this batch.
                        <Link :href="route('services.index')" class="underline"
                            >See the list</Link
                        >
                    </p>
                </div>
                <span class="text-sm font-medium text-brand-900">
                    {{ installProgress }}%
                </span>
            </div>
            <div class="mt-2 h-1.5 w-full rounded bg-brand-200">
                <div
                    class="h-1.5 rounded bg-brand-500 transition-all duration-500"
                    :style="{ width: installProgress + '%' }"
                />
            </div>
        </div>

        <LiveUsage
            :initial="metrics"
            :history="history"
            :ranges="historyRanges"
            class="mb-6"
        />

        <!--
            The lead tile is filled and the rest are white, which is how the
            eye finds the number that matters first. Each carries a tinted
            icon chip so the row reads as four things rather than four numbers.
        -->
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Link
                :href="route('sites.index')"
                class="rounded-2xl bg-brand-600 p-5 text-white shadow-sm transition hover:bg-brand-700"
            >
                <div class="flex items-center gap-3">
                    <span
                        class="flex h-10 w-10 items-center justify-center rounded-full bg-white/20"
                    >
                        <svg
                            class="h-5 w-5"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.6"
                            viewBox="0 0 24 24"
                        >
                            <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                d="M12 21a9 9 0 100-18 9 9 0 000 18zm0 0c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3 7.5 7.03 7.5 12s2.015 9 4.5 9zM3.6 9h16.8M3.6 15h16.8"
                            />
                        </svg>
                    </span>
                    <div>
                        <p class="text-sm text-white/70">Sites</p>
                        <p class="text-2xl font-semibold">{{ counts.sites }}</p>
                    </div>
                </div>
            </Link>

            <Link
                :href="route('databases.index')"
                class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5 transition hover:shadow-md"
            >
                <div class="flex items-center gap-3">
                    <span
                        class="flex h-10 w-10 items-center justify-center rounded-full bg-brand-50 text-brand-600"
                    >
                        <svg
                            class="h-5 w-5"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.6"
                            viewBox="0 0 24 24"
                        >
                            <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 5.625c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125"
                            />
                        </svg>
                    </span>
                    <div>
                        <p class="text-sm text-slate-500">Databases</p>
                        <p class="text-2xl font-semibold text-slate-900">
                            {{ counts.databases }}
                        </p>
                    </div>
                </div>
            </Link>

            <Link
                :href="route('services.index')"
                class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5 transition hover:shadow-md"
            >
                <div class="flex items-center gap-3">
                    <span
                        class="flex h-10 w-10 items-center justify-center rounded-full"
                        :class="
                            system.services_failed_count
                                ? 'bg-rose-50 text-rose-600'
                                : 'bg-brand-50 text-brand-600'
                        "
                    >
                        <svg
                            class="h-5 w-5"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.6"
                            viewBox="0 0 24 24"
                        >
                            <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                d="M6 6.878V6a2.25 2.25 0 012.25-2.25h7.5A2.25 2.25 0 0118 6v.878M4.5 9.878A2.25 2.25 0 016.75 7.5h10.5A2.25 2.25 0 0119.5 9.878M3 12.75A2.25 2.25 0 015.25 10.5h13.5A2.25 2.25 0 0121 12.75V18a2.25 2.25 0 01-2.25 2.25H5.25A2.25 2.25 0 013 18v-5.25z"
                            />
                        </svg>
                    </span>
                    <div>
                        <p class="text-sm text-slate-500">Software</p>
                        <p class="text-2xl font-semibold text-slate-900">
                            {{ counts.services_installed }}/{{ counts.services_total }}
                        </p>
                    </div>
                </div>
                <p
                    v-if="system.services_failed_count"
                    class="mt-2 text-xs text-rose-600"
                >
                    {{ system.services_failed_count }} failed
                </p>
            </Link>

            <Link
                :href="route('email.index')"
                class="rounded-2xl bg-white p-5 shadow-sm ring-1 ring-slate-900/5 transition hover:shadow-md"
            >
                <div class="flex items-center gap-3">
                    <span
                        class="flex h-10 w-10 items-center justify-center rounded-full"
                        :class="
                            system.mail_configured
                                ? 'bg-emerald-50 text-emerald-600'
                                : 'bg-slate-100 text-slate-400'
                        "
                    >
                        <svg
                            class="h-5 w-5"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.6"
                            viewBox="0 0 24 24"
                        >
                            <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                d="M21.75 6.75v10.5a2.25 2.25 0 01-2.25 2.25h-15a2.25 2.25 0 01-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0019.5 4.5h-15a2.25 2.25 0 00-2.25 2.25m19.5 0v.243a2.25 2.25 0 01-1.07 1.916l-7.5 4.615a2.25 2.25 0 01-2.36 0L3.32 8.91a2.25 2.25 0 01-1.07-1.916V6.75"
                            />
                        </svg>
                    </span>
                    <div class="min-w-0">
                        <p class="text-sm text-slate-500">Mail</p>
                        <p class="text-lg font-semibold text-slate-900">
                            {{ system.mail_configured ? 'Active' : 'Not installed' }}
                        </p>
                    </div>
                </div>
                <p
                    v-if="system.mail_hostname"
                    class="mt-2 truncate text-xs text-slate-500"
                >
                    {{ system.mail_hostname }}
                </p>
            </Link>
        </div>

        <div class="mt-6">
            <VersionCard :update="update" />
        </div>

        <div v-if="activeTask" class="mt-6">
            <TaskConsole :task="activeTask" title="Running now" />
        </div>

        <div class="mt-6 grid gap-6 lg:grid-cols-2">
            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
                <div
                    class="flex items-center justify-between border-b border-slate-200 px-5 py-4"
                >
                    <h3 class="font-semibold text-slate-800">Recent sites</h3>
                    <Link
                        :href="route('sites.index')"
                        class="text-sm text-brand-600 hover:underline"
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
                            class="font-medium text-slate-800 hover:text-brand-600"
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
                            class="text-brand-600 hover:underline"
                            >Create a site</Link
                        >.
                    </li>
                </ul>
            </div>

            <div class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
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
