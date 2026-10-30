<script setup>
import { computed, ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Pagination from '@/Components/Pagination.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import TaskStatus from '@/Components/TaskStatus.vue';
import { isBusyStatus, useLiveRefresh } from '@/Composables/useLiveRefresh';
import { usePagination } from '@/Composables/usePagination';
import { Head, Link, router } from '@inertiajs/vue3';
import { confirmDanger } from '@/dialog';

const props = defineProps({
    domain: Object,
    roundcubeInstalled: Boolean,
    activeTask: { type: Object, default: null },
});

const showDkim = ref(false);

const busy = computed(
    () =>
        isBusyStatus(props.domain.status) ||
        props.domain.accounts.some((account) => isBusyStatus(account.status)) ||
        props.activeTask?.status === 'running',
);

useLiveRefresh(busy, ['domain', 'activeTask']);

const { page, pages, total, perPage, visible: visibleAccounts } = usePagination(
    () => props.domain.accounts,
);

const retry = () =>
    router.post(
        route('email.domains.retry', props.domain.id),
        {},
        { preserveScroll: true },
    );

const repairAccount = (account) =>
    router.post(
        route('email.accounts.repair', account.id),
        {},
        { preserveScroll: true },
    );

const deleteAccount = async (account) => {
    const go = await confirmDanger(
        `Delete ${account.address}?`,
        'The mailbox and every message stored in it are removed.',
    );

    if (!go) return;

    router.delete(route('email.accounts.destroy', account.id), {
        preserveScroll: true,
    });
};

const deleteDomain = async () => {
    const go = await confirmDanger(
        `Remove ${props.domain.domain}?`,
        'Every mailbox on it and all stored mail are deleted.',
        'Remove',
    );

    if (!go) return;

    router.delete(route('email.domains.destroy', props.domain.id));
};
</script>

<template>
    <Head :title="domain.domain" />

    <AuthenticatedLayout>
        <template #header>
            <div class="flex flex-wrap items-center justify-between gap-3">
                <div class="min-w-0">
                    <Link
                        :href="route('email.domains.index')"
                        class="text-xs text-slate-500 hover:text-slate-900"
                        >&larr; Email</Link
                    >
                    <h2 class="truncate text-xl font-semibold text-slate-900">
                        {{ domain.domain }}
                    </h2>
                </div>
                <div class="flex flex-wrap items-center gap-2">
                    <StatusBadge :status="domain.status" />
                    <!-- A domain that failed had no way back: the row exists, so
                         adding it again is refused, and deleting it to retype the
                         same thing is not a recovery. -->
                    <button
                        v-if="domain.status === 'failed'"
                        @click="retry"
                        class="rounded-xl px-3 py-2 text-sm font-medium text-brand-700 ring-1 ring-brand-200 transition hover:bg-brand-50"
                    >
                        Retry setup
                    </button>
                    <a
                        v-if="roundcubeInstalled"
                        :href="domain.webmail_url"
                        target="_blank"
                        rel="noopener"
                        class="rounded-xl px-3 py-2 text-sm font-medium text-brand-700 ring-1 ring-brand-200 transition hover:bg-brand-50"
                        >Webmail</a
                    >
                    <Link
                        :href="route('email.accounts.create', domain.id)"
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

        <div
            v-if="domain.last_error"
            class="mb-6 rounded-2xl bg-rose-50 px-5 py-4 text-sm text-rose-700 ring-1 ring-rose-200"
        >
            {{ domain.last_error }}
        </div>

        <!-- Mailboxes -->
        <div
            class="overflow-hidden rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
        >
            <div class="border-b border-slate-200 px-5 py-4">
                <h3 class="font-semibold text-slate-900">Mailboxes</h3>
                <p class="text-sm text-slate-500">
                    {{ domain.accounts.length }}
                    {{ domain.accounts.length === 1 ? 'address' : 'addresses' }}
                    on this domain
                </p>
            </div>

            <ul class="divide-y divide-slate-100">
                <li
                    v-for="account in visibleAccounts"
                    :key="account.id"
                    class="flex flex-wrap items-center justify-between gap-3 px-5 py-4"
                >
                    <div class="min-w-0">
                        <p class="text-sm font-medium text-slate-800">
                            <Link
                                :href="route('email.accounts.show', account.id)"
                                class="hover:text-brand-600 hover:underline"
                                >{{ account.address }}</Link
                            >
                        </p>
                        <p class="text-xs text-slate-500">
                            <span v-if="account.name">{{ account.name }} · </span>
                            {{ account.quota_label }}
                            <span v-if="account.last_error" class="text-rose-600"
                                >· {{ account.last_error }}</span
                            >
                        </p>
                    </div>
                    <div class="flex shrink-0 items-center gap-3">
                        <StatusBadge :status="account.status" />
                        <a
                            v-if="roundcubeInstalled"
                            :href="account.webmail_url"
                            target="_blank"
                            rel="noopener"
                            class="text-sm text-brand-600 hover:underline"
                            >Open webmail</a
                        >
                        <!-- Only when there is something to recover from. A
                             mailbox that worked needs no button offering to fix
                             it, and one that failed needs the panel to set it
                             up again from the password it already holds. -->
                        <button
                            v-if="account.status === 'failed'"
                            @click="repairAccount(account)"
                            class="text-sm text-brand-600 hover:underline"
                        >
                            Retry
                        </button>
                        <Link
                            :href="route('email.accounts.show', account.id)"
                            class="text-sm text-brand-600 hover:underline"
                            >Manage</Link
                        >
                        <button
                            @click="deleteAccount(account)"
                            class="text-sm text-rose-600 hover:underline"
                        >
                            Delete
                        </button>
                    </div>
                </li>

                <li
                    v-if="!domain.accounts.length"
                    class="px-5 py-16 text-center text-sm text-slate-500"
                >
                    No mailboxes yet.
                    <Link
                        :href="route('email.accounts.create', domain.id)"
                        class="text-brand-600 hover:underline"
                        >Add one</Link
                    >.
                </li>
            </ul>
            <Pagination v-model="page" :pages="pages" :total="total" :per-page="perPage" />
        </div>

        <!--
            Autoconfiguration first, because for most people it is instead of
            everything below rather than as well as it.
        -->
        <div
            class="mt-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <h3 class="font-semibold text-slate-900">
                Setting up a mail client
            </h3>
            <p class="mt-1 text-sm text-slate-600">
                In Thunderbird — and anything else that reads Mozilla
                autoconfiguration — the address and the password are the whole
                setup. It asks
                <span class="font-mono">{{ domain.autoconfig_host }}</span> for
                the hostnames and ports below and fills them in itself, so
                nobody has to be told which port is which.
            </p>
            <p class="mt-2 text-xs text-slate-500">
                Served from
                <a
                    :href="domain.autoconfig_url"
                    target="_blank"
                    rel="noopener"
                    class="font-mono text-brand-600 hover:underline"
                    >{{ domain.autoconfig_url }}</a
                >. Its A record is published with the MX and the SPF — if the
                lookup fails, set the domain up again from the button above and
                the record is written with the rest.
            </p>
        </div>

        <!-- Connection details, for everything that does not look them up. -->
        <div class="mt-6 grid gap-6 lg:grid-cols-2">
            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Sending (SMTP)</h3>
                <dl class="mt-3 space-y-1 text-sm text-slate-600">
                    <div class="flex justify-between gap-3">
                        <dt>Host</dt>
                        <dd class="font-mono text-slate-800">
                            {{ domain.client_settings.smtp.host }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-3">
                        <dt>Port</dt>
                        <dd class="font-mono text-slate-800">
                            {{ domain.client_settings.smtp.port }} ({{
                                domain.client_settings.smtp.security
                            }})
                        </dd>
                    </div>
                    <div class="flex justify-between gap-3">
                        <dt>or</dt>
                        <dd class="font-mono text-slate-800">
                            {{ domain.client_settings.smtp_ssl.port }} ({{
                                domain.client_settings.smtp_ssl.security
                            }})
                        </dd>
                    </div>
                    <div class="flex justify-between gap-3">
                        <dt>Username</dt>
                        <dd class="text-slate-800">the full email address</dd>
                    </div>
                    <div class="flex justify-between gap-3">
                        <dt>Password</dt>
                        <dd class="text-slate-800">the mailbox password</dd>
                    </div>
                </dl>
                <p class="mt-3 text-xs text-slate-500">
                    Paste these into an application's mailer configuration as
                    they are — the certificate is issued for this hostname, so
                    verification does not need turning off.
                </p>
            </div>

            <div class="rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5">
                <h3 class="font-semibold text-slate-900">Receiving (IMAP)</h3>
                <dl class="mt-3 space-y-1 text-sm text-slate-600">
                    <div class="flex justify-between gap-3">
                        <dt>Host</dt>
                        <dd class="font-mono text-slate-800">
                            {{ domain.client_settings.imap.host }}
                        </dd>
                    </div>
                    <div class="flex justify-between gap-3">
                        <dt>Port</dt>
                        <dd class="font-mono text-slate-800">
                            {{ domain.client_settings.imap.port }} ({{
                                domain.client_settings.imap.security
                            }})
                        </dd>
                    </div>
                    <div class="flex justify-between gap-3">
                        <dt>Webmail</dt>
                        <dd>
                            <a
                                :href="domain.webmail_url"
                                target="_blank"
                                rel="noopener"
                                class="font-mono text-brand-600 hover:underline"
                                >{{ domain.webmail_host }}</a
                            >
                        </dd>
                    </div>
                </dl>

                <button
                    v-if="domain.dkim_public_key"
                    @click="showDkim = !showDkim"
                    class="mt-3 text-sm text-brand-600 hover:underline"
                >
                    {{ showDkim ? 'Hide' : 'Show' }} DKIM record
                </button>
                <pre
                    v-if="showDkim"
                    class="mt-2 overflow-x-auto rounded-xl bg-slate-900 p-3 text-xs text-slate-200"
                    >{{ domain.dkim_selector }}._domainkey.{{ domain.domain }}  TXT
{{ domain.dkim_public_key }}</pre
                >
            </div>
        </div>

        <!-- Removing the domain is last, and on its own. -->
        <div
            class="mt-6 flex flex-wrap items-center justify-between gap-3 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div>
                <h3 class="font-semibold text-slate-900">Remove this domain</h3>
                <p class="text-sm text-slate-500">
                    Every mailbox on it and all stored mail are deleted, along
                    with its DKIM key, its webmail and autoconfig vhosts, and
                    its published DNS records.
                </p>
            </div>
            <button
                @click="deleteDomain"
                class="rounded-xl px-4 py-2.5 text-sm font-medium text-rose-600 ring-1 ring-rose-200 transition hover:bg-rose-50"
            >
                Remove {{ domain.domain }}
            </button>
        </div>
    </AuthenticatedLayout>
</template>
