<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import EmailTabs from '@/Components/EmailTabs.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
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
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.mailboxes.some((mailbox) => isBusyStatus(mailbox.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['mailboxes', 'counts', 'activeTask']);
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

            <div
                class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            >
                <div class="overflow-x-auto">
                    <table class="min-w-full divide-y divide-slate-200">
                        <thead class="bg-slate-50">
                            <tr
                                class="text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                            >
                                <th class="px-5 py-3">Address</th>
                                <th class="px-5 py-3">Domain</th>
                                <th class="px-5 py-3">Quota</th>
                                <th class="px-5 py-3">Status</th>
                                <th class="px-5 py-3"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100 text-sm">
                            <template
                                v-for="mailbox in mailboxes"
                                :key="mailbox.id"
                            >
                                <tr>
                                    <td class="break-all px-5 py-3 font-medium text-slate-800">
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
                                    </td>
                                    <td class="px-5 py-3">
                                        <Link
                                            :href="route('email.domains.show', mailbox.domain_id)"
                                            class="text-slate-600 hover:text-brand-600 hover:underline"
                                            >{{ mailbox.domain }}</Link
                                        >
                                    </td>
                                    <td class="whitespace-nowrap px-5 py-3 text-slate-600">
                                        {{ mailbox.quota_label }}
                                    </td>
                                    <td class="px-5 py-3">
                                        <StatusBadge :status="mailbox.status" />
                                    </td>
                                    <td class="whitespace-nowrap px-5 py-3 text-right">
                                        <a
                                            v-if="roundcubeInstalled"
                                            :href="mailbox.webmail_url"
                                            target="_blank"
                                            rel="noopener"
                                            class="mr-3 text-brand-600 hover:underline"
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
                                        class="bg-rose-50 px-5 py-2 text-xs text-rose-700"
                                    >
                                        {{ mailbox.last_error }}
                                    </td>
                                </tr>
                            </template>
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
            </div>
        </template>
    </AuthenticatedLayout>
</template>
