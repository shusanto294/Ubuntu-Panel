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
            <div class="flex flex-wrap items-center justify-between gap-3">
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
            <div class="overflow-x-auto">
            <table class="min-w-full divide-y divide-slate-200">
                <thead class="bg-slate-50">
                    <tr
                        class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                    >
                        <th class="px-3 py-3 sm:px-5">Domain</th>
                        <th class="hidden px-3 py-3 sm:px-5 md:table-cell">Type</th>
                        <th class="hidden px-3 py-3 sm:px-5 sm:table-cell">Status</th>
                        <th class="hidden px-3 py-3 sm:px-5 lg:table-cell">Runtime</th>
                        <th class="hidden px-3 py-3 sm:px-5 sm:table-cell">SSL</th>
                        <th class="hidden px-3 py-3 sm:px-5 lg:table-cell">DNS</th>
                        <th class="px-3 py-3 sm:px-5"></th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-slate-100 text-sm">
                    <tr v-for="site in visibleSites" :key="site.id">
                        <td class="break-all px-3 py-3 sm:px-5">
                            <!--
                                The domain opens the site itself, in a new tab:
                                it is an address, and an address is clicked to
                                see what is there. The site's settings are the
                                Manage link at the end of the row.
                            -->
                            <a
                                :href="site.url"
                                target="_blank"
                                rel="noopener"
                                class="font-medium text-slate-800 hover:text-brand-600 hover:underline"
                            >
                                {{ site.domain }}
                            </a>
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
                                +
                                <template v-for="(alias, i) in site.aliases" :key="alias">
                                    <a
                                        :href="site.url.replace(site.domain, alias)"
                                        target="_blank"
                                        rel="noopener"
                                        class="hover:text-brand-600 hover:underline"
                                        >{{ alias }}</a
                                    ><template v-if="i < site.aliases.length - 1">, </template>
                                </template>
                            </p>
                            <!-- On a phone the status rides under the name, freeing its column. -->
                            <div class="mt-1.5 sm:hidden"><StatusBadge :status="site.status" /></div>
                        </td>
                        <td class="hidden px-3 py-3 sm:px-5 text-slate-600 md:table-cell">
                            {{ site.type_label }}
                        </td>
                        <td class="hidden px-3 py-3 sm:px-5 sm:table-cell">
                            <StatusBadge :status="site.status" />
                        </td>
                        <td class="hidden px-3 py-3 sm:px-5 text-slate-600 lg:table-cell">
                            {{
                                site.app_port
                                    ? 'port ' + site.app_port
                                    : 'PHP ' + site.php_version
                            }}
                        </td>
                        <td class="hidden px-3 py-3 sm:px-5 text-slate-600 sm:table-cell">
                            {{ site.ssl ? 'yes' : 'no' }}
                        </td>
                        <td class="hidden px-3 py-3 sm:px-5 text-slate-600 lg:table-cell">
                            {{ site.dns_provider ?? (site.manage_dns ? 'managed' : 'manual') }}
                        </td>
                        <td
                            class="whitespace-nowrap px-3 py-3 sm:px-5 text-right [&>*]:block [&>*]:py-0.5 sm:[&>*]:inline sm:[&>*]:py-0"
                        >
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
                                class="text-brand-600 hover:underline sm:mr-3"
                                >WP Admin</a
                            >
                            <a
                                v-else-if="site.managed"
                                :href="site.admin_url"
                                target="_blank"
                                rel="noopener"
                                class="text-brand-600 hover:underline sm:mr-3"
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
            </div>
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>
    </AuthenticatedLayout>
</template>
