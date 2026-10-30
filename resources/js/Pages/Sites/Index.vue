<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import Pagination from '@/Components/Pagination.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { usePagination } from '@/Composables/usePagination';
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

// The list keeps its rows current while something is deploying — the run
// itself belongs to the site's own page, which is where the button that
// started it sent you.
useLiveRefresh(busy, ['sites', 'activeTask']);

const { page, pages, total, perPage, visible: visibleSites } = usePagination(() => props.sites);
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

        <!--
            The same line every other page carries, in the same place. This one
            already refreshed itself while a deployment ran and said nothing
            about what was running.
        -->
        <div v-if="activeTask" class="mb-6">
            <TaskStatus :task="activeTask" />
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
                    <tr v-for="site in visibleSites" :key="site.id">
                        <td class="px-5 py-3">
                            <!--
                                The domain goes to the site's own page, not to
                                the site. Leaving the panel is what the Visit
                                button on that page is for; from a list, the
                                thing you want next is nearly always the site's
                                settings.
                            -->
                            <Link
                                :href="route('sites.show', site.id)"
                                class="font-medium text-slate-800 hover:text-brand-600 hover:underline"
                            >
                                {{ site.domain }}
                            </Link>
                            <!--
                                Marked here, not only on the site's own page. A
                                staging copy sitting among a page of live sites
                                is the row somebody hands to a customer, or
                                deletes thinking it is the spare one.
                            -->
                            <span
                                v-if="site.is_staging"
                                class="ms-2 rounded-full bg-amber-100 px-2 py-0.5 text-[11px] font-medium text-amber-800"
                                >staging</span
                            >
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
                        <td class="whitespace-nowrap px-5 py-3 text-right">
                            <!--
                                The way into the application itself, so the
                                thing most often wanted from this list does not
                                need the site's own page first. WordPress goes
                                through the panel, which signs the browser in;
                                the others open their own front door.
                            -->
                            <a
                                v-if="site.type === 'wordpress'"
                                :href="route('sites.wp-admin', site.id)"
                                target="_blank"
                                rel="noopener"
                                class="mr-3 text-brand-600 hover:underline"
                                >WP Admin</a
                            >
                            <a
                                v-else-if="site.managed"
                                :href="site.admin_url"
                                target="_blank"
                                rel="noopener"
                                class="mr-3 text-brand-600 hover:underline"
                                >Open</a
                            >
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
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>
    </AuthenticatedLayout>
</template>
