<script setup>
import AuthenticatedLayout from '@/Layouts/AuthenticatedLayout.vue';
import Checkbox from '@/Components/Checkbox.vue';
import InputError from '@/Components/InputError.vue';
import InputLabel from '@/Components/InputLabel.vue';
import PrimaryButton from '@/Components/PrimaryButton.vue';
import TextInput from '@/Components/TextInput.vue';
import { Head, Link, useForm } from '@inertiajs/vue3';

const props = defineProps({
    mailConfigured: Boolean,
    dnsAccounts: { type: Array, default: () => [] },
});

const form = useForm({
    domain: '',
    dkim_selector: 'mail',
    manage_dns: props.dnsAccounts.length > 0,
    dns_account_id: props.dnsAccounts[0]?.id ?? null,
});

const submit = () => form.post(route('email.domains.store'));
</script>

<template>
    <Head title="Add a mail domain" />

    <AuthenticatedLayout>
        <template #header>
            <div>
                <h2 class="text-xl font-semibold text-slate-900">
                    Add a mail domain
                </h2>
                <p class="text-sm text-slate-500">
                    Generates a DKIM key and, with a DNS provider connected,
                    publishes MX, SPF, DKIM and DMARC records.
                </p>
            </div>
        </template>

        <div
            v-if="!mailConfigured"
            class="mb-6 max-w-2xl rounded-2xl bg-amber-50 p-6 text-sm text-amber-800 ring-1 ring-amber-200"
        >
            <p class="font-medium">The mail server is not installed yet.</p>
            <p class="mt-1">
                Add it from the Services page first — nothing here can work
                until Postfix and Dovecot are on the machine.
            </p>
        </div>

        <form
            @submit.prevent="submit"
            class="max-w-2xl space-y-6 rounded-2xl bg-white p-6 shadow-sm ring-1 ring-slate-900/5"
        >
            <div>
                <InputLabel for="domain" value="Domain" />
                <TextInput
                    id="domain"
                    v-model="form.domain"
                    class="mt-1 block w-full"
                    placeholder="example.com"
                />
                <InputError class="mt-2" :message="form.errors.domain" />
            </div>

            <div>
                <InputLabel for="dkim_selector" value="DKIM selector" />
                <TextInput
                    id="dkim_selector"
                    v-model="form.dkim_selector"
                    class="mt-1 block w-full"
                />
                <InputError class="mt-2" :message="form.errors.dkim_selector" />
                <p class="mt-1 text-xs text-slate-500">
                    Names the DKIM record. `mail` is fine unless something else
                    already signs for this domain.
                </p>
            </div>

            <label class="flex items-center gap-2">
                <Checkbox
                    v-model:checked="form.manage_dns"
                    :disabled="!dnsAccounts.length"
                />
                <span class="text-sm text-slate-700">
                    Publish the DNS records for me
                </span>
            </label>

            <p v-if="!dnsAccounts.length" class="text-xs text-slate-500">
                No DNS provider is connected, so the panel will show you the
                records to add by hand. Connect one from
                <Link :href="route('dns.index')" class="text-brand-600 hover:underline"
                    >DNS</Link
                >
                to have them published for you.
            </p>

            <div v-if="form.manage_dns">
                <InputLabel for="dns_account_id" value="DNS provider" />
                <select
                    id="dns_account_id"
                    v-model="form.dns_account_id"
                    class="mt-1 block w-full rounded-lg border-slate-300 shadow-sm focus:border-brand-500 focus:ring-brand-500"
                >
                    <option
                        v-for="account in dnsAccounts"
                        :key="account.id"
                        :value="account.id"
                    >
                        {{ account.label }} — {{ account.provider_label }}
                    </option>
                </select>
                <InputError class="mt-2" :message="form.errors.dns_account_id" />
            </div>

            <div class="flex items-center gap-3 border-t border-slate-100 pt-6">
                <PrimaryButton :disabled="form.processing || !mailConfigured">
                    Add domain
                </PrimaryButton>
                <Link
                    :href="route('email.index')"
                    class="text-sm text-slate-500 hover:text-slate-900"
                >
                    Cancel
                </Link>
            </div>

            <p class="text-xs text-slate-500">
                Deliverability also needs a reverse DNS (PTR) record for the
                server IP pointing at the mail hostname. Most providers expose
                this in their control panel; the panel cannot set it for you.
            </p>
        </form>
    </AuthenticatedLayout>
</template>
