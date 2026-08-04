<script setup>
import { ref } from 'vue';
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import StatusBadge from '@/Components/StatusBadge.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import TextInput from '@/Components/TextInput.vue';
import Checkbox from '@/Components/Checkbox.vue';
import { Head, Link, router, useForm } from '@inertiajs/vue3';

const props = defineProps({
    domains: Array,
    mailConfigured: Boolean,
    mailHostname: String,
    dnsAccounts: Array,
});

const domainForm = useForm({
    domain: '',
    dkim_selector: 'mail',
    manage_dns: props.dnsAccounts.length > 0,
    dns_account_id: props.dnsAccounts[0]?.id ?? null,
});

const openDomain = ref(props.domains[0]?.id ?? null);
const showDkim = ref(null);

// One mailbox form per domain, built up front — useForm must run during setup.
const accountForms = Object.fromEntries(
    props.domains.map((domain) => [
        domain.id,
        useForm({ local_part: '', password: '', quota_mb: 2048 }),
    ]),
);

const formFor = (domainId) => accountForms[domainId];

const submitDomain = () =>
    domainForm.post(route('email.domains.store'), {
        preserveScroll: true,
        onSuccess: () => domainForm.reset('domain'),
    });

const submitAccount = (domain) => {
    const form = formFor(domain.id);
    form.post(route('email.accounts.store', domain.id), {
        preserveScroll: true,
        onSuccess: () => form.reset('local_part', 'password'),
    });
};

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
            <h2 class="text-xl font-semibold text-slate-800">Email</h2>
        </template>

        <div
            v-if="!mailConfigured"
            class="rounded-xl border border-amber-200 bg-amber-50 p-6 text-sm text-amber-800"
        >
            <p class="font-medium">The mail server is not installed yet.</p>
            <p class="mt-1">
                Install it from the Software page: Postfix, Dovecot and OpenDKIM
                go on with mailboxes stored in MariaDB, and ports 25, 465, 587,
                993 and 995 are opened.
            </p>
            <Link
                :href="route('services.index')"
                class="mt-3 inline-block rounded-md bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700"
            >
                Open the Software page
            </Link>
        </div>

        <div v-else class="grid gap-6 lg:grid-cols-3">
            <!-- Add domain -->
            <div class="rounded-xl border border-slate-200 bg-white p-6">
                <h3 class="font-semibold text-slate-800">Add a mail domain</h3>
                <p class="mt-1 text-sm text-slate-500">
                    Generates a DKIM key and, with a DNS provider connected,
                    publishes MX, SPF, DKIM and DMARC records.
                </p>

                <form @submit.prevent="submitDomain" class="mt-4 space-y-4">
                    <div>
                        <InputLabel for="domain" value="Domain" />
                        <TextInput
                            id="domain"
                            v-model="domainForm.domain"
                            class="mt-1 block w-full"
                            placeholder="example.com"
                        />
                        <InputError
                            class="mt-2"
                            :message="domainForm.errors.domain"
                        />
                    </div>

                    <div>
                        <InputLabel
                            for="dkim_selector"
                            value="DKIM selector"
                        />
                        <TextInput
                            id="dkim_selector"
                            v-model="domainForm.dkim_selector"
                            class="mt-1 block w-full"
                        />
                    </div>

                    <label class="flex items-center gap-2">
                        <Checkbox
                            v-model:checked="domainForm.manage_dns"
                            :disabled="!dnsAccounts.length"
                        />
                        <span class="text-sm text-slate-700"
                            >Publish the DNS records for me</span
                        >
                    </label>

                    <div v-if="domainForm.manage_dns">
                        <InputLabel
                            for="dns_account_id"
                            value="DNS provider"
                        />
                        <select
                            id="dns_account_id"
                            v-model="domainForm.dns_account_id"
                            class="mt-1 block w-full rounded-md border-slate-300 shadow-sm focus:border-orange-500 focus:ring-orange-500"
                        >
                            <option
                                v-for="account in dnsAccounts"
                                :key="account.id"
                                :value="account.id"
                            >
                                {{ account.label }} — {{ account.provider_label }}
                            </option>
                        </select>
                        <InputError
                            class="mt-2"
                            :message="domainForm.errors.dns_account_id"
                        />
                    </div>

                    <button
                        :disabled="domainForm.processing"
                        class="w-full rounded-md bg-orange-500 px-4 py-2 text-sm font-medium text-white hover:bg-orange-600 disabled:opacity-50"
                    >
                        Add domain
                    </button>
                </form>

                <p class="mt-4 text-xs text-slate-500">
                    Deliverability also needs a reverse DNS (PTR) record for the
                    server IP pointing at the mail hostname. Most providers
                    expose this in their control panel; the panel cannot set it
                    for you.
                </p>
            </div>

            <!-- Domains -->
            <div class="space-y-4 lg:col-span-2">
                <div
                    v-for="domain in domains"
                    :key="domain.id"
                    class="rounded-xl border border-slate-200 bg-white"
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
                            <button
                                v-if="domain.manage_dns"
                                @click="syncDns(domain)"
                                class="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
                            >
                                Sync DNS
                            </button>
                            <button
                                @click="
                                    openDomain =
                                        openDomain === domain.id
                                            ? null
                                            : domain.id
                                "
                                class="rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
                            >
                                {{
                                    openDomain === domain.id ? 'Hide' : 'Manage'
                                }}
                            </button>
                            <button
                                @click="deleteDomain(domain)"
                                class="rounded-md border border-rose-300 px-3 py-1.5 text-sm text-rose-600 hover:bg-rose-50"
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
                            </li>
                        </ul>

                        <!-- Add mailbox -->
                        <form
                            @submit.prevent="submitAccount(domain)"
                            class="mt-4 grid gap-3 border-t border-slate-100 pt-4 sm:grid-cols-4"
                        >
                            <div class="sm:col-span-2">
                                <InputLabel value="Address" />
                                <div class="mt-1 flex items-center">
                                    <TextInput
                                        v-model="formFor(domain.id).local_part"
                                        class="block w-full rounded-r-none"
                                        placeholder="info"
                                    />
                                    <span
                                        class="rounded-r-md border border-l-0 border-slate-300 bg-slate-50 px-3 py-2 text-sm text-slate-500"
                                        >@{{ domain.domain }}</span
                                    >
                                </div>
                                <InputError
                                    class="mt-1"
                                    :message="
                                        formFor(domain.id).errors.local_part
                                    "
                                />
                            </div>
                            <div>
                                <InputLabel value="Password" />
                                <TextInput
                                    type="password"
                                    v-model="formFor(domain.id).password"
                                    class="mt-1 block w-full"
                                    autocomplete="new-password"
                                />
                                <InputError
                                    class="mt-1"
                                    :message="formFor(domain.id).errors.password"
                                />
                            </div>
                            <div>
                                <InputLabel value="Quota (MB)" />
                                <TextInput
                                    type="number"
                                    v-model="formFor(domain.id).quota_mb"
                                    class="mt-1 block w-full"
                                />
                            </div>
                            <div class="sm:col-span-4">
                                <button
                                    :disabled="formFor(domain.id).processing"
                                    class="rounded-md bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
                                >
                                    Create mailbox
                                </button>
                            </div>
                        </form>

                        <!-- Client settings -->
                        <div
                            class="mt-5 rounded-lg bg-slate-50 p-4 text-xs text-slate-600"
                        >
                            <p class="font-semibold text-slate-700">
                                Mail client settings
                            </p>
                            <p class="mt-1">
                                IMAP {{ domain.client_settings.imap.host }}:{{
                                    domain.client_settings.imap.port
                                }}
                                ({{ domain.client_settings.imap.security }}) ·
                                SMTP {{ domain.client_settings.smtp.host }}:{{
                                    domain.client_settings.smtp.port
                                }}
                                ({{ domain.client_settings.smtp.security }})
                            </p>
                            <p class="mt-1">
                                Username is the full email address.
                            </p>
                            <button
                                v-if="domain.dkim_public_key"
                                @click="
                                    showDkim =
                                        showDkim === domain.id ? null : domain.id
                                "
                                class="mt-2 text-orange-600 hover:underline"
                            >
                                {{
                                    showDkim === domain.id ? 'Hide' : 'Show'
                                }}
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
                    class="rounded-xl border border-dashed border-slate-300 bg-white p-10 text-center text-sm text-slate-500"
                >
                    No mail domains yet. Add one on the left to start creating
                    addresses.
                </div>
            </div>
        </div>
    </AuthenticatedLayout>
</template>
