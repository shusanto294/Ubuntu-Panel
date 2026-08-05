<script setup>
import { ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import { Head, Link, router } from '@inertiajs/vue3';

const props = defineProps({
    domains: Array,
    mailConfigured: Boolean,
    mailHostname: String,
    dnsAccounts: Array,
});

const openDomain = ref(props.domains[0]?.id ?? null);
const showDkim = ref(null);

const deleteAccount = (account) => {
    if (confirm(`Delete ${account.address} and all of its mail?`)) {
        router.delete(route('email.accounts.destroy', account.id), {
            preserveScroll: true,
        });
    }
};

const deleteDomain = (domain) => {
    if (
        confirm(
            `Remove ${domain.domain}? Every mailbox on it and all stored mail are deleted.`,
        )
    ) {
        router.delete(route('email.domains.destroy', domain.id), {
            preserveScroll: true,
        });
    }
};

const syncDns = (domain) =>
    router.post(
        route('email.domains.dns', domain.id),
        {},
        { preserveScroll: true },
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

        <div v-else class="space-y-4">
            <div
                v-for="domain in domains"
                :key="domain.id"
                class="rounded-2xl bg-white shadow-sm ring-1 ring-slate-900/5"
            >
                <div
                    class="flex flex-wrap items-start justify-between gap-3 border-b border-slate-200 px-5 py-4"
                >
                    <div>
                        <p class="font-medium text-slate-800">
                            {{ domain.domain }}
                        </p>
                        <p class="text-xs text-slate-500">
                            {{ domain.accounts.length }} mailbox(es) ·
                            {{ domain.mail_hostname }}
                        </p>
                    </div>
                    <div class="flex flex-wrap items-center gap-2">
                        <StatusBadge :status="domain.status" />
                        <Link
                            :href="route('email.accounts.create', domain.id)"
                            class="rounded-xl bg-brand-600 px-3 py-1.5 text-sm font-medium text-white transition hover:bg-brand-700"
                        >
                            Add mailbox
                        </Link>
                        <button
                            v-if="domain.manage_dns"
                            @click="syncDns(domain)"
                            class="rounded-xl px-3 py-1.5 text-sm text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                        >
                            Sync DNS
                        </button>
                        <button
                            @click="
                                openDomain =
                                    openDomain === domain.id ? null : domain.id
                            "
                            class="rounded-xl px-3 py-1.5 text-sm text-slate-700 ring-1 ring-slate-900/10 hover:bg-slate-50"
                        >
                            {{ openDomain === domain.id ? 'Hide' : 'Manage' }}
                        </button>
                        <button
                            @click="deleteDomain(domain)"
                            class="rounded-xl px-3 py-1.5 text-sm text-rose-600 ring-1 ring-rose-200 hover:bg-rose-50"
                        >
                            Remove
                        </button>
                    </div>
                </div>

                <div
                    v-if="domain.last_error"
                    class="border-b border-rose-200 bg-rose-50 px-5 py-2 text-xs text-rose-700"
                >
                    {{ domain.last_error }}
                </div>

                <div v-if="openDomain === domain.id" class="px-5 py-4">
                    <!-- Mailboxes -->
                    <ul class="divide-y divide-slate-100">
                        <li
                            v-for="account in domain.accounts"
                            :key="account.id"
                            class="flex items-center justify-between py-2"
                        >
                            <div>
                                <p class="text-sm text-slate-800">
                                    {{ account.address }}
                                </p>
                                <p class="text-xs text-slate-500">
                                    quota {{ account.quota_mb }} MB
                                    <span
                                        v-if="account.last_error"
                                        class="text-rose-600"
                                        >· {{ account.last_error }}</span
                                    >
                                </p>
                            </div>
                            <div class="flex items-center gap-3">
                                <StatusBadge :status="account.status" />
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
                            class="py-3 text-sm text-slate-500"
                        >
                            No mailboxes yet.
                            <Link
                                :href="route('email.accounts.create', domain.id)"
                                class="text-brand-600 hover:underline"
                                >Add one</Link
                            >.
                        </li>
                    </ul>

                    <!-- Client settings -->
                    <div
                        class="mt-5 rounded-xl bg-slate-50 p-4 text-xs text-slate-600"
                    >
                        <p class="font-semibold text-slate-700">
                            Mail client settings
                        </p>
                        <p class="mt-1">
                            IMAP {{ domain.client_settings.imap.host }}:{{
                                domain.client_settings.imap.port
                            }}
                            ({{ domain.client_settings.imap.security }}) · SMTP
                            {{ domain.client_settings.smtp.host }}:{{
                                domain.client_settings.smtp.port
                            }}
                            ({{ domain.client_settings.smtp.security }})
                        </p>
                        <p class="mt-1">Username is the full email address.</p>
                        <button
                            v-if="domain.dkim_public_key"
                            @click="
                                showDkim =
                                    showDkim === domain.id ? null : domain.id
                            "
                            class="mt-2 text-brand-600 hover:underline"
                        >
                            {{ showDkim === domain.id ? 'Hide' : 'Show' }}
                            DKIM record
                        </button>
                        <pre
                            v-if="showDkim === domain.id"
                            class="mt-2 overflow-x-auto rounded bg-slate-900 p-3 text-slate-200"
                            >{{ domain.dkim_selector }}._domainkey.{{
                                domain.domain
                            }}  TXT
{{ domain.dkim_public_key }}</pre
                        >
                    </div>
                </div>
            </div>

            <div
                v-if="!domains.length"
                class="rounded-2xl border border-dashed border-slate-300 bg-white p-16 text-center text-sm text-slate-500"
            >
                No mail domains yet.
                <Link
                    :href="route('email.domains.create')"
                    class="text-brand-600 hover:underline"
                    >Add one</Link
                >
                to start creating addresses.
            </div>
        </div>
    </AuthenticatedLayout>
</template>
