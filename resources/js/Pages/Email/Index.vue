<script setup>
import { computed } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskConsole from '@/Components/TaskConsole.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    domains: Array,
    mailConfigured: Boolean,
    mailHostname: String,
    dnsAccounts: Array,
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

const busy = computed(
    () =>
        props.domains.some(
            (domain) =>
                isBusyStatus(domain.status) ||
                domain.accounts.some((account) => isBusyStatus(account.status)),
        ) || props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['domains', 'activeTask']);
</script>

<template>
    <Head title="Email" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div>
                    <h2 class="text-xl font-semibold text-slate-900">Email</h2>
                    <p class="text-sm text-slate-500">
                        {{ domains.length }}
                        {{ domains.length === 1 ? 'domain' : 'domains' }}
                        <span v-if="mailHostname">· {{ mailHostname }}</span>
                    </p>
                </div>
                <Link
                    v-if="mailConfigured"
                    :href="route('email.domains.create')"
                    class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                >
                    Add domain
                </Link>
            </div>
        </template>

        <div
            v-if="!mailConfigured"
            class="rounded-2xl bg-amber-50 p-6 text-sm text-amber-800 ring-1 ring-amber-200"
        >
            <p class="font-medium">The mail server is not installed yet.</p>
            <p class="mt-1">
                Install it from the Services page: Postfix, Dovecot and OpenDKIM
                go on with mailboxes stored in MariaDB, and ports 25, 465, 587,
                993 and 995 are opened.
            </p>
            <Link
                :href="route('services.index')"
                class="mt-3 inline-block rounded-xl bg-slate-900 px-4 py-2.5 text-sm font-medium text-white hover:bg-slate-700"
            >
                Open the Services page
            </Link>
        </div>

        <template v-else>
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
                                <th class="px-5 py-3">Domain</th>
                                <th class="px-5 py-3">Mailboxes</th>
                                <th class="px-5 py-3">Status</th>
                                <th class="px-5 py-3"></th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-slate-100 text-sm">
                            <template v-for="domain in domains" :key="domain.id">
                                <tr>
                                    <td class="px-5 py-3">
                                        <Link
                                            :href="route('email.domains.show', domain.id)"
                                            class="font-medium text-slate-800 hover:text-brand-600"
                                            >{{ domain.domain }}</Link
                                        >
                                        <p class="text-xs text-slate-500">
                                            {{ domain.webmail_host }}
                                        </p>
                                    </td>
                                    <td class="px-5 py-3 text-slate-600">
                                        {{ domain.accounts.length }}
                                    </td>
                                    <td class="px-5 py-3">
                                        <StatusBadge :status="domain.status" />
                                    </td>
                                    <td
                                        class="whitespace-nowrap px-5 py-3 text-right"
                                    >
                                        <a
                                            v-if="roundcubeInstalled"
                                            :href="domain.webmail_url"
                                            target="_blank"
                                            rel="noopener"
                                            class="mr-3 text-brand-600 hover:underline"
                                            >Webmail</a
                                        >
                                        <Link
                                            :href="route('email.domains.show', domain.id)"
                                            class="text-brand-600 hover:underline"
                                            >Manage</Link
                                        >
                                    </td>
                                </tr>
                                <tr v-if="domain.last_error">
                                    <td
                                        colspan="4"
                                        class="bg-rose-50 px-5 py-2 text-xs text-rose-700"
                                    >
                                        {{ domain.last_error }}
                                    </td>
                                </tr>
                            </template>
                            <tr v-if="!domains.length">
                                <td
                                    colspan="4"
                                    class="px-5 py-16 text-center text-sm text-slate-500"
                                >
                                    No mail domains yet.
                                    <Link
                                        :href="route('email.domains.create')"
                                        class="text-brand-600 hover:underline"
                                        >Add one</Link
                                    >
                                    to start creating addresses.
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>
            </div>
        </template>
    </AuthenticatedLayout>
</template>
