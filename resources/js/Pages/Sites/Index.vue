<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    sites: Array,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.sites.some((site) => isBusyStatus(site.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['sites', 'activeTask']);
</script>

<template>
    <Head title="Sites" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex items-center justify-between">
                <h2 class="text-xl font-semibold text-slate-800">Sites</h2>
                <Link
                    :href="route('sites.create')"
                    class="rounded-xl bg-brand-600 px-4 py-2 text-sm font-medium text-white hover:bg-brand-700"
                >
                    New site
                </Link>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskConsole :task="activeTask" title="Working" />
        </div>

        <div class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5">
            <table class="min-w-full divide-y divide-slate-200">
                <thead class="bg-slate-50">
                    <tr
                        class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                    >
                        <th class="px-5 py-3">Domain</th>
                        <th class="px-5 py-3">Type</th>
                        <th class="px-5 py-3">Status</th>
                        <th class="px-5 py-3">Runtime</th>
                        <th class="px-5 py-3">SSL</th>
                        <th class="px-5 py-3">DNS</th>
                        <th class="px-5 py-3"></th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-slate-100 text-sm">
                    <tr v-for="site in sites" :key="site.id">
                        <td class="px-5 py-3">
                            <!--
                                The domain is the site, so it links to the site
                                — in a new tab, because leaving the panel to
                                look at a page you just deployed is not what
                                you meant to do. The arrow says as much before
                                you click it.
                            -->
                            <a
                                :href="site.url"
                                target="_blank"
                                rel="noopener"
                                class="group inline-flex items-center gap-1.5 font-medium text-slate-800 hover:text-brand-600"
                                :title="`Open ${site.url} in a new tab`"
                            >
                                <svg
                                    class="h-4 w-4 shrink-0 text-slate-400 group-hover:text-brand-600"
                                    fill="none"
                                    stroke="currentColor"
                                    stroke-width="1.8"
                                    viewBox="0 0 24 24"
                                    aria-hidden="true"
                                >
                                    <path
                                        stroke-linecap="round"
                                        stroke-linejoin="round"
                                        d="M13.5 6H5.25A2.25 2.25 0 003 8.25v10.5A2.25 2.25 0 005.25 21h10.5A2.25 2.25 0 0018 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25"
                                    />
                                </svg>
                                <span>{{ site.domain }}</span>
                            </a>
                            <p
                                v-if="site.aliases.length"
                                class="text-xs text-slate-500"
                            >
                                + {{ site.aliases.join(', ') }}
                            </p>
                        </td>
                        <td class="px-5 py-3 text-slate-600">
                            {{ site.type_label }}
                        </td>
                        <td class="px-5 py-3">
                            <StatusBadge :status="site.status" />
                        </td>
                        <td class="px-5 py-3 text-slate-600">
                            {{
                                site.app_port
                                    ? 'port ' + site.app_port
                                    : 'PHP ' + site.php_version
                            }}
                        </td>
                        <td class="px-5 py-3 text-slate-600">
                            {{ site.ssl ? 'yes' : 'no' }}
                        </td>
                        <td class="px-5 py-3 text-slate-600">
                            {{ site.dns_provider ?? (site.manage_dns ? 'managed' : 'manual') }}
                        </td>
                        <td class="px-5 py-3 text-right">
                            <Link
                                :href="route('sites.show', site.id)"
                                class="text-brand-600 hover:underline"
                                >Manage</Link
                            >
                        </td>
                    </tr>
                    <tr v-if="!sites.length">
                        <td
                            colspan="8"
                            class="px-5 py-10 text-center text-sm text-slate-500"
                        >
                            No sites yet.
                            <Link
                                :href="route('sites.create')"
                                class="text-brand-600 hover:underline"
                                >Create one</Link
                            >.
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>
    </AuthenticatedLayout>
</template>
