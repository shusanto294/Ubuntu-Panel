<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import { Head, Link } from '@inertiajs/vue3';

defineProps({ sites: Array });
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
                            <p class="font-medium text-slate-800">
                                {{ site.domain }}
                            </p>
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
