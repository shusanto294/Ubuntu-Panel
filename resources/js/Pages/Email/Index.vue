<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import RequiredApps from '@/Components/RequiredApps.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { Head, Link } from '@inertiajs/vue3';

const props = defineProps({
    domains: Array,
    mailConfigured: Boolean,
    requirements: { type: Array, default: () => [] },
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
        ) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['domains', 'activeTask']);

// Two lists, one page. A domain is a thing you set up once — records, DKIM, a
// certificate — and a mailbox is a thing you make every week; reading past the
// first to count the second is the wrong way round.
const tab = ref('domains');

// Every address on the server, with the domain it belongs to. The domains
// already carry their own mailboxes, so this is the same data read the other
// way rather than a second thing to keep in step.
const mailboxes = computed(() =>
    props.domains.flatMap((domain) =>
        (domain.accounts ?? []).map((account) => ({
            ...account,
            domain: domain.domain,
            domain_id: domain.id,
        })),
    ),
);

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
                <!--
                    Test delivery is hidden for now rather than removed: the
                    endpoint and the conversation it holds with the server are
                    still there, and it goes back on the page when it is worth
                    trusting again.
                -->
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
            <div class="mb-6 border-b border-slate-200">
                <nav class="-mb-px flex flex-wrap gap-6" aria-label="Sections">
                    <button
                        @click="tab = 'domains'"
                        class="border-b-2 px-1 pb-3 text-sm font-medium transition"
                        :class="
                            tab === 'domains'
                                ? 'border-brand-600 text-brand-700'
                                : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                        "
                    >
                        Domains
                        <span class="ml-1 text-xs text-slate-400">{{ domains.length }}</span>
                    </button>
                    <button
                        @click="tab = 'mailboxes'"
                        class="border-b-2 px-1 pb-3 text-sm font-medium transition"
                        :class="
                            tab === 'mailboxes'
                                ? 'border-brand-600 text-brand-700'
                                : 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700'
                        "
                    >
                        Mailboxes
                        <span class="ml-1 text-xs text-slate-400">{{ mailboxes.length }}</span>
                    </button>
                </nav>
            </div>

            <div v-show="tab === 'domains'">
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
            </div>

            <!--
                Every address on the server in one list, which is the question
                this page is usually open for: who has a mailbox here. Creating
                one still belongs to its domain, because a mailbox needs a
                domain to be at.
            -->
            <div
                v-show="tab === 'mailboxes'"
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
                                        {{ mailbox.address }}
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
                                            :href="route('email.domains.show', mailbox.domain_id)"
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
