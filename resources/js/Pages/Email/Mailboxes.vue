<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import ReverseDnsNotice from '@/Components/ReverseDnsNotice.vue';
import EmailTabs from '@/Components/EmailTabs.vue';
import Pagination from '@/Components/Pagination.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { usePagination } from '@/Composables/usePagination';
import { Head, Link } from '@inertiajs/vue3';

// Every address on the server, with the domain it belongs to — the question
// these pages are usually open for. Creating one still belongs to a domain,
// because a mailbox needs a domain to be at.
const props = defineProps({
    mailboxes: Array,
    counts: Object,
    mailConfigured: Boolean,
    requirements: { type: Array, default: () => [] },
    mailHostname: String,
    reverseDns: { type: Object, default: null },
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.mailboxes.some((mailbox) => isBusyStatus(mailbox.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['mailboxes', 'counts', 'activeTask']);

// Filtered here rather than asked of the server: the whole list is already on
// the page, and a search that answers as you type is the point of it.
const search = ref('');

const filtered = computed(() => {
    const term = search.value.trim().toLowerCase();
    if (!term) return props.mailboxes;

    return props.mailboxes.filter(
        (item) =>
            (item.address ?? '').toLowerCase().includes(term) ||
            (item.name ?? '').toLowerCase().includes(term) ||
            (item.domain ?? '').toLowerCase().includes(term),
    );
});

const { page, pages, total, perPage, visible: visibleMailboxes } = usePagination(
    filtered,
);
</script>

<template>
    <Head title="Mailboxes" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Email</h2>
                    <p class="text-sm text-slate-500">
                        {{ mailboxes.length }}
                        {{ mailboxes.length === 1 ? 'mailbox' : 'mailboxes' }}
                        <span v-if="mailHostname">· {{ mailHostname }}</span>
                    </p>
                </div>
                <div v-if="mailConfigured" class="flex flex-wrap items-center gap-2">
                    <Link
                        :href="route('email.domains.create')"
                        class="rounded-xl bg-white px-4 py-2.5 text-sm font-medium text-slate-700 ring-1 ring-slate-900/10 shadow-sm transition hover:bg-slate-50"
                    >
                        Add domain
                    </Link>
                    <Link
                        :href="route('email.accounts.new')"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                    >
                        Add mailbox
                    </Link>
                </div>
            </div>
        </template>

        <div v-if="activeTask" class="mb-6">
            <TaskStatus :task="activeTask" />
        </div>

        <RequiredApps
            v-if="!mailConfigured"
            :apps="requirements"
            :need="['mysql', 'mail']"
            note="Postfix, Dovecot and OpenDKIM go on together, with mailboxes stored in MariaDB and ports 25, 465, 587, 993 and 995 opened. This page unlocks itself when they land."
        />

        <template v-else>
            <EmailTabs current="mailboxes" :counts="counts" />

            <ReverseDnsNotice :status="reverseDns" class="mb-6" />

            <input
                v-if="mailboxes.length"
                v-model="search"
                type="search"
                placeholder="Search by address, name or domain"
                class="mb-4 block w-full rounded-lg border-slate-300 text-sm shadow-sm"
            />

            <div
                class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            >
                <div class="overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200">
                        <thead class="bg-slate-50">
                            <tr
                                class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                            >
                                <th class="px-3 py-3 sm:px-5">Address</th>
                                <th class="hidden px-3 py-3 sm:px-5 md:table-cell">Domain</th>
                                <th class="hidden px-3 py-3 sm:px-5 sm:table-cell">Quota</th>
                                <th class="hidden px-3 py-3 sm:px-5 sm:table-cell">Status</th>
                                <th class="px-3 py-3 sm:px-5"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100 text-sm">
                            <template
                                v-for="mailbox in visibleMailboxes"
                                :key="mailbox.id"
                            >
                                <tr>
                                    <td class="break-all px-3 py-3 sm:px-5 font-medium text-slate-800">
                                        <Link
                                            :href="route('email.accounts.show', mailbox.id)"
                                            class="hover:text-brand-600 hover:underline"
                                            >{{ mailbox.address }}</Link
                                        >
                                        <span
                                            v-if="mailbox.name"
                                            class="block text-xs font-normal text-slate-500"
                                            >{{ mailbox.name }}</span
                                        >
                                        <div class="mt-1.5 sm:hidden"><StatusBadge :status="mailbox.status" /></div>
                                    </td>
                                    <td class="hidden px-3 py-3 sm:px-5 md:table-cell">
                                        <Link
                                            :href="route('email.domains.show', mailbox.domain_id)"
                                            class="text-slate-600 hover:text-brand-600 hover:underline"
                                            >{{ mailbox.domain }}</Link
                                        >
                                    </td>
                                    <td class="hidden whitespace-nowrap px-3 py-3 sm:px-5 text-slate-600 sm:table-cell">
                                        {{ mailbox.quota_label }}
                                    </td>
                                    <td class="hidden px-3 py-3 sm:px-5 sm:table-cell">
                                        <StatusBadge :status="mailbox.status" />
                                    </td>
                                    <td class="whitespace-nowrap px-3 py-3 sm:px-5 text-right [&>*]:block [&>*]:py-0.5 sm:[&>*]:inline sm:[&>*]:py-0">
                                        <a
                                            v-if="roundcubeInstalled"
                                            :href="mailbox.webmail_url"
                                            target="_blank"
                                            rel="noopener"
                                            class="text-brand-600 hover:underline sm:mr-3"
                                            >Webmail</a
                                        >
                                        <Link
                                            :href="route('email.accounts.show', mailbox.id)"
                                            class="text-brand-600 hover:underline"
                                            >Manage</Link
                                        >
                                    </td>
                                </tr>
                                <tr v-if="mailbox.last_error">
                                    <td
                                        colspan="5"
                                        class="bg-rose-50 px-3 py-2 sm:px-5 text-xs text-rose-700"
                                    >
                                        {{ mailbox.last_error }}
                                    </td>
                                </tr>
                            </template>
                            <tr v-if="mailboxes.length && !filtered.length">
                                <td
                                    colspan="5"
                                    class="px-5 py-12 text-center text-sm text-slate-500"
                                >
                                    No mailboxes match “{{ search }}”.
                                </td>
                            </tr>
                            <tr v-if="!mailboxes.length">
                                <td
                                    colspan="5"
                                    class="px-5 py-16 text-center text-sm text-slate-500"
                                >
                                    No mailboxes yet.
                                    <Link
                                        :href="route('email.accounts.new')"
                                        class="text-brand-600 hover:underline"
                                        >Add one</Link
                                    >
                                    on any domain.
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
                <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
            </div>
        </template>
    </AuthenticatedLayout>
</template>
