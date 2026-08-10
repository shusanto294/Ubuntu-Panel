<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link, router } from '@inertiajs/vue3';

const props = defineProps({
    domains: Array,
    mailConfigured: Boolean,
    requirements: { type: Array, default: () => [] },
    mailHostname: String,
    dnsAccounts: Array,
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

// Covers the gap between the button posting and a worker picking the job up.
// There is no task to watch for the first second or so, and the page that comes
// back from the post is rendered before one exists — so without this the
// progress only appeared on a manual reload, or on another page that happened
// to be watching the same queue.
const awaiting = ref(false);

const busy = computed(
    () =>
        awaiting.value ||
        props.domains.some(
            (domain) =>
                isBusyStatus(domain.status) ||
                domain.accounts.some((account) => isBusyStatus(account.status)),
        ) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['domains', 'activeTask']);

// Rewrites the schema, the mail store, the Postfix maps and the Dovecot
// config. Every statement in that task is idempotent, so it is the same run
// whether the server has never been configured or has lost a piece of it.
const repair = () => {
    awaiting.value = true;

    router.post(
        route('email.repair'),
        {},
        {
            preserveScroll: true,
            onFinish: () => setTimeout(() => (awaiting.value = false), 8000),
        },
    );
};
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
                <div v-if="mailConfigured" class="flex flex-wrap items-center gap-2">
                    <button
                        @click="repair"
                        class="rounded-xl px-3 py-2 text-sm font-medium text-slate-600 ring-1 ring-slate-200 transition hover:bg-slate-50"
                        title="Rewrite the mail schema, store and server configuration"
                    >
                        Repair mail server
                    </button>
                    <Link
                        :href="route('email.domains.create')"
                        class="rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-brand-700"
                    >
                        Add domain
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
